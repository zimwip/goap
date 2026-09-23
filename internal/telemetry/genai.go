package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/llm"
)

// Baggage keys propagating the GOAP caller from the engine to the model
// gateway, so that LLM spans and token metrics are attributed.
const (
	BaggageProcess     = "goap.process.id"
	BaggageMethodology = "goap.methodology"
	BaggageAgent       = "goap.agent"
	BaggageAction      = "goap.action"
)

// WithCallerBaggage copies the engine caller attributes of ctx into the
// OpenTelemetry baggage (propagated to downstream services).
func WithCallerBaggage(ctx context.Context) context.Context {
	a, ok := engine.AttributesFrom(ctx)
	if !ok {
		return ctx
	}
	b := baggage.FromContext(ctx)
	for k, v := range map[string]string{BaggageProcess: a.ProcessID, BaggageMethodology: a.Methodology, BaggageAgent: a.Agent, BaggageAction: a.Action} {
		if v == "" {
			continue
		}
		if m, err := baggage.NewMember(k, v); err == nil {
			if nb, err := b.SetMember(m); err == nil {
				b = nb
			}
		}
	}
	return baggage.ContextWithBaggage(ctx, b)
}

// LLMClient instruments an llm.Client: it propagates the caller baggage.
type LLMClient struct{ Next llm.Client }

// Complete implements llm.Client.
func (c LLMClient) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return c.Next.Complete(WithCallerBaggage(ctx), req)
}

// GenAI instruments LLM calls with the OpenTelemetry GenAI semantic
// conventions: one "chat <model>" client span per call and the
// gen_ai.client.token.usage / gen_ai.client.operation.duration metrics,
// attributed to the GOAP agent and action carried by the baggage.
type GenAI struct {
	tracer   trace.Tracer
	tokens   metric.Int64Histogram
	duration metric.Float64Histogram
}

// NewGenAI returns the LLM instrumentation.
func NewGenAI() *GenAI {
	m := otel.Meter(scope)
	g := &GenAI{tracer: otel.Tracer(scope)}
	g.tokens, _ = m.Int64Histogram("gen_ai.client.token.usage", metric.WithDescription("Number of input and output tokens used"), metric.WithUnit("{token}"))
	g.duration, _ = m.Float64Histogram("gen_ai.client.operation.duration", metric.WithDescription("GenAI operation duration"), metric.WithUnit("s"))
	return g
}

// Instrument wraps one completion on provider/model.
func (g *GenAI) Instrument(ctx context.Context, provider, model string, req llm.Request, call func(context.Context) (llm.Response, error)) (llm.Response, error) {
	caller := []attribute.KeyValue{}
	bag := baggage.FromContext(ctx)
	for _, k := range []string{BaggageProcess, BaggageMethodology, BaggageAgent, BaggageAction} {
		if v := bag.Member(k).Value(); v != "" {
			caller = append(caller, attribute.String(k, v))
		}
	}
	attrs := append([]attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.system", provider),
		attribute.String("gen_ai.request.model", model),
		attribute.Int("gen_ai.request.max_tokens", req.MaxTokens),
	}, caller...)
	ctx, span := g.tracer.Start(ctx, "chat "+model, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	defer span.End()
	start := time.Now()
	resp, err := call(ctx)
	metricAttrs := []attribute.KeyValue{attribute.String("gen_ai.operation.name", "chat"), attribute.String("gen_ai.system", provider),
		attribute.String("gen_ai.request.model", model)}
	for _, kv := range caller {
		if kv.Key != BaggageProcess { // keep metric cardinality bounded
			metricAttrs = append(metricAttrs, kv)
		}
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		metricAttrs = append(metricAttrs, attribute.String("error.type", "error"))
	} else {
		span.SetAttributes(attribute.String("gen_ai.response.model", resp.Model),
			attribute.Int("gen_ai.usage.input_tokens", resp.Usage.InputTokens),
			attribute.Int("gen_ai.usage.output_tokens", resp.Usage.OutputTokens))
		ma := metric.WithAttributes(append(metricAttrs, attribute.String("gen_ai.response.model", resp.Model))...)
		g.tokens.Record(ctx, int64(resp.Usage.InputTokens), ma, metric.WithAttributes(attribute.String("gen_ai.token.type", "input")))
		g.tokens.Record(ctx, int64(resp.Usage.OutputTokens), ma, metric.WithAttributes(attribute.String("gen_ai.token.type", "output")))
	}
	g.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(metricAttrs...))
	return resp, err
}
