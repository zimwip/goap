package modelgw

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/lib/modeltoken"
)

const sseAnswer = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":4,"cache_read_input_tokens":8,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}` + "\n\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}` + "\n\n" +
	`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","name":"Edit"}}` + "\n\n" +
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file\":1}"}}` + "\n\n" +
	`data: {"type":"content_block_stop","index":1}` + "\n\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}` + "\n\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// proxyFixture is a gateway whose catalog holds an Anthropic provider played by a fake API.
func proxyFixture(t *testing.T) (*AnthropicProxy, *Service, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-provider" || r.Header.Get("anthropic-beta") != "tools-1" {
			http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`, http.StatusUnauthorized)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["model"] != "claude-test" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"model"}}`)
			return
		}
		switch {
		case r.URL.Path == "/v1/messages/count_tokens":
			_, _ = io.WriteString(w, `{"input_tokens":42}`)
		case req["stream"] == true:
			w.Header().Set("Content-Type", "text/event-stream")
			for _, chunk := range strings.SplitAfter(sseAnswer, "\n\n") { // the events come in pieces
				_, _ = io.WriteString(w, chunk[:len(chunk)/2])
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, chunk[len(chunk)/2:])
			}
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"cache_creation_input_tokens":5,"output_tokens":7}}`)
		}
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("TEST_PROXY_ANTHROPIC_KEY", "sk-provider")
	svc, g := newService(NewMemoryStore())
	seed(t, g, []ProviderRecord{{Name: "claude", Kind: "anthropic", Protocol: "anthropic", BaseURL: upstream.URL, Enabled: true, APIKeyRef: "env:TEST_PROXY_ANTHROPIC_KEY"}},
		[]ModelEntry{{Provider: "claude", Model: "claude-test", Enabled: true, QuotaTokens: 60, QuotaPeriod: PeriodTotal}},
		[]AliasEntry{{Alias: "coder", Target: "claude/claude-test"}})
	return &AnthropicProxy{Service: svc, Secret: []byte("s3cret")}, svc, upstream
}

func proxyToken(t *testing.T, ttl time.Duration) string {
	t.Helper()
	tok, err := modeltoken.Sign([]byte("s3cret"), modeltoken.Claims{Subject: authz.System("connector:claude-coding-agent").Subject, EndUser: "alice",
		Org: "ORG-A", Project: "P1", Change: "CH-1", Process: "proc-1", Source: "connector:claude-coding-agent"}, time.Now(), ttl)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func post(t *testing.T, h http.Handler, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("x-api-key", token)
	r.Header.Set("anthropic-version", "2023-06-01")
	r.Header.Set("anthropic-beta", "tools-1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The calls of a tool calling the model by itself are rows of the ledger, attributed to the person, the change and the
// process of the token, with their tokens and their exchange; the request and the answer pass as they are.
func TestAnthropicProxyRecordsEveryCall(t *testing.T) {
	p, svc, _ := proxyFixture(t)
	tok := proxyToken(t, time.Hour)
	w := post(t, p, "/anthropic/v1/messages", tok, `{"model":"claude-test","max_tokens":100,"system":[{"type":"text","text":"be brief"}],"messages":[{"role":"user","content":"fix the build"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"text":"done"`) {
		t.Fatalf("answer %d %s", w.Code, w.Body)
	}
	w = post(t, p, "/anthropic/v1/messages", tok, `{"model":"coder","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"go on"}]}]}`)
	if w.Code != http.StatusOK || w.Body.String() != sseAnswer || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("the stream must pass as it is: %d %q", w.Code, w.Body)
	}
	rows := calls(t, svc, UsageFilter{})
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	for _, c := range rows {
		if c.Subject != "system:connector:claude-coding-agent" || c.EndUser != "alice" || c.ChangeID != "CH-1" || c.ProcessID != "proc-1" ||
			c.Source != "connector:claude-coding-agent" || c.Project != "P1" || c.Org != "ORG-A" || c.Provider != "claude" || c.Model != "claude-test" || c.Error != "" {
			t.Fatalf("row %+v", c)
		}
	}
	if rows[0].InputTokens != 15 || rows[0].OutputTokens != 7 || rows[1].InputTokens != 12 || rows[1].OutputTokens != 9 || rows[1].Alias != "coder" {
		t.Fatalf("tokens %+v", rows)
	}
	_, x, err := svc.CallExchange(authz.With(context.Background(), authz.Principal{Subject: "alice"}), rows[1].Seq, false)
	if err != nil || x.Response != "Hello\n[tool_use Edit {\"file\":1}]" || x.Messages[0].Content != "go on" {
		t.Fatalf("alice reads the exchange of the session's call: %v %+v", err, x)
	}
	if _, x, _ := svc.CallExchange(authz.With(context.Background(), authz.Principal{Subject: "alice"}), rows[0].Seq, false); x.System != "be brief" || x.Response != "done" {
		t.Fatalf("exchange %+v", x)
	}
	// the quota counts them: 22 + 21 of 60, the next one is refused before the provider
	w = post(t, p, "/anthropic/v1/messages", tok, `{"model":"claude-test","messages":[]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("third call %d %s", w.Code, w.Body)
	}
	w = post(t, p, "/anthropic/v1/messages", tok, `{"model":"claude-test","messages":[]}`)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("quota: %d %s", w.Code, w.Body)
	}
	if rows = calls(t, svc, UsageFilter{}); len(rows) != 4 || !strings.Contains(rows[3].Error, "quota") {
		t.Fatalf("the refusal is a row too: %+v", rows)
	}
}

// Only a valid token is served; a model outside the catalog or not served over the Anthropic protocol is refused, with
// a row; counting tokens makes no row.
func TestAnthropicProxyRefuses(t *testing.T) {
	p, svc, _ := proxyFixture(t)
	body := `{"model":"claude-test","messages":[]}`
	for _, tok := range []string{"", "nope", proxyToken(t, -time.Minute)} {
		if w := post(t, p, "/anthropic/v1/messages", tok, body); w.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: %d", tok, w.Code)
		}
	}
	if w := post(t, p, "/anthropic/v1/complete", proxyToken(t, time.Hour), body); w.Code != http.StatusNotFound {
		t.Fatalf("other paths: %d", w.Code)
	}
	if len(calls(t, svc, UsageFilter{})) != 0 {
		t.Fatal("no row without a valid token")
	}
	if w := post(t, p, "/anthropic/v1/messages", proxyToken(t, time.Hour), `{"model":"claude-other","messages":[]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown model: %d %s", w.Code, w.Body)
	}
	if w := post(t, p, "/anthropic/v1/messages/count_tokens", proxyToken(t, time.Hour), body); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "42") {
		t.Fatalf("count_tokens: %d %s", w.Code, w.Body)
	}
	if rows := calls(t, svc, UsageFilter{}); len(rows) != 1 || rows[0].Error == "" {
		t.Fatalf("the refused model is one row, count_tokens none: %+v", rows)
	}
}
