package condition

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"

	"github.com/zimwip/goap/contracts/domain"
)

// Scopes of a condition (ADR 0109 §3): the kind of subject it is about. A scoped condition is evaluated once per
// subject (`self`), its values are visible as `conditions` on the entries of its scope, and an aggregate (all, any,
// none) brings them to the level of the change, the only level the planner reasons on.
const (
	ScopeChange = "change"
	// ScopeImpact: the impacts of the change (an entry of changeImpacts), identified by their node key.
	ScopeImpact = "impact"
	// ScopeItem: the items of the change (an entry of items), identified by their id.
	ScopeItem = "item"
	// ScopeRisk, ScopeAction: the keyed records of the risk register (ADR 0036), identified by their key.
	ScopeRisk   = "risk"
	ScopeAction = "action"
	// ScopeStep: the step runs of the history (an entry of history.steps), identified by "<path>#<run>".
	ScopeStep = "step"
)

// Scopes lists the scopes of a subject, in a stable order.
var Scopes = []string{ScopeImpact, ScopeItem, ScopeRisk, ScopeAction, ScopeStep}

// Aggregate operators.
const (
	AggregateAll  = "all"
	AggregateAny  = "any"
	AggregateNone = "none"
)

// IsScoped reports whether the definition is about one subject of a scope.
func (d Definition) IsScoped() bool { return d.Scope != "" && d.Scope != ScopeChange }

// IsAggregate reports whether the definition aggregates a scoped condition.
func (d Definition) IsAggregate() bool { return d.All != "" || d.Any != "" || d.None != "" }

// Aggregate returns the operator and the scoped condition of an aggregate ("" when it is none).
func (d Definition) Aggregate() (op, of string) {
	switch {
	case d.All != "":
		return AggregateAll, d.All
	case d.Any != "":
		return AggregateAny, d.Any
	case d.None != "":
		return AggregateNone, d.None
	}
	return "", ""
}

// Subject is a subject of a scope with the values of the scoped conditions about it (a condition whose where leaves
// the subject out has no value) and the conditions that could not be evaluated.
type Subject struct {
	ID     string            `json:"id"`
	Values map[string]bool   `json:"values"`
	Errors map[string]string `json:"errors,omitempty"`
}

// Holds reports whether every condition has the expected value on the subject (a missing value does not hold).
func (s Subject) Holds(want map[string]bool) bool {
	for k, v := range want {
		if got, ok := s.Values[k]; !ok || got != v {
			return false
		}
	}
	return true
}

type scopedCond struct {
	name, scope, expr, where string
	prog, whereProg          cel.Program
}

func (c scopedCond) text() string {
	out := "[" + c.scope + "] " + c.expr
	if c.where != "" {
		out += " (where " + c.where + ")"
	}
	return out
}

type aggregate struct {
	name, op, of, scope, where string
	empty                      bool
	whereProg                  cel.Program
}

func (a aggregate) text() string {
	out := a.op + ": " + a.of
	if a.where != "" {
		out += " (where " + a.where + ")"
	}
	if a.op == AggregateAll && a.empty {
		out += " (empty: true)"
	}
	return out
}

// ScopedEnv is the CEL environment of a scoped condition: the one of the conditions plus `self`, the subject.
func ScopedEnv() (*cel.Env, error) {
	env, err := NewEnv()
	if err != nil {
		return nil, err
	}
	return env.Extend(cel.Variable("self", cel.MapType(cel.StringType, cel.DynType)))
}

// readRE finds the scoped conditions an expression reads on its subject: self.conditions.x, self.conditions["x"].
var readRE = regexp.MustCompile(`self\s*\.\s*conditions\s*(?:\.\s*([A-Za-z_][A-Za-z0-9_]*)|\[\s*"([^"]+)"\s*\])`)

