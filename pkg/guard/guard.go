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
// document contains (`children`, same shape) and the change (`change`).
type Guard struct {
	prog cel.Program
}

func guardEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("node", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("children", cel.ListType(cel.DynType)),
		cel.Variable("change", cel.MapType(cel.StringType, cel.DynType)),
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

// Check evaluates the guard. An evaluation error is an error, not a false.
func (g *Guard) Check(node map[string]any, children []any, change map[string]any) (bool, error) {
	if g == nil || g.prog == nil {
		return true, nil
	}
	if children == nil {
		children = []any{}
	}
	v, _, err := g.prog.Eval(map[string]any{"node": node, "children": children, "change": change})
	if err != nil {
		return false, err
	}
	b, ok := v.Value().(bool)
	if !ok {
		return false, fmt.Errorf("guard did not return a bool")
	}
	return b, nil
}
