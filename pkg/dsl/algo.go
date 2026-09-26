package dsl

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"time"

	"github.com/dop251/goja"
	"github.com/zimwip/goap/pkg/algo"
)

// The algorithm usages of the DSL (ADR 0018) share the script engine of the
// actions but each one runs in its own context object, which only offers what
// its extension point needs:
//
// A JavaScript algorithm is the body of a function of ctx, so it may `return`.
//
//	property_validator   ctx: ValidatorCtx    func Run(ctx *dsl.ValidatorCtx) error
//	transition_guard     ctx: GuardCtx        func Run(ctx *dsl.GuardCtx) error
//	transition_action    ctx: TransitionCtx   func Run(ctx *dsl.TransitionCtx) error
//
// They are pure: no LLM, tool or sub-agent calls, no blackboard. A script
// rejects with ctx.fail(message), by throwing (JavaScript) / returning an error
// (Go), or, in JavaScript, by returning false or a string from run(ctx).

// AlgorithmTimeout bounds one execution of an algorithm.
const AlgorithmTimeout = 5 * time.Second

// ChangeInfo describes the change a transition is taken in.
type ChangeInfo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Intent      string `json:"intent"`
	Methodology string `json:"methodology"`
	Goal        string `json:"goal"`
}

// TransitionInfo describes the lifecycle transition being taken.
type TransitionInfo struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// AlgorithmInput is what an algorithm is run on. Only the fields of its usage are read.
type AlgorithmInput struct {
	// Node is the node concerned (after the change for a validator; the moved node, in its new state, for a transition).
	Node Node
	// Property and Value: the property a validator checks.
	Property string
	Value    any
	// Children: the nodes a document contains (transitions).
	Children   []Node
	Change     ChangeInfo
	Transition TransitionInfo
}

// Outcome is the result of an algorithm.
type Outcome struct {
	// Failures are the reasons the algorithm rejected; empty: accepted.
	Failures []string
	// Set and Unset are the property changes of a transition action.
	Set   map[string]any
	Unset []string
	Logs  []LogLine
}

// OK tells whether the algorithm accepted.
func (o Outcome) OK() bool { return len(o.Failures) == 0 }

// common is the part of every algorithm context.
type common struct {
	b        algo.Bound
	failures []string
	logs     []LogLine
}

func (c *common) logf(level, msg string) {
	c.logs = append(c.logs, LogLine{Time: time.Now().UTC(), Level: level, Message: msg})
}

func (c *common) Param(name string) any { return c.b.Params[name] }
func (c *common) Instance() string      { return c.b.Instance }
func (c *common) Algorithm() string     { return c.b.Algorithm }
func (c *common) Log(msg string)        { c.logf("info", msg) }
func (c *common) Warn(msg string)       { c.logf("warn", msg) }
func (c *common) Fail(msg string)       { c.failures = append(c.failures, msg) }

// ValidatorCtx is the context of a property validator.
type ValidatorCtx struct {
	c  *common
	in AlgorithmInput
}

// Param returns a parameter value of the instance.
func (v *ValidatorCtx) Param(name string) any { return v.c.Param(name) }

// Property is the name of the property being validated.
func (v *ValidatorCtx) Property() string { return v.in.Property }

// Value is the value of the property (nil when the node has none).
func (v *ValidatorCtx) Value() any { return v.in.Value }

// Node is the node being validated, as it will be once the change is applied.
func (v *ValidatorCtx) Node() Node { return v.in.Node }

// Fail rejects the value with a message.
func (v *ValidatorCtx) Fail(msg string)  { v.c.Fail(msg) }
func (v *ValidatorCtx) Log(msg string)   { v.c.Log(msg) }
func (v *ValidatorCtx) Warn(msg string)  { v.c.Warn(msg) }
func (v *ValidatorCtx) logf(l, m string) { v.c.logf(l, m) }

// GuardCtx is the context of a transition guard.
type GuardCtx struct {
	c  *common
	in AlgorithmInput
}

// Param returns a parameter value of the instance.
func (g *GuardCtx) Param(name string) any { return g.c.Param(name) }

// Node is the node taking the transition, in its new state.
func (g *GuardCtx) Node() Node { return g.in.Node }

// Children are the nodes a document contains, as they are in the result of the change.
func (g *GuardCtx) Children() []Node { return g.in.Children }

// Change is the change being applied.
func (g *GuardCtx) Change() ChangeInfo { return g.in.Change }

// Transition is the transition being taken.
func (g *GuardCtx) Transition() TransitionInfo { return g.in.Transition }

// Fail refuses the transition with a message.
func (g *GuardCtx) Fail(msg string)  { g.c.Fail(msg) }
func (g *GuardCtx) Log(msg string)   { g.c.Log(msg) }
func (g *GuardCtx) Warn(msg string)  { g.c.Warn(msg) }
func (g *GuardCtx) logf(l, m string) { g.c.logf(l, m) }

// TransitionCtx is the context of a transition action, run once the
// transition has been accepted.
type TransitionCtx struct {
	c   *common
	in  AlgorithmInput
	set map[string]any
	del []string
}

// Param returns a parameter value of the instance.
func (t *TransitionCtx) Param(name string) any { return t.c.Param(name) }

