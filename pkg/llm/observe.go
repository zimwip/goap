package llm

import (
	"context"
	"errors"
	"time"
)

// ErrRefusal is the error of a call the model declined.
var ErrRefusal = errors.New("model refused the request")

// ErrorType classifies the error of a call for telemetry (the OpenTelemetry error.type): the HTTP status of a
// provider answer, "timeout", "canceled", "refusal", else "_OTHER". An error states its own type with an
// ErrorType() string method.
func ErrorType(err error) string {
	var typed interface{ ErrorType() string }
	switch {
	case err == nil:
		return ""
	case errors.As(err, &typed):
		return typed.ErrorType()
	case errors.Is(err, ErrRefusal):
		return "refusal"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "_OTHER"
}

// RetryObserver is told of every retry of a provider call: the attempt that failed (1-based), its error and the
// wait before the next one.
type RetryObserver func(attempt int, err error, wait time.Duration)

type retryKey struct{}

// WithRetryObserver returns a context whose provider calls report their retries to o (the instrumentation of the
// gateway installs one per call).
func WithRetryObserver(ctx context.Context, o RetryObserver) context.Context {
	return context.WithValue(ctx, retryKey{}, o)
}

// RetryObserverFrom returns the observer of ctx, or one that does nothing.
func RetryObserverFrom(ctx context.Context) RetryObserver {
	if o, ok := ctx.Value(retryKey{}).(RetryObserver); ok && o != nil {
		return o
	}
	return func(int, error, time.Duration) {}
}
