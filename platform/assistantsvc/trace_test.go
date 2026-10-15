package assistantsvc

import (
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestATurnIsAnInvokeAgentSpanWithItsTools(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	e := newEnv(t, call("delete_everything", `{"x":1}`), `{"message":"I cannot."}`)
	if _, _, err := e.send("do it", Context{}); err != nil {
		t.Fatal(err)
	}
	e.answer(t) // the turn has ended
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		byName[s.Name()] = s
	}
	turn, tool := byName["invoke_agent assistant"], byName["execute_tool delete_everything"]
	if turn == nil || tool == nil {
		t.Fatalf("spans %v", byName)
	}
	get := func(s sdktrace.ReadOnlySpan, k string) attribute.Value {
		for _, kv := range s.Attributes() {
			if string(kv.Key) == k {
				return kv.Value
			}
		}
		return attribute.Value{}
	}
	if get(turn, "goap.assistant.rounds").AsInt64() != 2 || get(turn, "goap.tool_calls").AsInt64() != 1 ||
		get(turn, "gen_ai.conversation.id").AsString() == "" || get(turn, "gen_ai.agent.name").AsString() != "assistant" {
		t.Fatalf("turn attributes %v", turn.Attributes())
	}
	if tool.Parent().SpanID() != turn.SpanContext().SpanID() || get(tool, "error.type").AsString() != "_OTHER" {
		t.Fatalf("tool span %v parent %v", tool.Attributes(), tool.Parent())
	}
}
