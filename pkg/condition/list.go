package condition

import (
	"fmt"

	"cel.dev/cel-go/cel"
	"google.golang.org/protobuf/types/known/structpb"
	"reflect"

	"github.com/zimwip/goap/pkg/domain"
)

// List is a compiled CEL expression returning a list (the `foreach` of a step, ADR 0050): evaluated against the
// blackboard, it yields the items a step is carried out for, one stream each.
type List struct {
	prog cel.Program
}

func compileList(env *cel.Env, expr string) (cel.Program, error) {
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	if t := ast.OutputType(); t.Kind() != cel.ListKind && t != cel.DynType {
		return nil, fmt.Errorf("expression must return a list, got %s", t)
	}
	return env.Program(ast, cel.CostLimit(1_000_000))
}

// CheckList validates a list expression.
func CheckList(expr string) error {
	env, err := NewEnv()
	if err != nil {
		return err
	}
	_, err = compileList(env, expr)
	return err
}

// CompileList compiles a list expression.
func CompileList(expr string) (*List, error) {
	env, err := NewEnv()
	if err != nil {
		return nil, err
	}
	p, err := compileList(env, expr)
	if err != nil {
		return nil, err
	}
	return &List{prog: p}, nil
}

// Eval returns the elements of the list as plain values (maps, lists, strings, numbers, bools).
func (l *List) Eval(bb domain.Blackboard) ([]any, error) {
	out, _, err := l.prog.Eval(Activation(bb))
	if err != nil {
		return nil, err
	}
	v, err := out.ConvertToNative(reflect.TypeOf(&structpb.ListValue{}))
	if err != nil {
		return nil, fmt.Errorf("not a list: %w", err)
	}
	return v.(*structpb.ListValue).AsSlice(), nil
}

// Value is a compiled CEL expression of any type: the `groupBy` of a step (ADR 0050), evaluated per element, whose
// result is the key of the group the element belongs to.
type Value struct {
	prog cel.Program
}

// CompileValue compiles an expression of any type.
func CompileValue(expr string) (*Value, error) {
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
	return &Value{prog: p}, nil
}

// Eval returns the value as a plain value (map, list, string, number, bool or nil).
func (v *Value) Eval(bb domain.Blackboard) (any, error) {
	out, _, err := v.prog.Eval(Activation(bb))
	if err != nil {
		return nil, err
	}
	pb, err := out.ConvertToNative(reflect.TypeOf(&structpb.Value{}))
	if err != nil {
		return nil, err
	}
	return pb.(*structpb.Value).AsInterface(), nil
}