// SelfReads lists the scoped conditions an expression reads on its own subject.
func SelfReads(expr string) []string {
	var out []string
	for _, m := range readRE.FindAllStringSubmatch(expr, -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// CheckDefinition checks one definition on its own (its scope, its expressions), without the conditions it names:
// what a compiler reports per condition before the whole set is compiled.
func CheckDefinition(d Definition) error {
	switch {
	case d.IsAggregate():
		n := 0
		for _, x := range []string{d.All, d.Any, d.None} {
			if x != "" {
				n++
			}
		}
		if n > 1 {
			return fmt.Errorf("an aggregate is one of all, any or none")
		}
		if d.Expr != "" || d.IsScoped() {
			return fmt.Errorf("an aggregate has no expression nor scope: it takes the scope of the condition it aggregates")
		}
		if d.Empty && d.All == "" {
			return fmt.Errorf("empty applies to an all aggregate (any is false and none true on no subject)")
		}
		if d.Where != "" {
			return checkScopedExpr(d.Where)
		}
		return nil
	case d.IsScoped():
		if !slices.Contains(Scopes, d.Scope) {
			return fmt.Errorf("unknown scope %q (one of change, %s)", d.Scope, strings.Join(Scopes, ", "))
		}
		if d.Where != "" {
			if err := checkScopedExpr(d.Where); err != nil {
				return fmt.Errorf("where: %w", err)
			}
		}
		return checkScopedExpr(d.Expr)
	}
	if d.Where != "" {
		return fmt.Errorf("where applies to a scoped condition or an aggregate")
	}
	if d.Empty {
		return fmt.Errorf("empty applies to an all aggregate")
	}
	_, err := Compile([]Definition{{Name: "x", Expr: d.Expr}})
	if err != nil {
		return fmt.Errorf("%s", strings.TrimPrefix(err.Error(), `condition "x": `))
	}
	return nil
}

func checkScopedExpr(expr string) error {
	env, err := ScopedEnv()
	if err != nil {
		return err
	}
	_, err = compileExpr(env, expr)
	return err
}

// compileScoped compiles the scoped conditions (ordered so that a condition comes after those it reads on its
// subject, refusing a cycle) and the aggregates.
func (s *Set) compileScoped(scoped, aggs []Definition) error {
	if len(scoped) == 0 && len(aggs) == 0 {
		return nil
	}
	env, err := ScopedEnv()
	if err != nil {
		return err
	}
	byName := map[string]Definition{}
	for _, d := range scoped {
		if err := CheckDefinition(d); err != nil {
			return fmt.Errorf("condition %q: %w", d.Name, err)
		}
		byName[d.Name] = d
	}
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(name string, from []string) error
	visit = func(name string, from []string) error {
		d := byName[name]
		switch state[name] {
		case 1:
			return fmt.Errorf("condition %q: scoped conditions read each other in a cycle (%s)", name, strings.Join(append(from, name), " -> "))
		case 2:
			return nil
		}
		state[name] = 1
		for _, r := range append(SelfReads(d.Expr), SelfReads(d.Where)...) {
			o, ok := byName[r]
			switch {
			case !ok:
				return fmt.Errorf("condition %q reads %q on its subject: not a scoped condition", name, r)
			case o.Scope != d.Scope:
				return fmt.Errorf("condition %q (scope %s) reads %q of scope %s", name, d.Scope, r, o.Scope)
			}
			if err := visit(r, append(from, name)); err != nil {
				return err
			}
		}
		state[name] = 2
		c := scopedCond{name: d.Name, scope: d.Scope, expr: d.Expr, where: d.Where}
		if c.prog, err = compileExpr(env, d.Expr); err != nil {
			return fmt.Errorf("condition %q: %w", name, err)
		}
		if d.Where != "" {
			if c.whereProg, err = compileExpr(env, d.Where); err != nil {
				return fmt.Errorf("condition %q: where: %w", name, err)
			}
		}
		s.scoped = append(s.scoped, c)
		return nil
	}
	for _, d := range scoped {
		if err := visit(d.Name, nil); err != nil {
			return err
		}
	}
	for _, d := range aggs {
		if err := CheckDefinition(d); err != nil {
			return fmt.Errorf("condition %q: %w", d.Name, err)
		}
		op, of := d.Aggregate()
		o, ok := byName[of]
		if !ok {
			return fmt.Errorf("condition %q: %s %q: not a scoped condition", d.Name, op, of)
		}
		a := aggregate{name: d.Name, op: op, of: of, scope: o.Scope, where: d.Where, empty: d.Empty}
		if d.Where != "" {
			for _, r := range SelfReads(d.Where) {
				if x, ok := byName[r]; !ok || x.Scope != o.Scope {
					return fmt.Errorf("condition %q: where reads %q: not a scoped condition of scope %s", d.Name, r, o.Scope)
				}
			}
			if a.whereProg, err = compileExpr(env, d.Where); err != nil {
				return fmt.Errorf("condition %q: where: %w", d.Name, err)
			}
		}
		s.aggregates = append(s.aggregates, a)
	}
	return nil
}

// ScopeOf returns the scope of a scoped condition of the set ("" for a condition of the change or an unknown one).
func (s *Set) ScopeOf(name string) string {
	for _, c := range s.scoped {
		if c.name == name {
			return c.scope
		}
	}
	return ""
}

func (s *Set) aggregateOf(name string) *aggregate {
	for i := range s.aggregates {
		if s.aggregates[i].name == name {
			return &s.aggregates[i]
		}
	}
	return nil
}

// scopeEntries are the entries of the scopes the set evaluates, by scope.
type scopeEntries map[string][]scopeEntry

type scopeEntry struct {
	id    string
	entry map[string]any
}

// SubjectsOf returns the entries of a scope as the conditions see them (CEL maps), with their identity, from an
// activation (resolving a lazy variable).
func SubjectsOf(act map[string]any, scope string) []map[string]any {
	var list []any
	switch scope {
	case ScopeImpact:
		list, _ = Resolve(act["changeImpacts"]).([]any)
	case ScopeItem:
		list, _ = Resolve(act["items"]).([]any)
	case ScopeRisk:
		list, _ = Resolve(act["risks"]).([]any)
	case ScopeAction:
		list, _ = Resolve(act["actions"]).([]any)
	case ScopeStep:
		h, _ := Resolve(act["history"]).(map[string]any)
		list, _ = h["steps"].([]any)
	}
	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// SubjectID is the identity of an entry of a scope.
func SubjectID(scope string, entry map[string]any) string {
	str := func(k string) string { s, _ := entry[k].(string); return s }
	switch scope {
	case ScopeImpact:
		if k := str("key"); k != "" {
			return k
		}
		return str("id")
	case ScopeItem:
		return str("id")
	case ScopeStep:
		runs, _ := entry["runs"].(int64)
		return fmt.Sprintf("%s#%d", str("path"), runs)
	}
	return str("key")
}

// SubjectEntry returns the entry of a subject of a scope as the conditions see it (nil when the blackboard has none):
// what an action run for that subject gets as `vars.item`.
func SubjectEntry(bb domain.Blackboard, scope, id string) map[string]any {
	for _, e := range SubjectsOf(Activation(bb), scope) {
		if SubjectID(scope, e) == id {
			return e
		}
	}
	return nil
}

// evaluateScoped evaluates the scoped conditions on every subject of their scope, records the values in res and on
// the entries (`conditions`), and makes the lazy variables of those scopes plain so that the conditions of the change
// read the same entries.
func (s *Set) evaluateScoped(act map[string]any, res *Result) scopeEntries {
	if len(s.scoped) == 0 {
		return nil
	}
	out := scopeEntries{}
	for _, c := range s.scoped {
		if _, ok := out[c.scope]; ok {
			continue
		}
		var list []scopeEntry
		for _, e := range SubjectsOf(act, c.scope) {
			e["conditions"] = map[string]any{}
			list = append(list, scopeEntry{id: SubjectID(c.scope, e), entry: e})
		}
		out[c.scope] = list
		switch c.scope {
		case ScopeRisk:
			act["risks"] = Resolve(act["risks"])
		case ScopeAction:
			act["actions"] = Resolve(act["actions"])
		case ScopeStep:
			act["history"] = Resolve(act["history"])
		}
	}
	res.Subjects = map[string][]Subject{}
	for scope, list := range out {
		subjects := make([]Subject, 0, len(list))
		for _, se := range list {
			sub := Subject{ID: se.id, Values: map[string]bool{}}
			sact := maps.Clone(act)
			sact["self"] = se.entry
			conds := se.entry["conditions"].(map[string]any)
			for _, c := range s.scoped {
				if c.scope != scope {
					continue
				}
				if c.whereProg != nil {
					in, err := evalBool(c.whereProg, sact)
					if err != nil {
						setErr(&sub, c.name, "where: "+err.Error())
						continue
					}
					if !in {
						continue
					}
				}
				b, err := evalBool(c.prog, sact)
				if err != nil {
					setErr(&sub, c.name, err.Error())
					continue
				}
				sub.Values[c.name] = b
				conds[c.name] = b
			}
			subjects = append(subjects, sub)
		}
		res.Subjects[scope] = subjects
	}
	return out
}

func setErr(s *Subject, name, msg string) {
	if s.Errors == nil {
		s.Errors = map[string]string{}
	}
	s.Errors[name] = msg
}

func evalBool(p cel.Program, act map[string]any) (bool, error) {
	out, _, err := p.Eval(act)
	if err != nil {
		return false, err
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("non-bool result %v", out.Value())
	}
	return b, nil
}

// aggregated is the outcome of an aggregate: its value, the subjects it counted and those that do not hold (for
// all: false, for none: true, for any: none of them holds when it is false), and the errors that make it unknown.
type aggregated struct {
	value          bool
	counted        int
	failing, errs  []string
	unknown        bool
	unknownMessage string
}

func (s *Set) aggregate(a aggregate, act map[string]any, entries scopeEntries, res Result) aggregated {
	var out aggregated
	subjects := res.Subjects[a.scope]
	for i, sub := range subjects {
		if msg, bad := sub.Errors[a.of]; bad {
			out.errs = append(out.errs, sub.ID+": "+msg)
			continue
		}
		v, ok := sub.Values[a.of]
		if !ok {
			continue // outside the where of the condition
		}
		if a.whereProg != nil {
			sact := maps.Clone(act)
			sact["self"] = entries[a.scope][i].entry
			in, err := evalBool(a.whereProg, sact)
			if err != nil {
				out.errs = append(out.errs, sub.ID+": where: "+err.Error())
				continue
			}
			if !in {
				continue
			}
		}
		out.counted++
		switch a.op {
		case AggregateAll:
			if !v {
				out.failing = append(out.failing, sub.ID)
			}
		case AggregateAny:
			if v {
				out.value = true
			} else {
				out.failing = append(out.failing, sub.ID)
			}
		case AggregateNone:
			if v {
				out.failing = append(out.failing, sub.ID)
			}
		}
	}
	switch a.op {
	case AggregateAll:
		out.value = len(out.failing) == 0 && (out.counted > 0 || a.empty)
	case AggregateNone:
		out.value = len(out.failing) == 0
	}
	if len(out.errs) > 0 && (out.value || a.op == AggregateAny) {
		// a subject that could not be evaluated might change the value: the aggregate is unknown
		sort.Strings(out.errs)
		out.unknown, out.unknownMessage = true, strings.Join(out.errs, "; ")
	}
	if a.op == AggregateAny && out.value {
		out.unknown = false
	}
	return out
}

// evaluateAggregates brings the scoped conditions to the level of the change.
func (s *Set) evaluateAggregates(act map[string]any, entries scopeEntries, res *Result) {
	for _, a := range s.aggregates {
		r := s.aggregate(a, act, entries, *res)
		if r.unknown {
			res.Errors[a.name] = r.unknownMessage
			continue
		}
		res.State[a.name] = r.value
	}
}

// explainAggregate explains an aggregate: its value and the subjects that do not hold.
func (s *Set) explainAggregate(a aggregate, bb domain.Blackboard) Explanation {
	act := Activation(bb)
	res := Result{State: map[string]bool{}, Errors: map[string]string{}}
	entries := s.evaluateScoped(act, &res)
	r := s.aggregate(a, act, entries, res)
	ex := Explanation{Name: a.name, Expr: a.text(), Value: r.value, Inputs: map[string]string{}}
	if r.unknown {
		ex.Error = r.unknownMessage
	}
	ex.Inputs["subjects"] = fmt.Sprint(r.counted)
	if len(r.failing) > 0 {
		ex.Inputs["failing"] = text(r.failing)
	}
	ex.Root = Term{Text: a.text(), Value: fmt.Sprint(r.value)}
	for _, id := range r.failing {
		ex.Root.Terms = append(ex.Root.Terms, Term{Text: a.of + " on " + id, Value: fmt.Sprint(a.op == AggregateNone)})
	}
	return ex
}

// explainScoped explains a scoped condition: its value on each subject of its scope. Value is true when it holds on
// every subject it has a value for.
func (s *Set) explainScoped(name string, bb domain.Blackboard) Explanation {
	act := Activation(bb)
	res := Result{State: map[string]bool{}, Errors: map[string]string{}}
	s.evaluateScoped(act, &res)
	var c scopedCond
	for _, x := range s.scoped {
		if x.name == name {
			c = x
		}
	}
	ex := Explanation{Name: name, Expr: c.text(), Value: true, Inputs: map[string]string{}, Root: Term{Text: c.text()}}
	for _, sub := range res.Subjects[c.scope] {
		t := Term{Text: name + " on " + sub.ID}
		if msg, bad := sub.Errors[name]; bad {
			t.Error = msg
		} else if v, ok := sub.Values[name]; ok {
			t.Value = fmt.Sprint(v)
			ex.Value = ex.Value && v
		} else {
			t.Skipped = true
		}
		ex.Root.Terms = append(ex.Root.Terms, t)
	}
	return ex
}
