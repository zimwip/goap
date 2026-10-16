package modelgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/llm"
)

// openAIServer answers with the given statuses in turn, then a completion.
func openAIServer(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if i < len(statuses) {
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"error":"busy"}`, statuses[i])
			return
		}
		_, _ = io.WriteString(w, `{"model":"m1","choices":[{"message":{"content":"hi"},"finish_reason":"length"}],
			"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":64}}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func routerOn(url string) *Router {
	r := NewRouter()
	r.RetryBase = time.Millisecond
	r.AddProvider("oa", &OpenAICompatible{BaseURL: url})
	return r
}

func TestRouterRetriesTransientFailures(t *testing.T) {
	srv, n := openAIServer(t, http.StatusTooManyRequests, http.StatusServiceUnavailable)
	resp, err := routerOn(srv.URL).Complete(context.Background(), llm.Request{Model: "oa/m1"})
	if err != nil || resp.Text != "hi" || n.Load() != 3 {
		t.Fatalf("resp %+v err %v after %d calls", resp, err, n.Load())
	}
	if resp.StopReason != llm.StopMaxTokens || !resp.Truncated() || resp.Usage.CacheReadTokens != 64 || resp.Usage.InputTokens != 100 {
		t.Fatalf("stop %q usage %+v", resp.StopReason, resp.Usage)
	}
}

func TestRouterDoesNotRetryAClientError(t *testing.T) {
	srv, n := openAIServer(t, http.StatusBadRequest)
	_, err := routerOn(srv.URL).Complete(context.Background(), llm.Request{Model: "oa/m1"})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusBadRequest || n.Load() != 1 {
		t.Fatalf("err %v after %d calls", err, n.Load())
	}
}

func TestRouterGivesUpAfterItsRetries(t *testing.T) {
	srv, n := openAIServer(t, 500, 500, 500, 500, 500)
	r := routerOn(srv.URL)
	r.Retries = 2
	if _, err := r.Complete(context.Background(), llm.Request{Model: "oa/m1"}); err == nil || n.Load() != 3 {
		t.Fatalf("err %v after %d calls", err, n.Load())
	}
	r.Retries = -1 // none
	n.Store(0)
	if _, err := r.Complete(context.Background(), llm.Request{Model: "oa/m1"}); err == nil || n.Load() != 1 {
		t.Fatalf("err %v after %d calls", err, n.Load())
	}
}

func TestRouterRetryStopsWithTheContext(t *testing.T) {
	srv, _ := openAIServer(t, 503, 503, 503, 503)
	r := routerOn(srv.URL)
	r.RetryBase = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := r.Complete(ctx, llm.Request{Model: "oa/m1"}); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
}

func TestStatusErrorReadsRetryAfter(t *testing.T) {
	resp := &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"7"}}}
	if e := statusErrorOf(resp, "x"); e.RetryAfter != 7*time.Second {
		t.Fatalf("retry after %s", e.RetryAfter)
	}
	r := NewRouter()
	if d := r.backoff(0, 7*time.Second); d != 7*time.Second {
		t.Fatalf("backoff %s, want the provider's 7s", d)
	}
	if d := r.backoff(10, 0); d > retryCap || d < retryCap/2 {
		t.Fatalf("backoff %s out of [%s, %s]", d, retryCap/2, retryCap)
	}
}

func TestAnthropicMarksTheCachePrefixOnRequest(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-x","stop_reason":"end_turn",
			"content":[{"type":"text","text":"ok"}],
			"usage":{"input_tokens":10,"output_tokens":3,"cache_read_input_tokens":2000,"cache_creation_input_tokens":500}}`)
	}))
	defer srv.Close()
	a := NewAnthropic("k", srv.URL)
	req := llm.Request{System: "sys", Messages: []llm.Message{{Role: "user", Content: "hi"}}}
	if _, err := a.Complete(context.Background(), "claude-x", req); err != nil {
		t.Fatal(err)
	}
	req.Cache = true
	resp, err := a.Complete(context.Background(), "claude-x", req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mustJSON(bodies[0]), "cache_control") {
		t.Fatalf("no cache asked, marked anyway: %v", bodies[0])
	}
	const mark = `"cache_control":{"type":"ephemeral"}`
	if !strings.Contains(mustJSON(bodies[1]["system"]), mark) || !strings.Contains(mustJSON(bodies[1]["messages"]), mark) {
		t.Fatalf("cache asked, not marked: %v", bodies[1])
	}
	u := resp.Usage
	if u.InputTokens != 2510 || u.CacheReadTokens != 2000 || u.CacheWriteTokens != 500 || resp.StopReason != llm.StopEnd {
		t.Fatalf("usage %+v stop %q", u, resp.StopReason)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
