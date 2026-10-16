package condition

import (
	"fmt"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/ext"
)

// EventFilter is a compiled trigger filter: a boolean CEL expression over
// the variable `event` ({type, change {...}, process {...}, methodology {...}}).
type EventFilter struct {
	prog cel.Program
}

func eventEnv() (*cel.Env, error) {
	return cel.NewEnv(cel.Variable("event", cel.MapType(cel.StringType, cel.DynType)), ext.Strings(), ext.Lists())
}

// CompileEventFilter compiles a trigger filter (empty: always true).
func CompileEventFilter(expr string) (*EventFilter, error) {
	if expr == "" {
		return &EventFilter{}, nil
	}
	env, err := eventEnv()
	if err != nil {
		return nil, err
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	if ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
		return nil, fmt.Errorf("filter must return bool, got %s", ast.OutputType())
	}
	p, err := env.Program(ast, cel.CostLimit(100_000))
	if err != nil {
		return nil, err
	}
	return &EventFilter{prog: p}, nil
}

// Match evaluates the filter; evaluation errors do not match.
func (f *EventFilter) Match(event map[string]any) bool {
	if f == nil || f.prog == nil {
		return true
	}
	v, _, err := f.prog.Eval(map[string]any{"event": event})
	if err != nil {
		return false
	}
	b, ok := v.Value().(bool)
	return ok && b
}
