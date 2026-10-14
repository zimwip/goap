package condition

import (
	"fmt"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/parser"

	"github.com/zimwip/goap/pkg/domain"
)

// The facts of a condition (ADR 0112 §5): a condition is a CEL expression over the variables of the context, so what
// it really asks is known from it. Facts reads it as the conjunction of its `&&` terms, each evaluated against the
// blackboard; a term `R.exists(v, P1 && ... && Pn)` is read part by part, part i holding when an element of R meets
// P1 to Pi: "a rating of the description: yes; current: yes; score >= 3: none". The first part that does not hold is
// what the condition is missing, and an action whose effect asks for the same facts is what produces it.

// Fact is one term of a condition with whether it holds now.
type Fact struct {
	// Text is the term as written.
	Text  string
	Holds bool
	// Error says why the term could not be evaluated.
	Error string
	// Over is the list an exists term ranges over; Parts its predicate, part by part, each holding when an element
	// meets it and the parts before it.
	Over  string
	Parts []Fact
	// key is the canonical form of the term (of the parts up to this one, for a part), to match producers.
	key atom
}

// Facts reads a condition of the change level into its terms evaluated against the blackboard (false for an unknown
// condition, a scoped one or an aggregate, which keep their own explanation).
func (s *Set) Facts(name string, bb domain.Blackboard) ([]Fact, bool) {
	expr := ""
	for _, p := range s.progs {
		if p.name == name {
			expr = p.expr
		}
	}
	if expr == "" {
		return nil, false
	}
	p, err := parser.NewParser()
	if err != nil {
		return nil, false
	}
	parsed, errs := p.Parse(common.NewTextSource(expr))
	if errs != nil && len(errs.GetErrors()) > 0 {
		return nil, false
	}
	info := parsed.SourceInfo()
	act := Activation(bb)
	var out []Fact
	for _, c := range conjuncts(parsed.Expr()) {
		f := Fact{Text: unparse(c, info), key: atomOf(c)}
		if c.Kind() == celast.CallKind {
			call := c.AsCall()
			if call.IsMemberFunction() && call.FunctionName() == "exists" && len(call.Args()) == 2 && call.Args()[0].Kind() == celast.IdentKind {
				v := call.Args()[0].AsIdent()
				rng := unparse(call.Target(), info)
				f.Over = rng
				var sofar []string
				rename := map[string]string{v: "@it"}
				for _, part := range conjuncts(call.Args()[1]) {
					txt := unparse(part, info)
					sofar = append(sofar, "("+txt+")")
					pf := Fact{Text: txt, key: atom{exists: true, text: f.key.text, preds: prefixKeys(f.Parts, canon(part, rename))}}
					pf.Holds, pf.Error = s.holds(fmt.Sprintf("%s.exists(%s, %s)", rng, v, strings.Join(sofar, " && ")), act)
					f.Parts = append(f.Parts, pf)
				}
			}
		}
		f.Holds, f.Error = s.holds(f.Text, act)
		out = append(out, f)
	}
	return out, true
}

// prefixKeys are the canonical predicates of the parts before one, and its own.
func prefixKeys(before []Fact, own string) []string {
	var out []string
	if n := len(before); n > 0 {
		out = append(out, before[n-1].key.preds...)
	}
	return append(out, own)
}

func unparse(e celast.Expr, info *celast.SourceInfo) string {
	s, err := parser.Unparse(e, info)
	if err != nil {
		return fmt.Sprintf("#%d", e.ID())
	}
	return s
}

// holds evaluates one term against the activation.
func (s *Set) holds(expr string, act map[string]any) (bool, string) {
	ast, iss := s.env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return false, iss.Err().Error()
	}
	prog, err := s.env.Program(ast, cel.CostLimit(1_000_000))
	if err != nil {
		return false, err.Error()
	}
	out, _, err := prog.Eval(act)
	if err != nil {
		return false, err.Error()
	}
	b, ok := valueOf(out).(bool)
	if !ok {
		return false, fmt.Sprintf("non-bool result %v", valueOf(out))
	}
	return b, ""
}

// Missing is the first term of the facts that does not hold: the part of an exists term when one fails, else the term.
func Missing(facts []Fact) (Fact, bool) {
	for _, f := range facts {
		if f.Holds {
			continue
		}
		for _, p := range f.Parts {
			if !p.Holds {
				return p, true
			}
		}
		return f, true
	}
	return Fact{}, false
}

// Establishes reports whether making the condition effect hold makes the fact hold: one of its terms is the fact or,
// for a part of an exists term, an exists over the same list asking for every part up to it.
func (s *Set) Establishes(effect string, f Fact) bool {
	expr := ""
	for _, p := range s.progs {
		if p.name == effect {
			expr = p.expr
		}
	}
	as, ok := atomsOf(expr)
	if !ok || expr == "" {
		return false
	}
	for _, a := range as {
		if a.implies(f.key) {
			return true
		}
	}
	return false
}
