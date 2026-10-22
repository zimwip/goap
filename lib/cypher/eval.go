package cypher

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The values of an expression: nil (null), bool, float64 (every number), string, []any, map[string]any and the
// entities a caller binds: a node, a relationship (Entity) and a path (Path).

// Entity is a node or a relationship bound by a match.
type Entity interface {
	// Kind is "node" or "relationship".
	Kind() string
	// ID identifies it.
	ID() string
	// Labels are a node's types (its type and the types it extends), a relationship's type alone.
	Labels() []string
	// Property reads a property (the entity's own fields first: key, type, state…).
	Property(key string) (any, bool)
	// Properties are its properties, as keys() and properties() return them.
	Properties() map[string]any
}

// Path is a path a pattern bound: Nodes[i] and Nodes[i+1] joined by Rels[i].
type Path struct {
	Nodes []Entity
	Rels  []Entity
}

// Scope resolves the variables of an expression.
type Scope interface {
	Lookup(name string) (any, bool)
}

// ExistsScope is a Scope that matches patterns: Eval asks it for an EXISTS.
type ExistsScope interface {
	Scope
	Exists(x Exists) (bool, error)
}

// MapScope is a Scope over a map.
type MapScope map[string]any

// Lookup implements Scope.
func (m MapScope) Lookup(name string) (any, bool) { v, ok := m[name]; return v, ok }

// EvalError is an expression that cannot be computed (an unknown variable, an operator on values of the wrong type).
type EvalError struct{ Msg string }

func (e *EvalError) Error() string { return e.Msg }

func errorf(format string, a ...any) error { return &EvalError{Msg: fmt.Sprintf(format, a...)} }

// Truthy reads a value as a predicate: only true holds (null and false do not).
func Truthy(v any) bool { b, ok := v.(bool); return ok && b }

