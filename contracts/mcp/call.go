package mcp

import "context"

// CallContext is what a tool call runs for: the unit holding the change, the change and the
// process. Built-in connectors read it (the change a goap-change tool edits by default); the engine
// sets Change and Process, the hub sets Unit.
type CallContext struct {
	Unit    string `json:"unit,omitempty"`
	Change  string `json:"change,omitempty"`
	Process string `json:"process,omitempty"`
}

type callKey struct{}

// WithCall returns ctx carrying the call context; empty fields keep the ones ctx already carries.
func WithCall(ctx context.Context, c CallContext) context.Context {
	cur := CallFrom(ctx)
	if c.Unit == "" {
		c.Unit = cur.Unit
	}
	if c.Change == "" {
		c.Change = cur.Change
	}
	if c.Process == "" {
		c.Process = cur.Process
	}
	return context.WithValue(ctx, callKey{}, c)
}

// CallFrom returns the call context of ctx.
func CallFrom(ctx context.Context) CallContext {
	c, _ := ctx.Value(callKey{}).(CallContext)
	return c
}
