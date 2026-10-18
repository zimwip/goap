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
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/ext"

	"github.com/zimwip/goap/contracts/domain"
)

// Definition is a named condition: an expression at the level of the change, an expression about one subject of a
// scope (Scope, Where: ADR 0109 §3), or an aggregate of a scoped condition at the level of the change (All, Any or
// None, with Where and Empty).
type Definition struct {
	Name string
	Expr string
	// Description says what the condition means (the libraries' own; a methodology describes its conditions itself).
	Description string
	// Scope is the kind of subject the condition is about (ScopeImpact, ...); empty or ScopeChange: the change.
	Scope string
	// Where (CEL, `self` the subject) narrows the subjects of a scoped condition or of an aggregate; a subject outside
	// it has no value.
	Where string
	// All, Any, None name the scoped condition an aggregate is over (one of them).
	All, Any, None string
	// Empty is the value of an `all` aggregate over no subject (default false: no vacuous truth).
	Empty bool
}

// Set is a compiled set of conditions.
type Set struct {
	env   *cel.Env
	progs []compiled
	// scoped are the scoped conditions in the order they are evaluated (a condition after those it reads);
	// aggregates the aggregates, each with the index of its scoped condition in scoped.
	scoped     []scopedCond
	aggregates []aggregate
	// descriptions are the descriptions of the conditions that have one (Description).
	descriptions map[string]string
	// implies maps a condition to the ones it implies (Implied), computed once.
	implyOnce sync.Once
	implies   map[string][]string
}

type compiled struct {
	name string
	expr string
	prog cel.Program
}

// Variables exposed to expressions.
var Variables = []string{"change", "items", "changeImpacts", "decisions", "artifacts", "vars",
	"options", "activeOption", "decisionPoints", "questions", "risks", "actions", "verifications", "derogations", "criticalityPolicy", "dependencies", "history", "assessments", "attestations"}

// NewEnv returns the CEL environment used for conditions.
func NewEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("change", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("items", cel.ListType(cel.DynType)),
		cel.Variable("changeImpacts", cel.ListType(cel.DynType)),
		cel.Variable("decisions", cel.ListType(cel.DynType)),
		cel.Variable("artifacts", cel.ListType(cel.DynType)),
		cel.Variable("vars", cel.MapType(cel.StringType, cel.DynType)),
		// options of the change and the one it works on (ADR 0032 §6); decision points and their questions (ADR 0009 §4)
		cel.Variable("options", cel.ListType(cel.DynType)),
		cel.Variable("activeOption", cel.StringType),
		cel.Variable("decisionPoints", cel.ListType(cel.DynType)),
		cel.Variable("questions", cel.ListType(cel.DynType)),
		// the risk register and the actions of the change (ADR 0036 §1)
		cel.Variable("risks", cel.ListType(cel.DynType)),
		cel.Variable("assessments", cel.ListType(cel.DynType)),
		cel.Variable("actions", cel.ListType(cel.DynType)),
		// the verification of the effects of the action runs (ADR 0075)
		cel.Variable("verifications", cel.ListType(cel.DynType)),
		// the derogations of the change (ADR 0075 §2)
		cel.Variable("derogations", cel.ListType(cel.DynType)),
		// what the organisation requires of the criticality of the change (ADR 0075 §3)
		cel.Variable("criticalityPolicy", cel.MapType(cel.StringType, cel.DynType)),
		// the changes the change depends on (ADR 0108)
		cel.Variable("dependencies", cel.ListType(cel.DynType)),
		// the history of the change: its runs and the runs of its steps, with their outcome (ADR 0109 §4)
		cel.Variable("history", cel.MapType(cel.StringType, cel.DynType)),
		// the attestations of the items of the checklists of the gates, by key (ADR 0132 §1)
		cel.Variable("attestations", cel.MapType(cel.StringType, cel.DynType)),
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
	var scoped, aggs []Definition
	for _, d := range defs {
		if d.Name == "" {
			return nil, fmt.Errorf("condition without name")
		}
		if seen[d.Name] {
			return nil, fmt.Errorf("duplicate condition %q", d.Name)
		}
		seen[d.Name] = true
		if d.Description != "" {
			if s.descriptions == nil {
				s.descriptions = map[string]string{}
			}
			s.descriptions[d.Name] = d.Description
		}
		switch {
		case d.IsAggregate():
			aggs = append(aggs, d)
			continue
		case d.IsScoped():
			scoped = append(scoped, d)
			continue
		}
		if d.Where != "" {
			return nil, fmt.Errorf("condition %q: where applies to a scoped condition or an aggregate", d.Name)
		}
		p, err := compileExpr(env, d.Expr)
		if err != nil {
			return nil, fmt.Errorf("condition %q: %w", d.Name, err)
		}
		s.progs = append(s.progs, compiled{name: d.Name, expr: d.Expr, prog: p})
	}
	if err := s.compileScoped(scoped, aggs); err != nil {
		return nil, err
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

// Names returns the names of the conditions of the level of the change (the world of the planner: expressions and
// aggregates), sorted.
func (s *Set) Names() []string {
	out := make([]string, 0, len(s.progs)+len(s.aggregates))
	for _, p := range s.progs {
		out = append(out, p.name)
	}
	for _, a := range s.aggregates {
		out = append(out, a.name)
	}
	sort.Strings(out)
	return out
}

// Exprs maps every condition of the set to its expression (an aggregate as "all: <condition>", a scoped condition
// prefixed by its scope).
func (s *Set) Exprs() map[string]string {
	out := make(map[string]string, len(s.progs)+len(s.scoped)+len(s.aggregates))
	for _, p := range s.progs {
		out[p.name] = p.expr
	}
	for _, c := range s.scoped {
		out[c.name] = c.text()
	}
	for _, a := range s.aggregates {
		out[a.name] = a.text()
	}
	return out
}

// Description says what a condition of the set means, as its definition does ("" when it says nothing): the
// methodology's own words, a library's, or those of the compiler for a condition it generates.
func (s *Set) Description(name string) string { return s.descriptions[name] }

// Has reports whether a condition is defined (of any level).
func (s *Set) Has(name string) bool {
	for _, p := range s.progs {
		if p.name == name {
			return true
		}
	}
	return s.ScopeOf(name) != "" || s.aggregateOf(name) != nil
}

// Result is the outcome of an evaluation.
type Result struct {
	// State is the value of every condition of the level of the change: the world a planner plans from.
	State map[string]bool
	// Errors holds the conditions that could not be evaluated (unknown).
	Errors map[string]string
	// Subjects are the values of the scoped conditions, per scope, subject by subject in the order of the context
	// (ADR 0109 §3); nil when the set has none.
	Subjects map[string][]Subject
}

// Evaluate evaluates every condition against the blackboard: the scoped conditions per subject first (their values
// are then visible as `conditions` on the entries of their scope), the conditions of the change, then the aggregates.
func (s *Set) Evaluate(bb domain.Blackboard) Result {
	act := Activation(bb)
	res := Result{State: map[string]bool{}, Errors: map[string]string{}}
	entries := s.evaluateScoped(act, &res)
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
	s.evaluateAggregates(act, entries, &res)
	return res
}
