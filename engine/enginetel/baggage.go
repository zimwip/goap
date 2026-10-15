package enginetel

import (
	"context"

	"go.opentelemetry.io/otel/baggage"

	"github.com/zimwip/goap/contracts/llm"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/lib/telemetry"
)

// WithCallerBaggage copies the engine caller attributes of ctx into the
// OpenTelemetry baggage (propagated to downstream services).
func WithCallerBaggage(ctx context.Context) context.Context {
	a, ok := engine.AttributesFrom(ctx)
	if !ok {
		return ctx
	}
	b := baggage.FromContext(ctx)
	for k, v := range map[string]string{telemetry.BaggageProcess: a.ProcessID, telemetry.BaggageMethodology: a.Methodology, telemetry.BaggageAgent: a.Agent, telemetry.BaggageAction: a.Action} {
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
