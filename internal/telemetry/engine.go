package telemetry

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/zimwip/goap/pkg/engine"
)

const scope = "github.com/zimwip/goap"

// EngineTracer implements engine.Tracer with OpenTelemetry. A process keeps
// a single trace across its runs: the W3C traceparent of its root span is
// stored in the process and background runs continue it.
type EngineTracer struct {
	tracer  trace.Tracer
	actions metric.Int64Counter
	actDur  metric.Float64Histogram
	tokens  metric.Int64Counter
}

var _ engine.Tracer = (*EngineTracer)(nil)

// NewEngineTracer returns the engine instrumentation.
func NewEngineTracer() *EngineTracer {
	m := otel.Meter(scope)
	t := &EngineTracer{tracer: otel.Tracer(scope)}
	t.actions, _ = m.Int64Counter("goap.actions", metric.WithDescription("Executed actions"), metric.WithUnit("{action}"))
	t.actDur, _ = m.Float64Histogram("goap.action.duration", metric.WithDescription("Action duration"), metric.WithUnit("s"))
	t.tokens, _ = m.Int64Counter("goap.tokens", metric.WithDescription("LLM tokens consumed by actions"), metric.WithUnit("{token}"))
	return t
}

func processAttrs(p *engine.Process) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("goap.process.id", p.ID),
		attribute.String("goap.methodology", p.Methodology),
		attribute.String("goap.agent", p.Agent),
		attribute.String("goap.planner", p.Planner),
		attribute.String("goap.goal", p.Goal),
		attribute.String("goap.change.id", string(p.ChangeID)),
		attribute.String("goap.parent.id", p.ParentID),
		attribute.String("enduser.id", p.Initiator.Subject),
	}
}

// StartProcess implements engine.Tracer.
func (t *EngineTracer) StartProcess(ctx context.Context, p *engine.Process) (context.Context, func(*engine.Process)) {
	if !trace.SpanContextFromContext(ctx).IsValid() && p.TraceParent != "" {
		ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": p.TraceParent})
	}
	ctx, span := t.tracer.Start(ctx, "process "+p.Agent, trace.WithAttributes(processAttrs(p)...))
	if p.TraceParent == "" {
		carrier := propagation.MapCarrier{}
		propagation.TraceContext{}.Inject(ctx, carrier)
		p.TraceParent = carrier["traceparent"]
		p.TraceID = span.SpanContext().TraceID().String()
	}
	return ctx, func(p *engine.Process) {
		span.SetAttributes(attribute.String("goap.status", string(p.Status)), attribute.Int("goap.steps", len(p.Steps)),
			attribute.Int64("gen_ai.usage.input_tokens", p.Usage.InputTokens), attribute.Int64("gen_ai.usage.output_tokens", p.Usage.OutputTokens),
			attribute.Int("goap.llm_calls", p.Usage.LLMCalls), attribute.Int("goap.tool_calls", p.Usage.ToolCalls))
		if p.Status == engine.StatusFailed {
			span.SetStatus(codes.Error, p.Error)
		}
		span.End()
	}
}

// StartAction implements engine.Tracer.
func (t *EngineTracer) StartAction(ctx context.Context, p *engine.Process, action, kind string) (context.Context, func(*engine.Step, error)) {
	attrs := append(processAttrs(p), attribute.String("goap.action", action), attribute.String("goap.action.kind", kind))
	ctx, span := t.tracer.Start(ctx, "action "+action, trace.WithAttributes(attrs...))
	start := time.Now()
	return ctx, func(s *engine.Step, err error) {
		outcome := "ok"
		switch {
		case err != nil:
			outcome = "error"
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		case s != nil && s.Pending():
			outcome = "waiting"
		}
		if s != nil {
			span.SetAttributes(attribute.Int64("gen_ai.usage.input_tokens", s.Usage.InputTokens),
				attribute.Int64("gen_ai.usage.output_tokens", s.Usage.OutputTokens),
				attribute.Int("goap.llm_calls", s.Usage.LLMCalls), attribute.Int("goap.tool_calls", s.Usage.ToolCalls),
				attribute.Int("goap.items", len(s.Items)), attribute.String("goap.sandbox", s.Sandbox))
			for _, c := range s.ToolCalls {
				span.AddEvent("tool "+c.Name, trace.WithAttributes(attribute.Int64("duration_ms", c.DurationMs), attribute.String("error", c.Error)))
			}
			base := metric.WithAttributes(attribute.String("goap.agent", p.Agent), attribute.String("goap.action", action))
			t.tokens.Add(ctx, s.Usage.InputTokens, base, metric.WithAttributes(attribute.String("gen_ai.token.type", "input")))
			t.tokens.Add(ctx, s.Usage.OutputTokens, base, metric.WithAttributes(attribute.String("gen_ai.token.type", "output")))
		}
		ma := metric.WithAttributes(attribute.String("goap.agent", p.Agent), attribute.String("goap.action", action),
			attribute.String("goap.action.kind", kind), attribute.String("goap.outcome", outcome))
		t.actions.Add(ctx, 1, ma)
		t.actDur.Record(ctx, time.Since(start).Seconds(), ma)
		span.SetAttributes(attribute.String("goap.outcome", outcome))
		span.End()
	}
}

// StartTool implements engine.Tracer (GenAI "execute_tool" span).
func (t *EngineTracer) StartTool(ctx context.Context, p *engine.Process, action, tool string) (context.Context, func(error)) {
	ctx, span := t.tracer.Start(ctx, "execute_tool "+tool, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "execute_tool"), attribute.String("gen_ai.tool.name", tool),
		attribute.String("goap.process.id", p.ID), attribute.String("goap.agent", p.Agent), attribute.String("goap.action", action)))
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// TraceID implements engine.Tracer.
func (t *EngineTracer) TraceID(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		return sc.TraceID().String()
	}
	return ""
}

var errNoSpan = errors.New("no span")

var _ = errNoSpan
