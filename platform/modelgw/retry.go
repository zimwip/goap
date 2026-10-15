package modelgw

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/zimwip/goap/contracts/llm"
)

// Retries of a provider call: a transient failure (a timeout, a rate limit, an overloaded or failing server, a
// connection dropped) is tried again with an exponential backoff and jitter, honouring the provider's Retry-After.
// The Anthropic SDK retries on its own (NewAnthropic): its errors are not StatusErrors and are never retried here.
const (
	DefaultRetries = 3
	retryBase      = time.Second
	retryCap       = 30 * time.Second
	retryAfterCap  = time.Minute
)

// StatusError is a provider answer with an HTTP error status.
type StatusError struct {
	Code       int
	RetryAfter time.Duration // from the Retry-After header, 0 when absent
	Msg        string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Code, http.StatusText(e.Code), e.Msg)
}

// ErrorType implements the classification of llm.ErrorType: the HTTP status.
func (e *StatusError) ErrorType() string { return strconv.Itoa(e.Code) }

// sdkError is an error of a provider SDK that retries on its own: classified by its HTTP status like a StatusError,
// never retried by the router.
type sdkError struct {
	code int
	err  error
}

func (e *sdkError) Error() string     { return e.err.Error() }
func (e *sdkError) Unwrap() error     { return e.err }
func (e *sdkError) ErrorType() string { return strconv.Itoa(e.code) }

func statusErrorOf(resp *http.Response, body string) *StatusError {
	e := &StatusError{Code: resp.StatusCode, Msg: body}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			e.RetryAfter = time.Duration(n) * time.Second
		} else if t, err := http.ParseTime(s); err == nil {
			e.RetryAfter = max(time.Until(t), 0)
		}
	}
	return e
}

// retryable says whether err is transient, and the wait the provider asked for (0: none).
func retryable(err error) (bool, time.Duration) {
	var se *StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests,
			http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
			return true, se.RetryAfter
		}
		return false, 0
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0
	}
	var ne net.Error
	return errors.As(err, &ne), 0
}

// backoff is the wait before the attempt-th retry (0-based).
func (r *Router) backoff(attempt int, asked time.Duration) time.Duration {
	base := r.RetryBase
	if base <= 0 {
		base = retryBase
	}
	d := min(base<<attempt, retryCap)
	d = d/2 + rand.N(d/2+1) // jitter: [d/2, d]
	return max(d, min(asked, retryAfterCap))
}

// completeWithRetry calls p, retrying the transient failures up to Router.Retries times.
func (r *Router) completeWithRetry(ctx context.Context, p Provider, model string, req llm.Request) (llm.Response, error) {
	retries := r.Retries
	if retries == 0 {
		retries = DefaultRetries
	}
	for attempt := 0; ; attempt++ {
		resp, err := p.Complete(ctx, model, req)
		ok, asked := retryable(err)
		if err == nil || !ok || attempt >= retries {
			return resp, err
		}
		wait := r.backoff(attempt, asked)
		llm.RetryObserverFrom(ctx)(attempt+1, err, wait)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return resp, err
		case <-t.C:
		}
	}
}
