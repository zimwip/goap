// Package condition compiles and evaluates the conditions of a methodology.
//
// A condition is a named CEL expression evaluated against the blackboard: the
// change items (axis change) hydrated with the domain nodes they reference
// (axis domain). The result of evaluating every condition is the world state
// the GOAP planner reasons on.
package condition

import (
	"fmt"
	"sort"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/ext"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/goap"
)

// Definition is a named condition expression.
type Definition struct {
	Name string
	Expr string
}

// Set is a compiled set of conditions.
type Set struct {
	env   *cel.Env
	progs []compiled
}

type compiled struct {
	name string
	expr string
	prog cel.Program
}

// Variables exposed to expressions.
var Variables = []string{"change", "items", "impacts", "proposals", "decisions", "artifacts", "merges", "vars"}

// NewEnv returns the CEL environment used for conditions.
func NewEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("change", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("items", cel.ListType(cel.DynType)),
		cel.Variable("impacts", cel.ListType(cel.DynType)),
		cel.Variable("proposals", cel.ListType(cel.DynType)),
		cel.Variable("decisions", cel.ListType(cel.DynType)),
		cel.Variable("artifacts", cel.ListType(cel.DynType)),
		cel.Variable("merges", cel.ListType(cel.DynType)),
		cel.Variable("vars", cel.MapType(cel.StringType, cel.DynType)),
		ext.Strings(),
		ext.Lists(),
		ext.Sets(),
	)
}

// Compile compiles definitions. Every expression must return a bool.
func Compile(defs []Definition) (*Set, error) {
	env, err := NewEnv()
	if err != nil {
		return nil, err
	}
	s := &Set{env: env}
	seen := map[string]bool{}
	for _, d := range defs {
		if d.Name == "" {
			return nil, fmt.Errorf("condition without name")
		}
		if seen[d.Name] {
			return nil, fmt.Errorf("duplicate condition %q", d.Name)
		}
		seen[d.Name] = true
		p, err := compileExpr(env, d.Expr)
		if err != nil {
			return nil, fmt.Errorf("condition %q: %w", d.Name, err)
		}
		s.progs = append(s.progs, compiled{name: d.Name, expr: d.Expr, prog: p})
	}
	return s, nil
}

func compileExpr(env *cel.Env, expr string) (cel.Program, error) {
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	if ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
		return nil, fmt.Errorf("expression must return bool, got %s", ast.OutputType())
	}
	return env.Program(ast, cel.CostLimit(1_000_000))
}

// Names returns the condition names, sorted.
func (s *Set) Names() []string {
	out := make([]string, 0, len(s.progs))
	for _, p := range s.progs {
		out = append(out, p.name)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a condition is defined.
func (s *Set) Has(name string) bool {
	for _, p := range s.progs {
		if p.name == name {
			return true
		}
	}
	return false
}

// Result is the outcome of an evaluation.
type Result struct {
	State goap.WorldState
	// Errors holds the conditions that could not be evaluated (unknown).
	Errors map[string]string
}

// Evaluate evaluates every condition against the blackboard.
func (s *Set) Evaluate(bb domain.Blackboard) Result {
	act := Activation(bb)
	res := Result{State: goap.WorldState{}, Errors: map[string]string{}}
	for _, p := range s.progs {
		out, _, err := p.prog.Eval(act)
		if err != nil {
			res.Errors[p.name] = err.Error()
			continue
		}
		b, ok := out.Value().(bool)
		if !ok {
			res.Errors[p.name] = fmt.Sprintf("non-bool result %v", out.Value())
			continue
		}
		res.State[p.name] = b
	}
	return res
}

// Eval evaluates a single ad-hoc expression against the blackboard.
func Eval(expr string, bb domain.Blackboard) (any, error) {
	env, err := NewEnv()
	if err != nil {
		return nil, err
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	p, err := env.Program(ast, cel.CostLimit(1_000_000))
	if err != nil {
		return nil, err
	}
	out, _, err := p.Eval(Activation(bb))
	if err != nil {
		return nil, err
	}
	return out.Value(), nil
}
