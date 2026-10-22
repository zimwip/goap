// Package cypher reads the read-only subset of openCypher the graph answers (ADR 0160): a query is parsed by the parser
// generated from the openCypher grammar (Cypher.g4, `make cypher`, package parser), then turned into the small tree of
// this file, refusing what the subset leaves out (writes, WITH, UNWIND, CALL, UNION, OPTIONAL MATCH, parameters,
// comprehensions, subqueries). Expressions are evaluated by Eval over values and the entities a caller binds (nodes,
// relationships, paths); matching a pattern is the caller's (graph/graph's query, over a view).
package cypher

// Query is a read query: one or more MATCH clauses, then RETURN.
type Query struct {
	Matches []Match
	Return  Return
}

// Match is a MATCH clause: its patterns and its WHERE (nil: none).
type Match struct {
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
		}
	}
	walk(e)
	return out
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