// Eval computes an expression in a scope. An aggregate is an error here: RETURN computes them (Aggregate).
func Eval(e Expr, s Scope) (any, error) {
	switch x := e.(type) {
	case nil:
		return nil, nil
	case Lit:
		return x.Value, nil
	case Exists:
		es, ok := s.(ExistsScope)
		if !ok {
			return nil, errorf("EXISTS and patterns are read in MATCH … WHERE and RETURN only")
		}
		return es.Exists(x)
	case Var:
		v, ok := s.Lookup(x.Name)
		if !ok {
			return nil, errorf("variable %s is not defined", x.Name)
		}
		return v, nil
	case Prop:
		v, err := Eval(x.X, s)
		if err != nil {
			return nil, err
		}
		return property(v, x.Key)
	case Index:
		v, err := Eval(x.X, s)
		if err != nil {
			return nil, err
		}
		i, err := Eval(x.I, s)
		if err != nil {
			return nil, err
		}
		return index(v, i)
	case Slice:
		return evalSlice(x, s)
	case Unary:
		v, err := Eval(x.X, s)
		if err != nil {
			return nil, err
		}
		switch x.Op {
		case "NOT":
			if v == nil {
				return nil, nil
			}
			b, ok := v.(bool)
			if !ok {
				return nil, errorf("NOT of %s", typeName(v))
			}
			return !b, nil
		case "-":
			if v == nil {
				return nil, nil
			}
			f, ok := v.(float64)
			if !ok {
				return nil, errorf("minus of %s", typeName(v))
			}
			return -f, nil
		}
		return v, nil
	case Binary:
		return evalBinary(x, s)
	case IsNull:
		v, err := Eval(x.X, s)
		if err != nil {
			return nil, err
		}
		return (v == nil) != x.Not, nil
	case HasLabels:
		v, err := Eval(x.X, s)
		if err != nil || v == nil {
			return nil, err
		}
		en, ok := v.(Entity)
		if !ok {
			return nil, errorf("labels of %s", typeName(v))
		}
		for _, l := range x.Labels {
			if !HasLabel(en, l) {
				return false, nil
			}
		}
		return true, nil
	case List:
		out := make([]any, len(x.Items))
		for i, it := range x.Items {
			v, err := Eval(it, s)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case Map:
		out := make(map[string]any, len(x.Keys))
		for i, k := range x.Keys {
			v, err := Eval(x.Values[i], s)
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	case Case:
		return evalCase(x, s)
	case Call:
		if aggregates[x.Name] {
			return nil, errorf("%s() is an aggregate: it belongs in RETURN", x.Name)
		}
		args := make([]any, len(x.Args))
		for i, a := range x.Args {
			v, err := Eval(a, s)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
		f := functions[x.Name]
		if len(args) < f.min || (f.max >= 0 && len(args) > f.max) {
			return nil, errorf("%s() takes %s", x.Name, f.arity())
		}
		return f.fn(args)
	}
	return nil, errorf("unknown expression %T", e)
}

// HasLabel reports an entity holding a label: as written, or, for a label naming no namespace, the label in any
// namespace (`Requirement` matches `alm@Requirement`).
func HasLabel(en Entity, label string) bool {
	for _, l := range en.Labels() {
		if LabelMatches(l, label) {
			return true
		}
	}
	return false
}

// LabelMatches reports a qualified type matching a label of a query: the same, or the same name when the label names
// no namespace.
func LabelMatches(typ, label string) bool {
	if typ == label {
		return true
	}
	if strings.Contains(label, "@") {
		return false
	}
	_, name, ok := strings.Cut(typ, "@")
	return ok && name == label
}

func property(v any, key string) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case Entity:
		p, _ := x.Property(key)
		return Normalize(p), nil
	case map[string]any:
		return Normalize(x[key]), nil
	}
	return nil, errorf("property %s of %s", key, typeName(v))
}

func index(v, i any) (any, error) {
	if v == nil || i == nil {
		return nil, nil
	}
	switch x := v.(type) {
	case []any:
		n, ok := i.(float64)
		if !ok {
			return nil, errorf("index of a list by %s", typeName(i))
		}
		k := int(n)
		if k < 0 {
			k += len(x)
		}
		if k < 0 || k >= len(x) {
			return nil, nil
		}
		return x[k], nil
	case map[string]any:
		k, ok := i.(string)
		if !ok {
			return nil, errorf("key of a map by %s", typeName(i))
		}
		return Normalize(x[k]), nil
	case Entity:
		k, ok := i.(string)
		if !ok {
			return nil, errorf("property by %s", typeName(i))
		}
		return property(x, k)
	}
	return nil, errorf("index of %s", typeName(v))
}

func evalSlice(x Slice, s Scope) (any, error) {
	v, err := Eval(x.X, s)
	if err != nil || v == nil {
		return nil, err
	}
	list, ok := v.([]any)
	if !ok {
		return nil, errorf("slice of %s", typeName(v))
	}
	bound := func(e Expr, def int) (int, error) {
		if e == nil {
			return def, nil
		}
		b, err := Eval(e, s)
		if err != nil {
			return 0, err
		}
		f, ok := b.(float64)
		if !ok {
			return 0, errorf("slice bound %s", typeName(b))
		}
		k := int(f)
		if k < 0 {
			k += len(list)
		}
		return max(0, min(k, len(list))), nil
	}
	from, err := bound(x.From, 0)
	if err != nil {
		return nil, err
	}
	to, err := bound(x.To, len(list))
	if err != nil {
		return nil, err
	}
	if from >= to {
		return []any{}, nil
	}
	return slices.Clone(list[from:to]), nil
}

func evalCase(x Case, s Scope) (any, error) {
	var subject any
	if x.Subject != nil {
		var err error
		if subject, err = Eval(x.Subject, s); err != nil {
			return nil, err
		}
	}
	for i, w := range x.Whens {
		v, err := Eval(w, s)
		if err != nil {
			return nil, err
		}
		hit := Truthy(v)
		if x.Subject != nil {
			eq, _ := equal(subject, v)
			hit = Truthy(eq)
		}
		if hit {
			return Eval(x.Thens[i], s)
		}
	}
	return Eval(x.Else, s)
}

func evalBinary(x Binary, s Scope) (any, error) {
	l, err := Eval(x.L, s)
	if err != nil {
		return nil, err
	}
	switch x.Op { // three-valued logic: a known side may decide alone
	case "AND":
		if b, ok := l.(bool); ok && !b {
			return false, nil
		}
	case "OR":
		if b, ok := l.(bool); ok && b {
			return true, nil
		}
	}
	r, err := Eval(x.R, s)
	if err != nil {
		return nil, err
	}
	switch x.Op {
	case "AND", "OR", "XOR":
		return logic(x.Op, l, r)
	case "=":
		return equal(l, r)
	case "<>":
		eq, err := equal(l, r)
		if eq == nil || err != nil {
			return nil, err
		}
		return !eq.(bool), nil
	case "<", ">", "<=", ">=":
		if l == nil || r == nil {
			return nil, nil
		}
		c, ok := Compare(l, r)
		if !ok {
			return nil, nil
		}
		switch x.Op {
		case "<":
			return c < 0, nil
		case ">":
			return c > 0, nil
		case "<=":
			return c <= 0, nil
		}
		return c >= 0, nil
	case "STARTS WITH", "ENDS WITH", "CONTAINS":
		ls, lok := l.(string)
		rs, rok := r.(string)
		if !lok || !rok {
			return nil, nil
		}
		switch x.Op {
		case "STARTS WITH":
			return strings.HasPrefix(ls, rs), nil
		case "ENDS WITH":
			return strings.HasSuffix(ls, rs), nil
		}
		return strings.Contains(ls, rs), nil
	case "IN":
		if r == nil {
			return nil, nil
		}
		list, ok := r.([]any)
		if !ok {
			return nil, errorf("IN %s: a list is expected", typeName(r))
		}
		var out any = false
		for _, it := range list {
			eq, _ := equal(l, it)
			if eq == true {
				return true, nil
			}
			if eq == nil {
				out = nil
			}
		}
		return out, nil
	case "+":
		if l == nil || r == nil {
			return nil, nil
		}
		switch a := l.(type) {
		case float64:
			if b, ok := r.(float64); ok {
				return a + b, nil
			}
		case string:
			return a + Text(r), nil
		case []any:
			if b, ok := r.([]any); ok {
				return append(slices.Clone(a), b...), nil
			}
			return append(slices.Clone(a), r), nil
		}
		if b, ok := r.(string); ok {
			return Text(l) + b, nil
		}
		return nil, errorf("%s + %s", typeName(l), typeName(r))
	case "-", "*", "/", "%", "^":
		if l == nil || r == nil {
			return nil, nil
		}
		a, aok := l.(float64)
		b, bok := r.(float64)
		if !aok || !bok {
			return nil, errorf("%s %s %s", typeName(l), x.Op, typeName(r))
		}
		switch x.Op {
		case "-":
			return a - b, nil
		case "*":
			return a * b, nil
		case "/":
			if b == 0 {
				return nil, errorf("division by zero")
			}
			return a / b, nil
		case "%":
			if b == 0 {
				return nil, errorf("modulo by zero")
			}
			return math.Mod(a, b), nil
		}
		return math.Pow(a, b), nil
	}
	return nil, errorf("unknown operator %s", x.Op)
}

func logic(op string, l, r any) (any, error) {
	lb, lok := l.(bool)
	rb, rok := r.(bool)
	if (l != nil && !lok) || (r != nil && !rok) {
		return nil, errorf("%s of %s and %s", op, typeName(l), typeName(r))
	}
	switch op {
	case "AND":
		if (lok && !lb) || (rok && !rb) {
			return false, nil
		}
		if !lok || !rok {
			return nil, nil
		}
		return true, nil
	case "OR":
		if (lok && lb) || (rok && rb) {
			return true, nil
		}
		if !lok || !rok {
			return nil, nil
		}
		return false, nil
	}
	if !lok || !rok {
		return nil, nil
	}
	return lb != rb, nil
}

// equal is `=`: null when a side is null.
func equal(l, r any) (any, error) {
	if l == nil || r == nil {
		return nil, nil
	}
	return Equal(l, r), nil
}

// Equal compares two values (an entity by identity).
func Equal(l, r any) bool {
	switch a := l.(type) {
	case Entity:
		b, ok := r.(Entity)
		return ok && a.Kind() == b.Kind() && a.ID() == b.ID()
	case []any:
		b, ok := r.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !Equal(a[i], b[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		b, ok := r.(map[string]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for k, v := range a {
			if !Equal(v, b[k]) {
				return false
			}
		}
		return true
	case Path:
		b, ok := r.(Path)
		return ok && Key(a) == Key(b)
	}
	return l == r
}

// Compare orders two values of one kind (numbers, strings, booleans); ok is false for values that do not compare.
func Compare(l, r any) (int, bool) {
	switch a := l.(type) {
	case float64:
		if b, ok := r.(float64); ok {
			switch {
			case a < b:
				return -1, true
			case a > b:
				return 1, true
			}
			return 0, true
		}
	case string:
		if b, ok := r.(string); ok {
			return strings.Compare(a, b), true
		}
	case bool:
		if b, ok := r.(bool); ok {
			switch {
			case a == b:
				return 0, true
			case !a:
				return -1, true
			}
			return 1, true
		}
	}
	return 0, false
}

// Order is the total order of ORDER BY: null last, then by kind (maps, entities, lists, strings, booleans, numbers),
// then within a kind.
func Order(l, r any) int {
	rank := func(v any) int {
		switch v.(type) {
		case map[string]any:
			return 0
		case Entity:
			return 1
		case Path:
			return 2
		case []any:
			return 3
		case string:
			return 4
		case bool:
			return 5
		case float64:
			return 6
		case nil:
			return 8
		}
		return 7
	}
	if a, b := rank(l), rank(r); a != b {
		return a - b
	}
	if c, ok := Compare(l, r); ok {
		return c
	}
	return strings.Compare(Key(l), Key(r))
}

// Key is a text identifying a value, for DISTINCT and grouping.
func Key(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case Entity:
		return x.Kind() + ":" + x.ID()
	case Path:
		var b strings.Builder
		b.WriteString("path:")
		for i, n := range x.Nodes {
			b.WriteString(n.ID())
			if i < len(x.Rels) {
				b.WriteString("|" + x.Rels[i].ID() + "|")
			}
		}
		return b.String()
	case []any:
		parts := make([]string, len(x))
		for i, it := range x {
			parts[i] = Key(it)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = strconv.Quote(k) + ":" + Key(x[k])
		}
		return "{" + strings.Join(parts, ",") + "}"
	case string:
		return strconv.Quote(x)
	}
	return fmt.Sprint(v)
}

// Text is a value as toString renders it.
func Text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return Key(v)
}

// Normalize brings a stored value to the values of an expression: every number a float64, nested lists and maps too.
func Normalize(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case float32:
		return float64(x)
	case []any:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = Normalize(it)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = it
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, it := range x {
			out[k] = Normalize(it)
		}
		return out
	}
	return v
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case float64:
		return "a number"
	case string:
		return "a string"
	case []any:
		return "a list"
	case map[string]any:
		return "a map"
	case Path:
		return "a path"
	case Entity:
		return "a " + v.(Entity).Kind()
	}
	return fmt.Sprintf("%T", v)
}

