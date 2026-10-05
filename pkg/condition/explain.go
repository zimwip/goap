package condition

import (
	"encoding/json"
	"fmt"
	"strings"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/parser"

	"github.com/zimwip/goap/pkg/domain"
)

// maxValueText bounds the text of one observed value.
const maxValueText = 400

// Term is one sub-expression of a formula with the value it took against the blackboard.
type Term struct {
	// Text is the sub-expression as written.
	Text string
	// Value is what it evaluated to; empty when Skipped or Error says why not.
	Value string
	// Skipped: short-circuited, or inside a macro (exists, all, ...) whose iterations have no single value.
	Skipped bool
	Error   string
	Terms   []Term
}

// Explanation shows how a condition got its value: the formula, the value it took and the value of each part.
type Explanation struct {
	Name  string
	Expr  string
	Value bool
	// Error is set when the condition could not be evaluated (unknown).
	Error string
	Root  Term
	// Inputs are the blackboard variables the formula reads, with their value (as JSON text).
	Inputs map[string]string
}

// Explain evaluates the condition against the blackboard keeping the value of every sub-expression.
func (s *Set) Explain(name string, bb domain.Blackboard) (Explanation, bool) {
	var expr string
	found := false
	for _, p := range s.progs {
		if p.name == name {
			expr, found = p.expr, true
			break
		}
	}
	if !found {
		return Explanation{}, false
	}
	ex := Explanation{Name: name, Expr: expr, Inputs: map[string]string{}}
	ast, iss := s.env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		ex.Error = iss.Err().Error()
		return ex, true
	}
	prog, err := s.env.Program(ast, cel.CostLimit(1_000_000), cel.EvalOptions(cel.OptTrackState))
	if err != nil {
		ex.Error = err.Error()
		return ex, true
	}
	act := Activation(bb)
	out, det, err := prog.Eval(act)
	switch b, isBool := valueOf(out).(bool); {
	case err != nil:
		ex.Error = err.Error()
	case !isBool:
		ex.Error = fmt.Sprintf("non-bool result %v", valueOf(out))
	default:
		ex.Value = b
	}
	native := ast.NativeRep()
	ex.Root = term(native.Expr(), native.SourceInfo(), det)
	for _, v := range exprVariables(native.Expr()) {
		if val, ok := act[v]; ok {
			ex.Inputs[v] = text(val)
		}
	}
	return ex, true
}

func valueOf(v ref.Val) any {
	if v == nil {
		return nil
	}
	return v.Value()
}

func term(e celast.Expr, info *celast.SourceInfo, det *cel.EvalDetails) Term {
	src, err := parser.Unparse(e, info)
	if err != nil {
		src = fmt.Sprintf("#%d", e.ID())
	}
	t := Term{Text: src}
	if det == nil || det.State() == nil {
		t.Skipped = true
		return t
	}
	v, ok := det.State().Value(e.ID())
	switch {
	case !ok:
		t.Skipped = true
	default:
		t.Value = text(v.Value())
		if _, isErr := v.Value().(error); isErr {
			t.Error, t.Value = t.Value, ""
		}
	}
	if e.Kind() == celast.CallKind {
		c := e.AsCall()
		var kids []celast.Expr
		if c.IsMemberFunction() {
			kids = append(kids, c.Target())
		}
		kids = append(kids, c.Args()...)
		for _, k := range kids {
			if k.Kind() == celast.LiteralKind {
				continue
			}
			t.Terms = append(t.Terms, term(k, info, det))
		}
	}
	return t
}

// exprVariables lists the blackboard variables an expression reads, in the order of Variables.
func exprVariables(e celast.Expr) []string {
	seen := map[string]bool{}
	var walk func(celast.Expr)
	walk = func(e celast.Expr) {
		switch e.Kind() {
		case celast.IdentKind:
			seen[e.AsIdent()] = true
		case celast.SelectKind:
			walk(e.AsSelect().Operand())
		case celast.CallKind:
			c := e.AsCall()
			if c.IsMemberFunction() {
				walk(c.Target())
			}
			for _, a := range c.Args() {
				walk(a)
			}
		case celast.ListKind:
			for _, el := range e.AsList().Elements() {
				walk(el)
			}
		case celast.ComprehensionKind:
			c := e.AsComprehension()
			walk(c.IterRange())
			walk(c.AccuInit())
			walk(c.LoopCondition())
			walk(c.LoopStep())
			walk(c.Result())
		}
	}
	walk(e)
	var out []string
	for _, v := range Variables {
		if seen[v] {
			out = append(out, v)
		}
	}
	return out
}

// text renders an observed value, bounded.
func text(v any) string {
	var s string
	if err, ok := v.(error); ok {
		s = err.Error()
	} else if b, err := json.Marshal(v); err == nil {
		s = string(b)
	} else {
		s = fmt.Sprint(v)
	}
	if len(s) > maxValueText {
		s = strings.TrimSpace(s[:maxValueText]) + "…"
	}
	return s
}
