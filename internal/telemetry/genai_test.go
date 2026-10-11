package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/llm"
)

func one(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range spans {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no span %q in %d spans", name, len(spans))
	return nil
}

func TestGenAISpanCarriesCallerCacheAndStop(t *testing.T) {
	rec := recorder(t)
	g := NewGenAI()
	ctx := llm.WithMeta(context.Background(), llm.CallMeta{Source: llm.SourceAssistant, ConversationID: "c1", ChangeID: "ch1"})
	req := llm.Request{System: "be brief", Messages: []llm.Message{{Role: "user", Content: "hi"}}, JSON: true, Cache: true}
	_, _ = g.Instrument(ctx, "anthropic", "claude-x", req, func(context.Context) (llm.Response, error) {
		return llm.Response{Text: `{"a":1}`, Model: "claude-x", StopReason: llm.StopMaxTokens, Behaviors: []string{"terse"},
			Usage: llm.Usage{InputTokens: 100, OutputTokens: 5, CacheReadTokens: 80, CacheWriteTokens: 10}}, nil
	})
	s := one(t, rec.Ended(), "chat claude-x")
	for k, want := range map[string]string{"goap.llm.source": "assistant", "gen_ai.conversation.id": "c1", "goap.change.id": "ch1",
		"gen_ai.provider.name": "anthropic", "gen_ai.output.type": "json"} {
		if got := attr(s, k).AsString(); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if attr(s, "gen_ai.usage.cache_read.input_tokens").AsInt64() != 80 || attr(s, "gen_ai.usage.cache_creation.input_tokens").AsInt64() != 10 ||
		attr(s, "gen_ai.response.finish_reasons").AsStringSlice()[0] != "max_tokens" || !attr(s, "goap.llm.cache").AsBool() ||
		attr(s, "goap.llm.behaviors").AsStringSlice()[0] != "terse" {
		t.Fatalf("attributes %v", s.Attributes())
	}
	// no content by default
	if attr(s, "gen_ai.input.messages").AsString() != "" || attr(s, "gen_ai.system_instructions").AsString() != "" {
		t.Fatal("content recorded without being asked")
	}
}

func TestGenAICapturesContentOnRequest(t *testing.T) {
	rec := recorder(t)
	g := NewGenAI()
	g.Content = true
	long := strings.Repeat("é", MaxContent) // 2 bytes a rune: cut on a rune boundary
	_, _ = g.Instrument(context.Background(), "p", "m", llm.Request{System: long, Messages: []llm.Message{{Role: "user", Content: "hi"}}},
		func(context.Context) (llm.Response, error) { return llm.Response{Text: "hello"}, nil })
	s := one(t, rec.Ended(), "chat m")
	sys := attr(s, "gen_ai.system_instructions").AsString()
	if !strings.HasSuffix(sys, "…(truncated)") || len(sys) > MaxContent+len("…(truncated)") || !strings.HasPrefix(sys, "éé") {
		t.Fatalf("system instructions %d bytes", len(sys))
	}
	if attr(s, "gen_ai.input.messages").AsString() != `[{"role":"user","parts":[{"type":"text","content":"hi"}]}]` ||
		attr(s, "gen_ai.output.messages").AsString() != `[{"role":"assistant","parts":[{"type":"text","content":"hello"}]}]` {
		t.Fatalf("attributes %v", s.Attributes())
	}
}

func TestGenAIRecordsRetriesAndTheErrorType(t *testing.T) {
	rec := recorder(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer srv.Close()
	r := modelgw.NewRouter()
	r.RetryBase = time.Millisecond
	r.AddProvider("oa", &modelgw.OpenAICompatible{BaseURL: srv.URL})
	r.Instrument = NewGenAI().Instrument
	if _, err := r.Complete(context.Background(), llm.Request{Model: "oa/m"}); err == nil {
		t.Fatal("the 400 must fail the call")
	}
	s := one(t, rec.Ended(), "chat m")
	if attr(s, "error.type").AsString() != "400" {
		t.Fatalf("error.type %q", attr(s, "error.type").AsString())
	}
	var retries []string
	for _, e := range s.Events() {
		if e.Name == "gen_ai.retry" {
			for _, kv := range e.Attributes {
				if kv.Key == "error.type" {
					retries = append(retries, kv.Value.AsString())
				}
			}
		}
	}
	if strings.Join(retries, ",") != "429,429" {
		t.Fatalf("retry events %v", retries)
	}
}

func TestGenAIEmbeddingsSpan(t *testing.T) {
	rec := recorder(t)
	r := modelgw.NewRouter()
	r.AddProvider("f", modelgw.Fake{})
	r.InstrumentEmbed = NewGenAI().InstrumentEmbed
	ctx := llm.WithMeta(context.Background(), llm.CallMeta{Source: llm.SourceIndexer})
	if _, err := r.Embed(ctx, llm.EmbedRequest{Model: "f/e", Texts: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	s := one(t, rec.Ended(), "embeddings e")
	if attr(s, "gen_ai.operation.name").AsString() != "embeddings" || attr(s, "goap.llm.texts").AsInt64() != 2 ||
		attr(s, "goap.llm.source").AsString() != "indexer" {
		t.Fatalf("attributes %v", s.Attributes())
	}
}

func TestEngineToolSpanAndEvents(t *testing.T) {
	rec := recorder(t)
	tr := NewEngineTracer()
	p := &engine.Process{ID: "p1", Agent: "a"}
	ctx, endAction := tr.StartAction(context.Background(), p, "act", "llm")
	tr.Event(ctx, "llm.repair", map[string]any{"goap.llm.repair": 1, "goap.llm.truncated": true, "error": "bad"})
	_, endTool := tr.StartTool(ctx, p, "act", "fs/read")
	endTool(context.DeadlineExceeded)
	endAction(nil, nil)
	spans := rec.Ended()
	tool := one(t, spans, "execute_tool fs/read")
	if attr(tool, "goap.mcp").AsString() != "fs" || attr(tool, "error.type").AsString() != "timeout" {
		t.Fatalf("tool attributes %v", tool.Attributes())
	}
	action := one(t, spans, "action act")
	if len(action.Events()) != 1 || action.Events()[0].Name != "llm.repair" || len(action.Events()[0].Attributes) != 3 {
		t.Fatalf("events %v", action.Events())
	}
}

func TestErrorTypes(t *testing.T) {
	for err, want := range map[error]string{
		nil:                                  "",
		llm.ErrRefusal:                       "refusal",
		context.Canceled:                     "canceled",
		errors.New("x"):                      "_OTHER",
		&modelgw.StatusError{Code: 503}:      "503",
		errors.Join(errors.New("w"), io.EOF): "_OTHER",
	} {
		if got := errorType(err); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}
}
