package condition

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/parser"
)

// Implications between derived conditions (ADR 0112 §5): a condition B implies a condition A when B cannot hold
// without A holding, read from their expressions. Reaching A is then a prerequisite of B: `description_accepted`
// (a current rating of the description scoring 3 or more) needs `description_rated` (a current rating), which the
// rating action produces. The analysis is structural and conservative: an implication it finds holds, one it misses
// is only not shown.
//
// An expression is read as a conjunction of atoms (its `&&` terms). B implies A when every atom of A is implied by an
// atom of B: the same term, or, for `R.exists(v, P)`, a term `R.exists(w, Q)` over the same range whose predicate Q
// has every conjunct of P (the iteration variables renamed alike).

// atom is one conjunct of an expression: a term, or an exists over a range with the conjuncts of its predicate.
type atom struct {
	text   string
	exists bool
	preds  []string
}

// atomsOf reads an expression as its conjuncts (false when it cannot be parsed).
func atomsOf(expr string) ([]atom, bool) {
	p, err := parser.NewParser() // no macro: `exists` stays a call, its variable an argument
	if err != nil {
		return nil, false
	}
	parsed, errs := p.Parse(common.NewTextSource(expr))
	if errs != nil && len(errs.GetErrors()) > 0 {
		return nil, false
	}
	var out []atom
	for _, c := range conjuncts(parsed.Expr()) {
		out = append(out, atomOf(c))
	}
	return out, true
}

func atomOf(e celast.Expr) atom {
	if e.Kind() == celast.CallKind {
		c := e.AsCall()
		if c.IsMemberFunction() && c.FunctionName() == "exists" && len(c.Args()) == 2 && c.Args()[0].Kind() == celast.IdentKind {
			rename := map[string]string{c.Args()[0].AsIdent(): "@it"}
			a := atom{exists: true, text: canon(c.Target(), nil)}
			for _, p := range conjuncts(c.Args()[1]) {
				a.preds = append(a.preds, canon(p, rename))
			}
			slices.Sort(a.preds)
			return a
		}
	}
	return atom{text: canon(e, nil)}
}

// conjuncts flattens the `&&` terms of an expression.
func conjuncts(e celast.Expr) []celast.Expr {
	if e.Kind() == celast.CallKind && e.AsCall().FunctionName() == "_&&_" {
		var out []celast.Expr
		for _, a := range e.AsCall().Args() {
			out = append(out, conjuncts(a)...)
		}
		return out
	}
	return []celast.Expr{e}
}

// canon is a canonical text of an expression, the identifiers renamed (for comparison only).
func canon(e celast.Expr, rename map[string]string) string {
	switch e.Kind() {
	case celast.IdentKind:
		if r, ok := rename[e.AsIdent()]; ok {
			return r
		}
		return e.AsIdent()
	case celast.SelectKind:
		s := e.AsSelect()
		if s.IsTestOnly() {
			return "has(" + canon(s.Operand(), rename) + "." + s.FieldName() + ")"
		}
		return canon(s.Operand(), rename) + "." + s.FieldName()
	case celast.LiteralKind:
		return fmt.Sprintf("%#v", e.AsLiteral().Value())
	case celast.ListKind:
		var els []string
		for _, el := range e.AsList().Elements() {
			els = append(els, canon(el, rename))
		}
		return "[" + strings.Join(els, ",") + "]"
	case celast.CallKind:
		c := e.AsCall()
		var args []string
		for _, a := range c.Args() {
			args = append(args, canon(a, rename))
		}
		if c.FunctionName() == "_&&_" || c.FunctionName() == "_||_" || c.FunctionName() == "_==_" || c.FunctionName() == "_!=_" {
			slices.Sort(args) // commutative
		}
		head := c.FunctionName()
		if c.IsMemberFunction() {
			// a nested macro: its variable is renamed by position so that `x.exists(b, ...)` matches `x.exists(c, ...)`
			if len(c.Args()) == 2 && c.Args()[0].Kind() == celast.IdentKind {
				inner := map[string]string{}
				for k, v := range rename {
					inner[k] = v
				}
				inner[c.Args()[0].AsIdent()] = fmt.Sprintf("@%d", len(rename))
				args = []string{inner[c.Args()[0].AsIdent()], canon(c.Args()[1], inner)}
			}
			head = canon(c.Target(), rename) + "." + head
		}
		return head + "(" + strings.Join(args, ",") + ")"
	}
	return fmt.Sprintf("#%d", e.Kind())
}

// implies reports whether atom b cannot hold without atom a holding.
func (b atom) implies(a atom) bool {
	if a.exists != b.exists || a.text != b.text {
		return false
	}
	if !a.exists {
		return true
	}
	for _, p := range a.preds {
		if !slices.Contains(b.preds, p) {
			return false
		}
	}
	return true
}

// Implies reports whether an expression b cannot hold without an expression a holding (structurally).
func Implies(b, a string) bool {
	ab, ok := atomsOf(b)
	if !ok {
		return false
	}
	aa, ok := atomsOf(a)
	if !ok || len(aa) == 0 {
		return false
	}
	for _, x := range aa {
		if !slices.ContainsFunc(ab, func(y atom) bool { return y.implies(x) }) {
			return false
		}
	}
	return true
}

// implications computes, for every condition expression of the set, the other ones it implies.
func (s *Set) implications() map[string][]string {
	atoms := map[string][]atom{}
	for _, p := range s.progs {
		if as, ok := atomsOf(p.expr); ok && len(as) > 0 {
			atoms[p.name] = as
		}
	}
	out := map[string][]string{}
	for b, ab := range atoms {
		for a, aa := range atoms {
			if a == b {
				continue
			}
			all := true
			for _, x := range aa {
				if !slices.ContainsFunc(ab, func(y atom) bool { return y.implies(x) }) {
					all = false
					break
				}
			}
			if all {
				out[b] = append(out[b], a)
			}
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// Implied lists the conditions of the set that the condition cannot hold without (its prerequisites), sorted. Two
// conditions implying each other (the same expression) are not each other's prerequisite.
func (s *Set) Implied(name string) []string {
	s.implyOnce.Do(func() { s.implies = s.implications() })
	var out []string
	for _, a := range s.implies[name] {
		if !slices.Contains(s.implies[a], name) {
			out = append(out, a)
		}
	}
	return out
}