// Aggregate computes an aggregating call over the values its argument took in a group (count(*): one per row, the
// values unused).
func Aggregate(c Call, values []any) (any, error) {
	if c.Star {
		return float64(len(values)), nil
	}
	var kept []any
	seen := map[string]bool{}
	for _, v := range values {
		if v == nil {
			continue
		}
		if c.Distinct {
			k := Key(v)
			if seen[k] {
				continue
			}
			seen[k] = true
		}
		kept = append(kept, v)
	}
	switch c.Name {
	case "count":
		return float64(len(kept)), nil
	case "collect":
		if kept == nil {
			kept = []any{}
		}
		return kept, nil
	case "sum", "avg":
		sum := 0.0
		for _, v := range kept {
			f, ok := v.(float64)
			if !ok {
				return nil, errorf("%s() of %s", c.Name, typeName(v))
			}
			sum += f
		}
		if c.Name == "avg" {
			if len(kept) == 0 {
				return nil, nil
			}
			return sum / float64(len(kept)), nil
		}
		return sum, nil
	case "min", "max":
		var out any
		for _, v := range kept {
			if out == nil {
				out = v
				continue
			}
			cmp := Order(v, out)
			if (c.Name == "min" && cmp < 0) || (c.Name == "max" && cmp > 0) {
				out = v
			}
		}
		return out, nil
	}
	return nil, errorf("unknown aggregate %s", c.Name)
}

