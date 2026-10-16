package telemetry

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/zimwip/goap/contracts/llm"
)

// Baggage keys propagating the GOAP caller from the engine to the model
// gateway, so that LLM spans and token metrics are attributed.
const (
	BaggageProcess     = "goap.process.id"
	BaggageMethodology = "goap.methodology"
	BaggageAgent       = "goap.agent"
	BaggageAction      = "goap.action"
)

// GenAI instruments LLM calls with the OpenTelemetry GenAI semantic
// conventions: one "chat <model>" client span per completion and one
// "embeddings <model>" span per embedding call, the gen_ai.client.token.usage /
// gen_ai.client.operation.duration metrics, goap.llm.cache_tokens and
// goap.llm.retries, attributed to the GOAP agent and action carried by the
// baggage and to the source the caller declares (llm.CallMeta).
type GenAI struct {
	tracer   trace.Tracer
	tokens   metric.Int64Histogram
	duration metric.Float64Histogram
	cache    metric.Int64Counter
	retries  metric.Int64Counter
	// Content records the prompt and the answer on the chat spans (gen_ai.system_instructions,
	// gen_ai.input.messages, gen_ai.output.messages), each capped at MaxContent bytes. Off by default: the change
	// log and the ledger keep the exchanges (ADR 0059, 0089); NewGenAI turns it on when
	// OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT is "true".
	Content bool
}

// ContentEnv is the standard variable turning the capture of the message content on.
const ContentEnv = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"

// MaxContent caps each content attribute of a span (bytes).
const MaxContent = 16 << 10

// NewGenAI returns the LLM instrumentation.
func NewGenAI() *GenAI {
	m := otel.Meter(scope)
	g := &GenAI{tracer: otel.Tracer(scope), Content: strings.EqualFold(os.Getenv(ContentEnv), "true")}
	g.tokens, _ = m.Int64Histogram("gen_ai.client.token.usage", metric.WithDescription("Number of input and output tokens used"), metric.WithUnit("{token}"))
	g.duration, _ = m.Float64Histogram("gen_ai.client.operation.duration", metric.WithDescription("GenAI operation duration"), metric.WithUnit("s"))
	g.cache, _ = m.Int64Counter("goap.llm.cache_tokens", metric.WithDescription("Input tokens read from or written to the provider's prompt cache"), metric.WithUnit("{token}"))
	g.retries, _ = m.Int64Counter("goap.llm.retries", metric.WithDescription("Retries of a transient provider failure"), metric.WithUnit("{retry}"))
	return g
}

// callerAttrs are the attributes of the caller of a call: the engine baggage, and what the caller declares.
// span holds them all; metric only the bounded ones (no process, change or conversation id).
func callerAttrs(ctx context.Context) (span, metric []attribute.KeyValue) {
	bag := baggage.FromContext(ctx)
	for _, k := range []string{BaggageProcess, BaggageMethodology, BaggageAgent, BaggageAction} {
		if v := bag.Member(k).Value(); v != "" {
			span = append(span, attribute.String(k, v))
			if k != BaggageProcess {
				metric = append(metric, attribute.String(k, v))
			}
		}
	}
	m := llm.MetaFrom(ctx)
	span = append(span, attribute.String("goap.llm.source", m.Source))
	metric = append(metric, attribute.String("goap.llm.source", m.Source))
	if m.ConversationID != "" {
		span = append(span, attribute.String("gen_ai.conversation.id", m.ConversationID))
	}
	if m.ChangeID != "" {
		span = append(span, attribute.String("goap.change.id", m.ChangeID))
	}
	if m.ProcessID != "" && bag.Member(BaggageProcess).Value() == "" {
		span = append(span, attribute.String(BaggageProcess, m.ProcessID))
	}
	return span, metric
}

// observeRetries makes the retries of the provider call of ctx span events and goap.llm.retries.
func (g *GenAI) observeRetries(ctx context.Context, span trace.Span, metricAttrs []attribute.KeyValue) context.Context {
	return llm.WithRetryObserver(ctx, func(attempt int, err error, wait time.Duration) {
		typ := llm.ErrorType(err)
		span.AddEvent("gen_ai.retry", trace.WithAttributes(attribute.Int("goap.llm.attempt", attempt),
			attribute.String("error.type", typ), attribute.Int64("goap.llm.retry_wait_ms", wait.Milliseconds())))
		g.retries.Add(ctx, 1, metric.WithAttributes(append(slices.Clone(metricAttrs), attribute.String("error.type", typ))...))
	})
}

// fail records err on span and returns the metric attributes with its error.type.
func fail(span trace.Span, err error, metricAttrs []attribute.KeyValue) []attribute.KeyValue {
	typ := llm.ErrorType(err)
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(attribute.String("error.type", typ))
	return append(metricAttrs, attribute.String("error.type", typ))
}

