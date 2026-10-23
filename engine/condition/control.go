package condition

import (
	"fmt"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/ext"
)

// RuleFilter is the where of a control (ADR 0162): a CEL predicate telling the rules of the domain that govern an
// activity, with `self` the rule (key, type, state, props, project), `subject` the subject of the execution (key, type,
// state, props; an empty map when the execution has none) and `change` the change (id, title, intent, projectId...).
type RuleFilter struct {
	expr string
	prog cel.Program
}

// CompileRuleFilter compiles the where of a control; an empty expression keeps every rule.
func CompileRuleFilter(expr string) (*RuleFilter, error) {
	if expr == "" {
		return &RuleFilter{}, nil
	}
	env, err := cel.NewEnv(
		cel.Variable("self", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("subject", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("change", cel.MapType(cel.StringType, cel.DynType)),
		ext.Strings(), ext.Lists(),
	)
	if err != nil {
		return nil, err
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	if ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
		return nil, fmt.Errorf("must be a boolean, is %s", ast.OutputType())
	}
	prog, err := env.Program(ast)
	if err != nil {
		return nil, err
	}
	return &RuleFilter{expr: expr, prog: prog}, nil
}

// Keeps tells whether the rule governs the subject. A filter that fails on a rule (a property it reads is missing)
// keeps it out: a rule is in force only where it is known to apply.
func (f *RuleFilter) Keeps(self, subject, change map[string]any) bool {
	if f == nil || f.prog == nil {
		return true
	}
	if subject == nil {
		subject = map[string]any{}
	}
	if change == nil {
		change = map[string]any{}
	}
	out, _, err := f.prog.Eval(map[string]any{"self": self, "subject": subject, "change": change})
	if err != nil {
		return false
	}
	b, ok := out.Value().(bool)
	return ok && b
}

// String is the expression of the filter.
func (f *RuleFilter) String() string {
	if f == nil {
		return ""
	}
	return f.expr
}