// Node is the node that moved, in its new state (with the changes of the actions run before).
func (t *TransitionCtx) Node() Node { return t.in.Node }

// Children are the nodes a document contains.
func (t *TransitionCtx) Children() []Node { return t.in.Children }

// Change is the change being applied.
func (t *TransitionCtx) Change() ChangeInfo { return t.in.Change }

// Transition is the transition that was taken.
func (t *TransitionCtx) Transition() TransitionInfo { return t.in.Transition }

// SetProp sets a property of the node in the version the transition produces.
func (t *TransitionCtx) SetProp(name string, value any) {
	if t.set == nil {
		t.set = map[string]any{}
	}
	t.set[name] = value
	t.del = removeString(t.del, name)
}

// RemoveProp removes a property of the node.
func (t *TransitionCtx) RemoveProp(name string) {
	delete(t.set, name)
	t.del = append(removeString(t.del, name), name)
}

// Fail aborts the transition (and the application of the change) with a message.
func (t *TransitionCtx) Fail(msg string)  { t.c.Fail(msg) }
func (t *TransitionCtx) Log(msg string)   { t.c.Log(msg) }
func (t *TransitionCtx) Warn(msg string)  { t.c.Warn(msg) }
func (t *TransitionCtx) logf(l, m string) { t.c.logf(l, m) }

func removeString(l []string, s string) []string {
	out := l[:0:0]
	for _, x := range l {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// RunAlgorithm runs a bound algorithm on an input. The error is a failure of
// the script itself (does not compile, throws, times out): callers treat it as
// a rejection too. A rejection by the algorithm is in Outcome.Failures.
func RunAlgorithm(ctx context.Context, b algo.Bound, in AlgorithmInput) (Outcome, error) {
	ctx, cancel := context.WithTimeout(ctx, AlgorithmTimeout)
	defer cancel()
	c := &common{b: b}
	var obj scriptLogger
	var jsObj any
	var bind func(entry any) (func() error, error)
	var out func() Outcome
	switch b.Type {
	case algo.UsagePropertyValidator:
		x := &ValidatorCtx{c: c, in: in}
		obj, jsObj = x, x
		bind = func(entry any) (func() error, error) {
			run, ok := entry.(func(*ValidatorCtx) error)
			if !ok {
				return nil, fmt.Errorf("go script: Run must have signature func(*dsl.ValidatorCtx) error, got %T", entry)
			}
			return func() error { return run(x) }, nil
		}
		out = func() Outcome { return Outcome{Failures: c.failures, Logs: c.logs} }
	case algo.UsageTransitionGuard:
		x := &GuardCtx{c: c, in: in}
		obj, jsObj = x, x
		bind = func(entry any) (func() error, error) {
			run, ok := entry.(func(*GuardCtx) error)
			if !ok {
				return nil, fmt.Errorf("go script: Run must have signature func(*dsl.GuardCtx) error, got %T", entry)
			}
			return func() error { return run(x) }, nil
		}
		out = func() Outcome { return Outcome{Failures: c.failures, Logs: c.logs} }
	case algo.UsageTransitionAction:
		x := &TransitionCtx{c: c, in: in}
		obj, jsObj = x, x
		bind = func(entry any) (func() error, error) {
			run, ok := entry.(func(*TransitionCtx) error)
			if !ok {
				return nil, fmt.Errorf("go script: Run must have signature func(*dsl.TransitionCtx) error, got %T", entry)
			}
			return func() error { return run(x) }, nil
		}
		out = func() Outcome { return Outcome{Failures: c.failures, Set: x.set, Unset: x.del, Logs: c.logs} }
	default:
		return Outcome{}, fmt.Errorf("algorithm %s: type %q cannot be run", b.Instance, b.Type)
	}
	// a JavaScript run(ctx) may reject by returning false or a message
	onReturn := func(v goja.Value) {
		if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
			return
		}
		switch x := v.Export().(type) {
		case bool:
			if !x {
				c.Fail("rejected")
			}
		case string:
			if x != "" {
				c.Fail(x)
			}
		}
	}
	err := runScript(ctx, b.Language, jsBody(b.Language, b.Code), jsObj, obj, onReturn, bind)
	res := out()
	if err != nil {
		return res, fmt.Errorf("algorithm %s: %w", b.Instance, err)
	}
	return res, nil
}

// jsBody makes the code of a JavaScript algorithm the body of run(ctx): it can
// use `return` (false or a message rejects). Go code is left as is.
func jsBody(language, code string) string {
	if language == algo.Go {
		return code
	}
	return "function run(ctx) {\n" + code + "\n}"
}

// CheckAlgorithmCode checks that the code of an algorithm compiles and, in Go,
// declares func Run. It does not run it.
func CheckAlgorithmCode(language, code string) error {
	switch language {
	case algo.JavaScript, "js":
		if _, err := goja.Compile("algorithm.js", jsBody(language, code), false); err != nil {
			return jsError(err)
		}
		return nil
	case algo.Go:
		f, err := parser.ParseFile(token.NewFileSet(), "algorithm.go", code, 0)
		if err != nil {
			return fmt.Errorf("go script: %w", err)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "Run" {
				return nil
			}
		}
		return fmt.Errorf("go script must declare func Run(ctx *dsl.<Usage>Ctx) error")
	}
	return fmt.Errorf("unsupported language %q", language)
}
