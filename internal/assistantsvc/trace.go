package assistantsvc

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/zimwip/goap/pkg/llm"
)

// The spans of a turn (GenAI semantic conventions): "invoke_agent assistant" for the turn, a "chat" span per round
// (the gateway's), an "execute_tool <name>" per tool call. Without a configured provider they are no-ops.
var tracer = otel.Tracer("github.com/zimwip/goap/internal/assistantsvc")

// startTurn starts the span of a turn.
func (t *turn) startTurn(ctx context.Context) (context.Context, func(err error)) {
	ctx, span := tracer.Start(ctx, "invoke_agent "+agentName, trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "invoke_agent"),
		attribute.String("gen_ai.agent.name", agentName),
		attribute.String("gen_ai.conversation.id", t.conversation),
		attribute.String("gen_ai.request.model", Alias),
		attribute.String("enduser.id", t.p.Subject),
		attribute.String("goap.project", t.project),
		attribute.Int("goap.assistant.ui_tools", len(t.uiTools)),
	))
	return ctx, func(err error) {
		span.SetAttributes(attribute.Int("goap.assistant.rounds", t.rounds), attribute.Int("goap.tool_calls", t.toolCalls),
			attribute.Int("goap.assistant.actions", len(t.actions)), attribute.Int("goap.assistant.proposals", t.proposals))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			span.SetAttributes(attribute.String("error.type", llm.ErrorType(err)))
		}
		span.End()
	}
}

// startTool starts the span of a tool call.
func startTool(ctx context.Context, name string) (context.Context, func(err error)) {
	typ := "function"
	if len(name) > len(PrefixUI) && name[:len(PrefixUI)] == PrefixUI {
		typ = "extension" // a screen tool: run (or proposed) by the web, not here
	}
	ctx, span := tracer.Start(ctx, "execute_tool "+name, trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "execute_tool"),
		attribute.String("gen_ai.tool.name", name),
		attribute.String("gen_ai.tool.type", typ),
	))
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			span.SetAttributes(attribute.String("error.type", llm.ErrorType(err)))
		}
		span.End()
	}
}

// agentName is the name of the assistant as a GenAI agent.
const agentName = "assistant"
