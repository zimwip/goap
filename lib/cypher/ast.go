// Package cypher reads the read-only subset of openCypher the graph answers (ADR 0160): a query is parsed by the parser
// generated from the openCypher grammar (Cypher.g4, `make cypher`, package parser), then turned into the small tree of
// this file, refusing what the subset leaves out (writes, WITH, UNWIND, CALL, UNION, parameters, comprehensions,
// subqueries other than EXISTS { pattern }). Expressions are evaluated by Eval over values and the entities a caller binds (nodes,
// relationships, paths); matching a pattern is the caller's (graph/graph's query, over a view), EXISTS included: Eval
// asks its scope (ExistsScope).
package cypher

import "slices"

// Query is a read query: one or more MATCH clauses, then RETURN.
type Query struct {
	Matches []Match
	Return  Return
}

// Match is a MATCH clause: its patterns and its WHERE (nil: none). An OPTIONAL MATCH keeps the rows it matches nothing
// for, its variables null.
type Match struct {
	Optional bool
	Patterns []Pattern
	Where    Expr
}

// Pattern is a path pattern: nodes joined by relationships (len(Rels) == len(Nodes)-1), named by Var when given.
type Pattern struct {
	Var   string
	Nodes []NodePattern
	Rels  []RelPattern
}

// NodePattern is `(v:Label {key: value})`.
type NodePattern struct {
	Var    string
	Labels []string
	Props  map[string]Expr
}

// Direction is the direction of a relationship pattern, from its left node to its right one.
type Direction int

const (
	// Either matches a relationship in either direction: `-[]-`.
	Either Direction = iota
	// Right matches `-[]->`.
	Right
	// Left matches `<-[]-`.
	Left
)

// RelPattern is `-[r:TYPE|OTHER*1..3 {key: value}]->`.
type RelPattern struct {
	Var   string
	Types []string
	Dir   Direction
	// Range is set for a variable length relationship (`*`, `*2`, `*1..3`): Max 0 is unbounded.
	Range *Range
	Props map[string]Expr
}

// Range is the number of hops of a variable length relationship.
type Range struct{ Min, Max int }

// Return is the RETURN clause.
type Return struct {
	Distinct bool
	// Star is `RETURN *` (followed by Items, if any).
	Star  bool
	Items []Item
	Order []SortItem
	Skip  Expr
	Limit Expr
}

// Item is a projection: an expression and its column name (the alias, else the text of the expression).
type Item struct {
	Expr Expr
	Name string
}

// SortItem is an ORDER BY key.
type SortItem struct {
	Expr Expr
	Desc bool
}

// Expr is an expression.
type Expr interface{ expr() }

type (
	// Lit is a literal: nil, bool, float64, string.
	Lit struct{ Value any }
	// Var is a variable.
	Var struct{ Name string }
	// Prop is `x.key`.
	Prop struct {
		X   Expr
		Key string
	}
	// Index is `x[i]`, Slice `x[from..to]` (nil bounds: open).
	Index struct{ X, I Expr }
	Slice struct{ X, From, To Expr }
	// Unary is NOT, - or + applied to X.
	Unary struct {
		Op string
		X  Expr
	}
	// Binary is a binary operator: OR XOR AND = <> < > <= >= + - * / % ^ IN, STARTS WITH, ENDS WITH, CONTAINS.
	Binary struct {
		Op   string
		L, R Expr
	}
	// IsNull is `x IS NULL`, or `x IS NOT NULL` when Not.
	IsNull struct {
		X   Expr
		Not bool
	}
	// HasLabels is `n:Label:Other`.
	HasLabels struct {
		X      Expr
		Labels []string
	}
	// List is `[a, b]`, Map `{k: v}`.
	List struct{ Items []Expr }
	Map  struct {
		Keys   []string
		Values []Expr
	}
	// Call is a function call; Star is `count(*)`.
	Call struct {
		Name     string
		Distinct bool
		Star     bool
		Args     []Expr
	}
	// Case is `CASE [subject] WHEN … THEN … [ELSE …] END`.
	Case struct {
		Subject Expr
		Whens   []Expr
		Thens   []Expr
		Else    Expr
	}
	// Exists is `EXISTS { pattern [WHERE …] }` or a pattern predicate (`(a)-[:T]->(:B)`): true when the patterns match,
	// the variables already bound being those of the row, the others local to it.
	Exists struct {
		Patterns []Pattern
		Where    Expr
	}
)