// Instrument wraps one completion on provider/model.
func (g *GenAI) Instrument(ctx context.Context, provider, model string, req llm.Request, call func(context.Context) (llm.Response, error)) (llm.Response, error) {
	caller, callerMetric := callerAttrs(ctx)
	attrs := append([]attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.system", provider), // kept for the dashboards; gen_ai.provider.name is its successor
		attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", model),
		attribute.Int("gen_ai.request.max_tokens", req.MaxTokens),
		attribute.Bool("goap.llm.json", req.JSON),
		attribute.Bool("goap.llm.cache", req.Cache),
		attribute.Int("goap.llm.messages", len(req.Messages)),
	}, caller...)
	if req.JSON {
		attrs = append(attrs, attribute.String("gen_ai.output.type", "json"))
	}
	ctx, span := g.tracer.Start(ctx, "chat "+model, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	defer span.End()
	metricAttrs := append([]attribute.KeyValue{attribute.String("gen_ai.operation.name", "chat"), attribute.String("gen_ai.system", provider),
		attribute.String("gen_ai.provider.name", provider), attribute.String("gen_ai.request.model", model)}, callerMetric...)
	if g.Content {
		span.SetAttributes(attribute.String("gen_ai.system_instructions", capText(req.System)),
			attribute.String("gen_ai.input.messages", capText(messagesJSON(req.Messages))))
	}
	start := time.Now()
	resp, err := call(g.observeRetries(ctx, span, metricAttrs))
	if err != nil {
		metricAttrs = fail(span, err, metricAttrs)
	} else {
		span.SetAttributes(attribute.String("gen_ai.response.model", resp.Model),
			attribute.Int("gen_ai.usage.input_tokens", resp.Usage.InputTokens),
			attribute.Int("gen_ai.usage.output_tokens", resp.Usage.OutputTokens),
			attribute.Int("gen_ai.usage.cache_read.input_tokens", resp.Usage.CacheReadTokens),
			attribute.Int("gen_ai.usage.cache_creation.input_tokens", resp.Usage.CacheWriteTokens))
		if resp.StopReason != "" {
			span.SetAttributes(attribute.StringSlice("gen_ai.response.finish_reasons", []string{resp.StopReason}))
		}
		if len(resp.Behaviors) > 0 {
			span.SetAttributes(attribute.StringSlice("goap.llm.behaviors", resp.Behaviors))
		}
		if g.Content {
			span.SetAttributes(attribute.String("gen_ai.output.messages", capText(messagesJSON([]llm.Message{{Role: "assistant", Content: resp.Text}}))))
		}
		ma := append(slices.Clone(metricAttrs), attribute.String("gen_ai.response.model", resp.Model))
		g.tokens.Record(ctx, int64(resp.Usage.InputTokens), metric.WithAttributes(append(ma, attribute.String("gen_ai.token.type", "input"))...))
		g.tokens.Record(ctx, int64(resp.Usage.OutputTokens), metric.WithAttributes(append(ma, attribute.String("gen_ai.token.type", "output"))...))
		if resp.Usage.CacheReadTokens > 0 {
			g.cache.Add(ctx, int64(resp.Usage.CacheReadTokens), metric.WithAttributes(append(ma, attribute.String("goap.cache.op", "read"))...))
		}
		if resp.Usage.CacheWriteTokens > 0 {
			g.cache.Add(ctx, int64(resp.Usage.CacheWriteTokens), metric.WithAttributes(append(ma, attribute.String("goap.cache.op", "write"))...))
		}
	}
	g.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(metricAttrs...))
	return resp, err
}

// InstrumentEmbed wraps one embedding call on provider/model.
func (g *GenAI) InstrumentEmbed(ctx context.Context, provider, model string, texts int, call func(context.Context) (llm.EmbedResponse, error)) (llm.EmbedResponse, error) {
	caller, callerMetric := callerAttrs(ctx)
	attrs := append([]attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "embeddings"),
		attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", model),
		attribute.Int("goap.llm.texts", texts),
	}, caller...)
	ctx, span := g.tracer.Start(ctx, "embeddings "+model, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	defer span.End()
	metricAttrs := append([]attribute.KeyValue{attribute.String("gen_ai.operation.name", "embeddings"),
		attribute.String("gen_ai.provider.name", provider), attribute.String("gen_ai.request.model", model)}, callerMetric...)
	start := time.Now()
	resp, err := call(ctx)
	if err != nil {
		metricAttrs = fail(span, err, metricAttrs)
	} else {
		span.SetAttributes(attribute.Int("gen_ai.usage.input_tokens", resp.Tokens))
		g.tokens.Record(ctx, int64(resp.Tokens), metric.WithAttributes(append(slices.Clone(metricAttrs), attribute.String("gen_ai.token.type", "input"))...))
	}
	g.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(metricAttrs...))
	return resp, err
}

// messagesJSON renders messages in the shape of the GenAI semantic conventions ({role, parts: [{type: text, content}]}).
func messagesJSON(msgs []llm.Message) string {
	type part struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	type message struct {
		Role  string `json:"role"`
		Parts []part `json:"parts"`
	}
	out := make([]message, len(msgs))
	for i, m := range msgs {
		out[i] = message{Role: m.Role, Parts: []part{{Type: "text", Content: m.Content}}}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// capText cuts s at MaxContent bytes, on a rune boundary.
func capText(s string) string {
	if len(s) <= MaxContent {
		return s
	}
	cut := MaxContent
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…(truncated)"
}
