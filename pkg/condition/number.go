package condition

import (
	"fmt"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"

	"github.com/zimwip/goap/pkg/domain"
)

// Input is what expressions are evaluated against.
type Input = domain.Blackboard

// NumberSet is a compiled set of numeric expressions (action utilities).
type NumberSet struct {
	progs map[string]cel.Program
}

func compileNumber(env *cel.Env, expr string) (cel.Program, error) {
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	switch ast.OutputType() {
	case cel.DoubleType, cel.IntType, cel.UintType, cel.DynType:
	default:
		return nil, fmt.Errorf("expression must return a number, got %s", ast.OutputType())
	}
	return env.Program(ast, cel.CostLimit(1_000_000))
}

// CheckNumber validates a numeric expression.
func CheckNumber(expr string) error {
	env, err := NewEnv()
	if err != nil {
		return err
	}
	_, err = compileNumber(env, expr)
	return err
}

// CompileNumbers compiles numeric expressions by name.
func CompileNumbers(defs []Definition) (*NumberSet, error) {
	env, err := NewEnv()
	if err != nil {
		return nil, err
	}
	s := &NumberSet{progs: map[string]cel.Program{}}
	for _, d := range defs {
		p, err := compileNumber(env, d.Expr)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.Name, err)
		}
		s.progs[d.Name] = p
	}
	return s, nil
}

// Evaluate returns the value of every expression that evaluates to a number.
func (s *NumberSet) Evaluate(bb Input) map[string]float64 {
	out := map[string]float64{}
	if s == nil || len(s.progs) == 0 {
		return out
	}
	act := Activation(bb)
	for name, p := range s.progs {
		v, _, err := p.Eval(act)
		if err != nil {
			continue
		}
		switch x := v.(type) {
		case types.Double:
			out[name] = float64(x)
		case types.Int:
			out[name] = float64(x)
		case types.Uint:
			out[name] = float64(x)
		}
	}
	return out
}