func (Lit) expr()       {}
func (Var) expr()       {}
func (Prop) expr()      {}
func (Index) expr()     {}
func (Slice) expr()     {}
func (Unary) expr()     {}
func (Binary) expr()    {}
func (IsNull) expr()    {}
func (HasLabels) expr() {}
func (List) expr()      {}
func (Map) expr()       {}
func (Call) expr()      {}
func (Case) expr()      {}
func (Exists) expr()    {}

// Vars are the variables an expression reads.
func Vars(e Expr) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(Expr)
	walk = func(e Expr) {
		switch x := e.(type) {
		case Var:
			if !seen[x.Name] {
				seen[x.Name] = true
				out = append(out, x.Name)
			}
		case Prop:
			walk(x.X)
		case Index:
			walk(x.X)
			walk(x.I)
		case Slice:
			walk(x.X)
			walk(x.From)
			walk(x.To)
		case Unary:
			walk(x.X)
		case Binary:
			walk(x.L)
			walk(x.R)
		case IsNull:
			walk(x.X)
		case HasLabels:
			walk(x.X)
		case List:
			for _, i := range x.Items {
				walk(i)
			}
		case Map:
			for _, v := range x.Values {
				walk(v)
			}
		case Call:
			for _, a := range x.Args {
				walk(a)
			}
		case Case:
			walk(x.Subject)
			for i := range x.Whens {
				walk(x.Whens[i])
				walk(x.Thens[i])
			}
			walk(x.Else)
		case Exists:
			for _, p := range x.Patterns {
				for _, v := range PatternVars(p) {
					walk(Var{Name: v})
				}
				for _, pe := range patternExprs(p) {
					walk(pe)
				}
			}
			walk(x.Where)
		}
	}
	walk(e)
	return out
}

