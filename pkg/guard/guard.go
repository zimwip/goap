// Package guard compiles and evaluates the guards of node lifecycle
// transitions (ADR 0014).
package guard

import (
	"fmt"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/ext"
)

// Guard is a compiled lifecycle transition guard: a boolean CEL expression
// over the node being moved (`node`: key, type, state, props), the nodes a
// document contains (`children`, same shape), the change (`change`) and the
// impact of the node in the change (`impact`: intent, review, reviews; ADR
// 0076: a transition that requires a review checks it there).
//
// The guard of a transition of the lifecycle of a change (ADR 0058) is the same language: it sees `change` and, as
// the conditions of a methodology do, the decision points of the change (`decisionPoints`) and the world state
// (`world`: condition name to bool); `node` and `children` are empty then.
type Guard struct {
	prog cel.Program
}

func guardEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("node", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("children", cel.ListType(cel.DynType)),
		cel.Variable("change", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("impact", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("decisionPoints", cel.ListType(cel.DynType)),
		cel.Variable("actions", cel.ListType(cel.DynType)),
		cel.Variable("risks", cel.ListType(cel.DynType)),
		cel.Variable("verifications", cel.ListType(cel.DynType)),
		cel.Variable("derogations", cel.ListType(cel.DynType)),
		cel.Variable("criticalityPolicy", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("world", cel.MapType(cel.StringType, cel.DynType)),
		ext.Strings(), ext.Lists(), ext.Sets())
}

// Compile compiles a transition guard (empty: always true).
func Compile(expr string) (*Guard, error) {
	if expr == "" {
		return &Guard{}, nil
	}
	env, err := guardEnv()
	if err != nil {
		return nil, err
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	if ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
		return nil, fmt.Errorf("guard must return bool, got %s", ast.OutputType())
	}
	p, err := env.Program(ast, cel.CostLimit(100_000))
	if err != nil {
		return nil, err
	}
	return &Guard{prog: p}, nil
}

// Check evaluates the guard of a node transition; impact is the impact of the node in the change (nil: none). An
// evaluation error is an error, not a false.
func (g *Guard) Check(node map[string]any, children []any, change, impact map[string]any) (bool, error) {
	return g.eval(node, children, change, Facts{Impact: impact}, nil)
}

// Facts are what the guard of a change transition reads of the blackboard besides the change.
type Facts struct {
	DecisionPoints, Actions, Risks []any
	// Verifications and Derogations are the verification states and the derogations of the change (ADR 0075),
	// CriticalityPolicy what the organisation requires of its criticality.
	Verifications, Derogations []any
	CriticalityPolicy          map[string]any
	// Impact is the impact of the moved node in the change (node transitions).
	Impact map[string]any
}

// CheckChange evaluates the guard of a transition of the lifecycle of a change (ADR 0058).
func (g *Guard) CheckChange(change map[string]any, f Facts, world map[string]bool) (bool, error) {
	w := make(map[string]any, len(world))
	for k, v := range world {
		w[k] = v
	}
	return g.eval(map[string]any{}, nil, change, f, w)
}

func (g *Guard) eval(node map[string]any, children []any, change map[string]any, f Facts, world map[string]any) (bool, error) {
	if g == nil || g.prog == nil {
		return true, nil
	}
	if children == nil {
		children = []any{}
	}
	list := func(l []any) []any {
		if l == nil {
			return []any{}
		}
		return l
	}
	if world == nil {
		world = map[string]any{}
	}
	policy := f.CriticalityPolicy
	if policy == nil {
		policy = map[string]any{}
	}
	impact := f.Impact
	if impact == nil {
		impact = map[string]any{}
	}
	v, _, err := g.prog.Eval(map[string]any{"node": node, "children": children, "change": change, "impact": impact, "decisionPoints": list(f.DecisionPoints), "actions": list(f.Actions), "risks": list(f.Risks),
		"verifications": list(f.Verifications), "derogations": list(f.Derogations), "criticalityPolicy": policy, "world": world})
	if err != nil {
		return false, err
	}
	b, ok := v.Value().(bool)
	if !ok {
		return false, fmt.Errorf("guard did not return a bool")
	}
	return b, nil
}
