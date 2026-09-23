package engine

import "context"

// Tracer instruments the engine (implemented with OpenTelemetry in
// internal/telemetry). The core engine does not depend on a tracing SDK.
type Tracer interface {
	// StartProcess starts the span of a Run call.
	StartProcess(ctx context.Context, p *Process) (context.Context, func(p *Process))
	// StartAction starts the span of an action execution.
	StartAction(ctx context.Context, p *Process, action, kind string) (context.Context, func(step *Step, err error))
	// StartTool starts the span of a tool call.
	StartTool(ctx context.Context, p *Process, action, tool string) (context.Context, func(err error))
	// TraceID returns the trace of ctx ("" when not traced).
	TraceID(ctx context.Context) string
}

type nopTracer struct{}

func (nopTracer) StartProcess(ctx context.Context, _ *Process) (context.Context, func(*Process)) {
	return ctx, func(*Process) {}
}
func (nopTracer) StartAction(ctx context.Context, _ *Process, _, _ string) (context.Context, func(*Step, error)) {
	return ctx, func(*Step, error) {}
}
func (nopTracer) StartTool(ctx context.Context, _ *Process, _, _ string) (context.Context, func(error)) {
	return ctx, func(error) {}
}
func (nopTracer) TraceID(context.Context) string { return "" }

func (e *Engine) tracer() Tracer {
	if e.Tracer == nil {
		return nopTracer{}
	}
	return e.Tracer
}