type function struct {
	min, max int // max -1: any number of arguments
	fn       func(args []any) (any, error)
}

func (f function) arity() string {
	switch {
	case f.max < 0:
		return fmt.Sprintf("at least %d argument(s)", f.min)
	case f.min == f.max:
		return fmt.Sprintf("%d argument(s)", f.min)
	}
	return fmt.Sprintf("%d to %d arguments", f.min, f.max)
}

// one is a function of one argument, null for null.
func one(fn func(v any) (any, error)) function {
	return function{min: 1, max: 1, fn: func(args []any) (any, error) {
		if args[0] == nil {
			return nil, nil
		}
		return fn(args[0])
	}}
}

func str(name string, fn func(string) any) function {
	return one(func(v any) (any, error) {
		s, ok := v.(string)
		if !ok {
			return nil, errorf("%s() of %s", name, typeName(v))
		}
		return fn(s), nil
	})
}

func entity(name string, fn func(Entity) any) function {
	return one(func(v any) (any, error) {
		e, ok := v.(Entity)
		if !ok {
			return nil, errorf("%s() of %s", name, typeName(v))
		}
		return fn(e), nil
	})
}

func pathOf(name string, fn func(Path) any) function {
	return one(func(v any) (any, error) {
		p, ok := v.(Path)
		if !ok {
			return nil, errorf("%s() of %s", name, typeName(v))
		}
		return fn(p), nil
	})
}

