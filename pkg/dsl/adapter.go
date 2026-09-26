package dsl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/zimwip/goap/pkg/algo"
)

// The adapter usage (ADR 0019) implements the tools of an MCP with the operations of a
// connector. The script is the body of function (ctx): ctx.tool() is the tool called,
// ctx.args() its arguments, ctx.param(name) a parameter of the organisation's instance
// (secrets are never readable), ctx.call(operation, args) calls an operation the connector
// exposes and returns its result (a failure throws), ctx.operations() lists them. The script
// returns the result of the tool, or rejects with ctx.fail(message) / by throwing.
//
//	adapter   ctx: AdapterCtx   func Run(ctx *dsl.AdapterCtx) error   (result with ctx.Return)

const (
	// AdapterTimeout bounds one execution of an adapter, connector calls included.
	AdapterTimeout = 30 * time.Second
	// MaxAdapterCalls bounds the connector calls of one execution.
	MaxAdapterCalls = 32
)

// AdapterInput is what an adapter is run on.
type AdapterInput struct {
	Tool string
	Args map[string]any
	// Operations the connector exposes; ctx.call refuses any other.
	Operations []string
	// Call runs a connector operation. The hub closes it over the configuration and the secrets.
	Call func(ctx context.Context, operation string, args map[string]any) (map[string]any, error)
}

// AdapterOutcome is the result of an adapter.
type AdapterOutcome struct {
	// Result is the result of the tool.
	Result map[string]any
	// Failures are the reasons the adapter rejected; empty: accepted.
	Failures []string
	Logs     []LogLine
	// Calls is the number of connector calls made.
	Calls int
}

// OK tells whether the adapter accepted.
func (o AdapterOutcome) OK() bool { return len(o.Failures) == 0 }

// AdapterCtx is the context of an adapter script.
type AdapterCtx struct {
	c      *common
	in     AdapterInput
	ctx    context.Context
	result map[string]any
	calls  int
}

// Param returns a parameter value of the instance (a secret is not readable).
func (a *AdapterCtx) Param(name string) any { return a.c.Param(name) }

// Tool is the name of the MCP tool being called.
func (a *AdapterCtx) Tool() string { return a.in.Tool }

// Args are the arguments of the tool call.
func (a *AdapterCtx) Args() map[string]any {
	if a.in.Args == nil {
		return map[string]any{}
	}
	return a.in.Args
}

// Operations lists the operations the connector exposes.
func (a *AdapterCtx) Operations() []string { return a.in.Operations }

// Call runs an operation of the connector and returns its result. It fails on an operation
// the connector does not expose, when the call limit is exceeded and when the connector fails.
func (a *AdapterCtx) Call(operation string, args map[string]any) (map[string]any, error) {
	known := false
	for _, o := range a.in.Operations {
		known = known || o == operation
	}
	if !known {
		return nil, fmt.Errorf("the connector exposes no operation %q (available: %s)", operation, strings.Join(a.in.Operations, ", "))
	}
	if a.calls >= MaxAdapterCalls {
		return nil, fmt.Errorf("more than %d connector calls in one execution", MaxAdapterCalls)
	}
	a.calls++
	if a.in.Call == nil {
		return nil, fmt.Errorf("no connector to call")
	}
	if args == nil {
		args = map[string]any{}
	}
	return a.in.Call(a.ctx, operation, args)
}

// Return sets the result of the tool (Go scripts; a JavaScript script returns it).
func (a *AdapterCtx) Return(result map[string]any) { a.result = result }

// Fail rejects the call with a message.
func (a *AdapterCtx) Fail(msg string)  { a.c.Fail(msg) }
func (a *AdapterCtx) Log(msg string)   { a.c.Log(msg) }
func (a *AdapterCtx) Warn(msg string)  { a.c.Warn(msg) }
func (a *AdapterCtx) logf(l, m string) { a.c.logf(l, m) }

// RunAdapter runs a bound adapter. The error is a failure of the script itself (does not
// compile, throws, times out, a connector call failed and was not caught); a rejection by
// the script is in Outcome.Failures. b.Params must hold the non-secret parameter values.
func RunAdapter(ctx context.Context, b algo.Bound, in AdapterInput) (AdapterOutcome, error) {
	if b.Type != algo.UsageAdapter {
		return AdapterOutcome{}, fmt.Errorf("algorithm %s: type %q is not an adapter", b.Instance, b.Type)
	}
	ctx, cancel := context.WithTimeout(ctx, AdapterTimeout)
	defer cancel()
	c := &common{b: b}
	x := &AdapterCtx{c: c, in: in, ctx: ctx}
	bind := func(entry any) (func() error, error) {
		run, ok := entry.(func(*AdapterCtx) error)
		if !ok {
			return nil, fmt.Errorf("go script: Run must have signature func(*dsl.AdapterCtx) error, got %T", entry)
		}
		return func() error { return run(x) }, nil
	}
	// a JavaScript adapter returns the result of the tool: an object, or any other value wrapped
	onReturn := func(v goja.Value) {
		if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
			return
		}
		switch r := v.Export().(type) {
		case map[string]any:
			x.result = r
		default:
			x.result = map[string]any{"value": r}
		}
	}
	err := runScript(ctx, b.Language, jsBody(b.Language, b.Code), x, x, onReturn, bind)
	out := AdapterOutcome{Result: x.result, Failures: c.failures, Logs: c.logs, Calls: x.calls}
	if err != nil {
		return out, fmt.Errorf("adapter %s: %w", b.Instance, err)
	}
	return out, nil
}
