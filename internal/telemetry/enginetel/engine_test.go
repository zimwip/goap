package enginetel

import (
	"context"
	"errors"
	"io"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/llm"
)

func recorder(t *testing.T) *tracetest.SpanRecorder {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

func attr(s sdktrace.ReadOnlySpan, key string) attribute.Value {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value
		}
	}
	return attribute.Value{}
}

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

func TestGenAISpanAttributedToAgent(t *testing.T) {
	rec := recorder(t)
	g := telemetry.NewGenAI()
	ctx := engine.WithAttributes(context.Background(), engine.Attributes{ProcessID: "p1", Agent: "test-designer", Action: "summarize"})
	ctx = WithCallerBaggage(ctx)
	_, err := g.Instrument(ctx, "anthropic", "claude-opus-5", llm.Request{MaxTokens: 100}, func(context.Context) (llm.Response, error) {
		return llm.Response{Model: "claude-opus-5", Usage: llm.Usage{InputTokens: 12, OutputTokens: 5}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "chat claude-opus-5" {
		t.Fatalf("spans %v", spans)
	}
	s := spans[0]
	if attr(s, "gen_ai.usage.input_tokens").AsInt64() != 12 || attr(s, "gen_ai.usage.output_tokens").AsInt64() != 5 ||
		attr(s, "gen_ai.system").AsString() != "anthropic" || attr(s, "goap.agent").AsString() != "test-designer" ||
		attr(s, "goap.action").AsString() != "summarize" {
		t.Fatalf("attributes %v", s.Attributes())
	}
}

func TestProcessTraceContinues(t *testing.T) {
	rec := recorder(t)
	tr := NewEngineTracer()
	p := &engine.Process{ID: "p1", Agent: "coordinator"}
	_, end := tr.StartProcess(context.Background(), p)
	end(p)
	if p.TraceParent == "" || p.TraceID == "" {
		t.Fatal("trace parent not stored on the process")
	}
	// a later background run continues the same trace
	ctx, end2 := tr.StartProcess(context.Background(), p)
	_, endAction := tr.StartAction(ctx, p, "delegate", "script")
	endAction(&engine.Step{Usage: engine.Usage{InputTokens: 3, LLMCalls: 1}}, nil)
	end2(p)
	spans := rec.Ended()
	if len(spans) != 3 {
		t.Fatalf("expected 3 spans, got %d", len(spans))
	}
	for _, s := range spans {
		if s.SpanContext().TraceID().String() != p.TraceID {
			t.Fatalf("span %s is not in the process trace", s.Name())
		}
	}
	var action sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == "action delegate" {
			action = s
		}
	}
	if action == nil || attr(action, "gen_ai.usage.input_tokens").AsInt64() != 3 || attr(action, "goap.action.kind").AsString() != "script" {
		t.Fatalf("action span missing or wrong: %v", action)
	}
}