// PatternVars are the variables a pattern names: its nodes, its relationships and the path.
func PatternVars(p Pattern) []string {
	var out []string
	add := func(v string) {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	for i, n := range p.Nodes {
		add(n.Var)
		if i < len(p.Rels) {
			add(p.Rels[i].Var)
		}
	}
	add(p.Var)
	return out
}

// patternExprs are the property values of a pattern.
func patternExprs(p Pattern) []Expr {
	var out []Expr
	for _, n := range p.Nodes {
		for _, e := range n.Props {
			out = append(out, e)
		}
	}
	for _, r := range p.Rels {
		for _, e := range r.Props {
			out = append(out, e)
		}
	}
	return out
}

// Declared are the variables the EXISTS of an expression name in their patterns: bound there when the row has not.
func Declared(e Expr) []string {
	var out []string
	Walk(e, func(x Expr) bool {
		if ex, ok := x.(Exists); ok {
			for _, p := range ex.Patterns {
				for _, v := range PatternVars(p) {
					if !slices.Contains(out, v) {
						out = append(out, v)
					}
				}
			}
		}
		return true
	})
	return out
}

// HasExists reports an expression holding an EXISTS anywhere.
func HasExists(e Expr) bool {
	found := false
	Walk(e, func(x Expr) bool {
		if _, ok := x.(Exists); ok {
			found = true
		}
		return !found
	})
	return found
}

// children are the expressions an expression holds (an EXISTS: its WHERE and its property values).
func children(e Expr) []Expr {
	switch x := e.(type) {
	case Prop:
		return []Expr{x.X}
	case Index:
		return []Expr{x.X, x.I}
	case Slice:
		return []Expr{x.X, x.From, x.To}
	case Unary:
		return []Expr{x.X}
	case Binary:
		return []Expr{x.L, x.R}
	case IsNull:
		return []Expr{x.X}
	case HasLabels:
		return []Expr{x.X}
	case List:
		return x.Items
	case Map:
		return x.Values
	case Call:
		return x.Args
	case Case:
		out := []Expr{x.Subject}
		for i := range x.Whens {
			out = append(out, x.Whens[i], x.Thens[i])
		}
		return append(out, x.Else)
	case Exists:
		var out []Expr
		for _, p := range x.Patterns {
			out = append(out, patternExprs(p)...)
		}
		return append(out, x.Where)
	}
	return nil
}

// Walk visits an expression and what it holds, depth first, while visit answers true.
func Walk(e Expr, visit func(Expr) bool) {
	if e == nil || !visit(e) {
		return
	}
	for _, c := range children(e) {
		Walk(c, visit)
	}
}

// Rewrite rebuilds an expression bottom up, each expression replaced by what f answers (itself to keep it); an EXISTS
// is replaced as a whole, what it holds left alone.
func Rewrite(e Expr, f func(Expr) Expr) Expr {
	if e == nil {
		return nil
	}
	rw := func(x Expr) Expr { return Rewrite(x, f) }
	list := func(xs []Expr) []Expr {
		if xs == nil {
			return nil
		}
		out := make([]Expr, len(xs))
		for i, x := range xs {
			out[i] = rw(x)
		}
		return out
	}
	switch x := e.(type) {
	case Prop:
		x.X = rw(x.X)
		e = x
	case Index:
		x.X, x.I = rw(x.X), rw(x.I)
		e = x
	case Slice:
		x.X, x.From, x.To = rw(x.X), rw(x.From), rw(x.To)
		e = x
	case Unary:
		x.X = rw(x.X)
		e = x
	case Binary:
		x.L, x.R = rw(x.L), rw(x.R)
		e = x
	case IsNull:
		x.X = rw(x.X)
		e = x
	case HasLabels:
		x.X = rw(x.X)
		e = x
	case List:
		x.Items = list(x.Items)
		e = x
	case Map:
		x.Values = list(x.Values)
		e = x
	case Call:
		x.Args = list(x.Args)
		e = x
	case Case:
		x.Subject, x.Whens, x.Thens, x.Else = rw(x.Subject), list(x.Whens), list(x.Thens), rw(x.Else)
		e = x
	}
	return f(e)
}

// Conjuncts splits an expression on its top-level ANDs (nil: none).
func Conjuncts(e Expr) []Expr {
	if e == nil {
		return nil
	}
	if b, ok := e.(Binary); ok && b.Op == "AND" {
		return append(Conjuncts(b.L), Conjuncts(b.R)...)
	}
	return []Expr{e}
}

// aggregates are the aggregating functions RETURN groups by.
var aggregates = map[string]bool{"count": true, "collect": true, "sum": true, "avg": true, "min": true, "max": true}

// IsAggregate reports an aggregating call (count, collect, sum, avg, min, max).
func IsAggregate(e Expr) bool {
	c, ok := e.(Call)
	return ok && aggregates[c.Name]
}

// HasAggregate reports an expression holding an aggregating call anywhere.
func HasAggregate(e Expr) bool {
	found := false
	var walk func(Expr)
	walk = func(e Expr) {
		if found || e == nil {
			return
		}
		if IsAggregate(e) {
			found = true
			return
		}
		switch x := e.(type) {
		case Prop:
			walk(x.X)
		case Index:
			walk(x.X)
			walk(x.I)
		case Slice:
			walk(x.X)
			walk(x.From)
			walk(x.To)
		case Unary:
			walk(x.X)
		case Binary:
			walk(x.L)
			walk(x.R)
		case IsNull:
			walk(x.X)
		case HasLabels:
			walk(x.X)
		case List:
			for _, i := range x.Items {
				walk(i)
			}
		case Map:
			for _, v := range x.Values {
				walk(v)
			}
		case Call:
			for _, a := range x.Args {
				walk(a)
			}
		case Case:
			walk(x.Subject)
			for i := range x.Whens {
				walk(x.Whens[i])
				walk(x.Thens[i])
			}
			walk(x.Else)
		}
	}
	walk(e)
	return found
}