func entities(es []Entity) []any {
	out := make([]any, len(es))
	for i, e := range es {
		out[i] = e
	}
	return out
}

// functions are the scalar functions of the subset.
var functions map[string]function

func init() {
	functions = map[string]function{
		"id": entity("id", func(e Entity) any { return e.ID() }),
		"type": one(func(v any) (any, error) {
			e, ok := v.(Entity)
			if !ok || e.Kind() != "relationship" {
				return nil, errorf("type() of %s", typeName(v))
			}
			return e.Labels()[0], nil
		}),
		"labels": entity("labels", func(e Entity) any {
			out := []any{}
			for _, l := range e.Labels() {
				out = append(out, l)
			}
			return out
		}),
		"keys": entity("keys", func(e Entity) any {
			ks := []string{}
			for k := range e.Properties() {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			return Normalize(ks)
		}),
		"properties": entity("properties", func(e Entity) any { return Normalize(e.Properties()) }),
		"nodes":      pathOf("nodes", func(p Path) any { return entities(p.Nodes) }),
		"relationships": one(func(v any) (any, error) {
			switch p := v.(type) {
			case Path:
				return entities(p.Rels), nil
			case []any:
				return p, nil
			}
			return nil, errorf("relationships() of %s", typeName(v))
		}),
		"length": one(func(v any) (any, error) {
			switch x := v.(type) {
			case Path:
				return float64(len(x.Rels)), nil
			case []any:
				return float64(len(x)), nil
			case string:
				return float64(utf8Len(x)), nil
			}
			return nil, errorf("length() of %s", typeName(v))
		}),
		"size": one(func(v any) (any, error) {
			switch x := v.(type) {
			case []any:
				return float64(len(x)), nil
			case string:
				return float64(utf8Len(x)), nil
			case map[string]any:
				return float64(len(x)), nil
			}
			return nil, errorf("size() of %s", typeName(v))
		}),
		"head": one(func(v any) (any, error) {
			l, ok := v.([]any)
			if !ok {
				return nil, errorf("head() of %s", typeName(v))
			}
			if len(l) == 0 {
				return nil, nil
			}
			return l[0], nil
		}),
		"last": one(func(v any) (any, error) {
			l, ok := v.([]any)
			if !ok {
				return nil, errorf("last() of %s", typeName(v))
			}
			if len(l) == 0 {
				return nil, nil
			}
			return l[len(l)-1], nil
		}),
		"tolower":  str("toLower", func(s string) any { return strings.ToLower(s) }),
		"toupper":  str("toUpper", func(s string) any { return strings.ToUpper(s) }),
		"trim":     str("trim", func(s string) any { return strings.TrimSpace(s) }),
		"tostring": one(func(v any) (any, error) { return Text(v), nil }),
		"tointeger": one(func(v any) (any, error) {
			switch x := v.(type) {
			case float64:
				return math.Trunc(x), nil
			case string:
				f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
				if err != nil {
					return nil, nil
				}
				return math.Trunc(f), nil
			}
			return nil, nil
		}),
		"tofloat": one(func(v any) (any, error) {
			switch x := v.(type) {
			case float64:
				return x, nil
			case string:
				f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
				if err != nil {
					return nil, nil
				}
				return f, nil
			}
			return nil, nil
		}),
		"abs": one(func(v any) (any, error) {
			f, ok := v.(float64)
			if !ok {
				return nil, errorf("abs() of %s", typeName(v))
			}
			return math.Abs(f), nil
		}),
		"coalesce": {min: 1, max: -1, fn: func(args []any) (any, error) {
			for _, a := range args {
				if a != nil {
					return a, nil
				}
			}
			return nil, nil
		}},
	}
}

func utf8Len(s string) int { return len([]rune(s)) }
