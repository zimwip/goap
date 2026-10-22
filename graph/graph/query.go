package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/cypher"
)

// A read query in the subset of openCypher (ADR 0160) over a view: MATCH patterns, WHERE, RETURN. A pattern starts from
// an anchor (a variable an earlier pattern bound, a key or an id, else the nodes of a label, read a page at a time) and
// grows a hop at a time through the neighbourhoods of the view, each reading the link types its relationship names
// (ViewNeighbourhoodOf). Nothing reads a namespace whole: the anchors, the hops and the rows are capped.

// Caps of a query, overridden by QueryOptions.
const (
	// QueryMaxRows is the most rows a match binds (before RETURN groups them).
	QueryMaxRows = 2000
	// QueryMaxReads is the most reads a query makes: the neighbourhoods of its hops and the pages of its anchors.
	QueryMaxReads = 2000
	// QueryMaxAnchors is the most nodes a label anchor reads.
	QueryMaxAnchors = 500
	// QueryMaxDepth is the most hops of a variable length relationship (`*`: 1 to QueryMaxDepth).
	QueryMaxDepth = 6
)

// QueryOptions bound a query and say what a caller may read.
type QueryOptions struct {
	// MaxRows, MaxReads, MaxAnchors: zero takes the default cap.
	MaxRows, MaxReads, MaxAnchors int
	// Visible tells whether the caller reads a node (nil: every node): a row naming or passing through a node it may
	// not read is dropped before RETURN. It is asked after the read, outside its transaction (an access check reads the
	// graph, and nothing in a graph transaction may wait on such a read).
	Visible func(domain.Node) bool
}

// QueryResult is what a query answered.
type QueryResult struct {
	Columns []string
	// Rows hold JSON values; a node, a relationship and a path are objects naming them ({"$node": id},
	// {"$link": id}, {"$path": {"nodes": [ids], "links": [ids]}}), the nodes and links themselves in Nodes and Links.
	Rows  [][]any
	Nodes []domain.Node
	Links []domain.Link
	// Truncated is set when a cap stopped the match: the rows are those found until then; Notes say which cap, and
	// what the query was read as (an unbounded `*`).
	Truncated bool
	Notes     []string
	// Reads is the number of reads the query made.
	Reads int
	// Baseline is the baseline the view read (a change view: its fork).
	Baseline domain.BaselineID
}

// QueryView runs a read query in a view. A query the subset does not read is ErrInvalid (with its position), so is a
// pattern with no anchor.
func (g *Graph) QueryView(ctx context.Context, v View, src string, opts QueryOptions) (QueryResult, error) {
	q, err := cypher.Parse(src)
	if err != nil {
		return QueryResult{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = QueryMaxRows
	}
	if opts.MaxReads <= 0 {
		opts.MaxReads = QueryMaxReads
	}
	if opts.MaxAnchors <= 0 {
		opts.MaxAnchors = QueryMaxAnchors
	}
	run := &queryRun{g: g, ctx: ctx, q: q, opts: opts, nodes: map[domain.NodeID]*qNode{}, hoods: map[string][]qHop{},
		links: map[domain.LinkID]domain.Link{}}
	if err := run.prepare(); err != nil {
		return QueryResult{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	var res QueryResult
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.openWorld(ctx, tx, v)
		if err != nil {
			return err
		}
		run.tx, run.w = tx, w
		res.Baseline = w.baseline
		return run.match()
	})
	if err != nil && !errors.Is(err, errQueryStop) {
		var ee *cypher.EvalError
		if errors.As(err, &ee) {
			return QueryResult{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		return QueryResult{}, err
	}
	run.hide()
	cols, rows, err := cypher.Project(q.Return, run.vars, run.rows)
	if err != nil {
		return QueryResult{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	res.Columns, res.Truncated, res.Notes, res.Reads = cols, run.truncated, run.notes, run.reads
	res.Rows = make([][]any, len(rows))
	for i, row := range rows {
		res.Rows[i] = make([]any, len(row))
		for j, v := range row {
			res.Rows[i][j] = run.export(v)
		}
	}
	res.Nodes, res.Links = run.exported()
	return res, nil
}

// errQueryStop ends a match early: a cap, or the rows a streaming RETURN keeps.
var errQueryStop = errors.New("query stopped")

type queryRun struct {
	g    *Graph
	ctx  context.Context
	tx   Tx
	w    *world
	q    *cypher.Query
	opts QueryOptions

	patterns []cypher.Pattern
	conds    []queryCond
	vars     []string
	stream   int // the rows to bind before stopping (-1: all)

	nodes map[domain.NodeID]*qNode // the nodes read, by id
	hoods map[string][]qHop        // the hops read, by node and link types
	links map[domain.LinkID]domain.Link
	reads int

	rows      []cypher.MapScope
	touched   [][]*qNode // the nodes each row names or passes through
	active    []*qTrail  // the trails of the patterns being matched
	truncated bool
	notes     []string

	outNodes map[domain.NodeID]bool
	outLinks map[domain.LinkID]bool
}

// queryCond is a conjunct of a WHERE with the variables it reads, checked as soon as they are bound.
type queryCond struct {
	expr cypher.Expr
	vars []string
}

// prepare flattens the patterns, splits the WHERE clauses and checks the variables they read.
func (r *queryRun) prepare() error {
	kinds := map[string]string{}
	declare := func(name, kind string) error {
		if name == "" {
			return nil
		}
		if k, ok := kinds[name]; ok {
			if k != kind || kind != "node" {
				return fmt.Errorf("variable %s is bound twice", name)
			}
			return nil
		}
		kinds[name] = kind
		r.vars = append(r.vars, name)
		return nil
	}
	for _, m := range r.q.Matches {
		for _, p := range m.Patterns {
			for i, n := range p.Nodes {
				if err := declare(n.Var, "node"); err != nil {
					return err
				}
				if i < len(p.Rels) {
					if err := declare(p.Rels[i].Var, "relationship"); err != nil {
						return err
					}
				}
			}
			if err := declare(p.Var, "path"); err != nil {
				return err
			}
			r.patterns = append(r.patterns, p)
		}
	}
	for _, m := range r.q.Matches {
		for _, c := range cypher.Conjuncts(m.Where) {
			vars := cypher.Vars(c)
			for _, v := range vars {
				if _, ok := kinds[v]; !ok {
					return fmt.Errorf("variable %s is not defined", v)
				}
			}
			r.conds = append(r.conds, queryCond{expr: c, vars: vars})
		}
	}
	aliases := map[string]bool{}
	for _, it := range r.q.Return.Items {
		for _, v := range cypher.Vars(it.Expr) {
			if _, ok := kinds[v]; !ok {
				return fmt.Errorf("variable %s is not defined", v)
			}
		}
		aliases[it.Name] = true
	}
	for _, s := range r.q.Return.Order {
		for _, v := range cypher.Vars(s.Expr) {
			if _, ok := kinds[v]; !ok && !aliases[v] {
				return fmt.Errorf("variable %s is not defined", v)
			}
		}
	}
	if _, _, err := cypher.Bounds(r.q.Return); err != nil {
		return err
	}
	r.stream = -1
	// rows dropped after the read (Visible) would leave fewer than the LIMIT: the match then goes on to its caps
	if n, ok := cypher.Streams(r.q.Return); ok && r.opts.Visible == nil {
		r.stream = n
	}
	return nil
}

func (r *queryRun) note(format string, a ...any) {
	n := fmt.Sprintf(format, a...)
	if !slices.Contains(r.notes, n) {
		r.notes = append(r.notes, n)
	}
}

// stop ends the match on a cap.
func (r *queryRun) stop(format string, a ...any) error {
	r.truncated = true
	r.note(format, a...)
	return errQueryStop
}

// read counts a read against the cap.
func (r *queryRun) read() error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if r.reads >= r.opts.MaxReads {
		return r.stop("stopped after %d reads: anchor the query more narrowly", r.opts.MaxReads)
	}
	r.reads++
	return nil
}

func (r *queryRun) match() error {
	return r.pattern(0, cypher.MapScope{}, map[domain.LinkID]bool{})
}

// pattern matches the patterns from i on, then binds a row.
func (r *queryRun) pattern(i int, b cypher.MapScope, used map[domain.LinkID]bool) error {
	if i == len(r.patterns) {
		for _, c := range r.conds { // every condition once more: the ones no binding checked (constants)
			ok, err := r.holds(c.expr, b)
			if err != nil || !ok {
				return err
			}
		}
		if len(r.rows) >= r.opts.MaxRows {
			return r.stop("stopped after %d rows", r.opts.MaxRows)
		}
		row := make(cypher.MapScope, len(b))
		for k, v := range b {
			row[k] = v
		}
		r.rows = append(r.rows, row)
		var nodes []*qNode
		for _, t := range r.active {
			nodes = append(nodes, t.nodes...)
			for _, m := range t.mids {
				nodes = append(nodes, m...)
			}
		}
		r.touched = append(r.touched, nodes)
		if r.stream >= 0 && len(r.rows) >= r.stream {
			return errQueryStop
		}
		return nil
	}
	p := r.patterns[i]
	at, cands, err := r.anchor(p, b)
	if err != nil {
		return err
	}
	tr := &qTrail{nodes: make([]*qNode, len(p.Nodes)), rels: make([][]domain.Link, len(p.Rels)), mids: make([][]*qNode, len(p.Rels))}
	r.active = append(r.active, tr)
	defer func() { r.active = r.active[:len(r.active)-1] }()
	for _, n := range cands {
		ok, undo, err := r.bindNode(p.Nodes[at], n, b)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		tr.nodes[at] = n
		err = r.right(p, at, tr, b, used, func() error {
			return r.left(p, at, tr, b, used, func() error {
				if p.Var == "" {
					return r.pattern(i+1, b, used)
				}
				ok, undo, err := r.bind(p.Var, tr.path(r), b)
				if err != nil || !ok {
					return err
				}
				defer undo()
				return r.pattern(i+1, b, used)
			})
		})
		undo()
		if err != nil {
			return err
		}
	}
	return nil
}

// hide drops the rows naming or passing through a node the caller may not read (QueryOptions.Visible), asked once per
// node, after the read.
func (r *queryRun) hide() {
	if r.opts.Visible == nil {
		return
	}
	seen := map[domain.NodeID]bool{}
	visible := func(n *qNode) bool {
		ok, known := seen[n.n.ID]
		if !known {
			ok = r.opts.Visible(n.n)
			seen[n.n.ID] = ok
		}
		return ok
	}
	kept := r.rows[:0]
	for i, row := range r.rows {
		ok := true
		for _, n := range r.touched[i] {
			if n != nil && !visible(n) {
				ok = false
				break
			}
		}
		if ok {
			kept = append(kept, row)
		}
	}
	r.rows = kept
}

// anchor picks the node pattern a pattern starts from and its candidate nodes: a variable already bound, else a key
// or an id, else the nodes of a label (narrowed by the text of a WHERE on it).
func (r *queryRun) anchor(p cypher.Pattern, b cypher.MapScope) (int, []*qNode, error) {
	for i, n := range p.Nodes {
		if v, ok := b[n.Var]; ok && n.Var != "" {
			return i, []*qNode{v.(*qNode)}, nil
		}
	}
	for i, n := range p.Nodes {
		if key, ok := r.literal(n, "key"); ok {
			node, err := r.byKey(key)
			if err != nil {
				return 0, nil, err
			}
			return i, node, nil
		}
		if id, ok := r.literal(n, "id"); ok {
			node, err := r.byID(domain.NodeID(id))
			if err != nil {
				return 0, nil, err
			}
			return i, node, nil
		}
	}
	for i, n := range p.Nodes {
		if len(n.Labels) > 0 {
			nodes, err := r.ofLabel(n)
			return i, nodes, err
		}
	}
	return 0, nil, fmt.Errorf("%w: a pattern starts from a node with a label, a key or an id, or a variable an earlier pattern bound: (n:Requirement), (n {key: 'R-1'})", ErrInvalid)
}

// literal is a string a node pattern sets a field to: inline (`{key: 'x'}`) or by an equality of the WHERE
// (`n.key = 'x'`).
func (r *queryRun) literal(n cypher.NodePattern, field string) (string, bool) {
	if e, ok := n.Props[field]; ok {
		if l, ok := e.(cypher.Lit); ok {
			s, ok := l.Value.(string)
			return s, ok
		}
	}
	if n.Var == "" {
		return "", false
	}
	for _, c := range r.conds {
		bin, ok := c.expr.(cypher.Binary)
		if !ok || bin.Op != "=" {
			continue
		}
		for _, side := range [][2]cypher.Expr{{bin.L, bin.R}, {bin.R, bin.L}} {
			pr, ok := side[0].(cypher.Prop)
			lit, lok := side[1].(cypher.Lit)
			if !ok || !lok || pr.Key != field {
				continue
			}
			if v, ok := pr.X.(cypher.Var); ok && v.Name == n.Var {
				if s, ok := lit.Value.(string); ok {
					return s, true
				}
			}
		}
	}
	return "", false
}

// text is the longest string a WHERE asks a property of a node pattern to equal, contain, start or end with: the text
// filter of its label's pages (a superset of what the condition keeps, which is checked once bound).
func (r *queryRun) text(n cypher.NodePattern) string {
	best := ""
	consider := func(s string) {
		if len(s) > len(best) {
			best = s
		}
	}
	for k, e := range n.Props {
		if l, ok := e.(cypher.Lit); ok && k != "type" {
			if s, ok := l.Value.(string); ok {
				consider(s)
			}
		}
	}
	if n.Var == "" {
		return best
	}
	for _, c := range r.conds {
		bin, ok := c.expr.(cypher.Binary)
		if !ok {
			continue
		}
		switch bin.Op {
		case "=", "CONTAINS", "STARTS WITH", "ENDS WITH":
		default:
			continue
		}
		pr, ok := bin.L.(cypher.Prop)
		lit, lok := bin.R.(cypher.Lit)
		if !ok || !lok {
			if bin.Op != "=" {
				continue
			}
			if pr, ok = bin.R.(cypher.Prop); !ok {
				continue
			}
			if lit, ok = bin.L.(cypher.Lit); !ok {
				continue
			}
		}
		if v, ok := pr.X.(cypher.Var); ok && v.Name == n.Var && pr.Key != "type" {
			if s, ok := lit.Value.(string); ok {
				consider(s)
			}
		}
	}
	return best
}

func (r *queryRun) byKey(key string) ([]*qNode, error) {
	if err := r.read(); err != nil {
		return nil, err
	}
	ref, err := r.w.ref(r.ctx, r.tx, "", key)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r.nodeAt(ref)
}

func (r *queryRun) byID(id domain.NodeID) ([]*qNode, error) {
	if err := r.read(); err != nil {
		return nil, err
	}
	at, err := r.w.at(r.ctx, r.tx, []domain.NodeID{id})
	if err != nil {
		return nil, err
	}
	v, ok := at[id]
	if !ok {
		return nil, nil
	}
	return r.nodeAt(domain.NodeRef{ID: id, Version: v})
}

func (r *queryRun) nodeAt(ref domain.NodeRef) ([]*qNode, error) {
	if n, ok := r.nodes[ref.ID]; ok {
		return []*qNode{n}, nil
	}
	n, err := r.w.node(r.ctx, r.tx, ref)
	if err != nil {
		return nil, err
	}
	return []*qNode{r.keep(n)}, nil
}

func (r *queryRun) keep(n domain.Node) *qNode {
	if q, ok := r.nodes[n.ID]; ok {
		return q
	}
	q := &qNode{n: n}
	r.nodes[n.ID] = q
	return q
}

// ofLabel reads the nodes of the first label of a node pattern, a page of at most MaxAnchors: the type as written, and
// a label naming no namespace in the namespace of the view.
func (r *queryRun) ofLabel(n cypher.NodePattern) ([]*qNode, error) {
	label := n.Labels[0]
	types := []string{label}
	if !strings.Contains(label, "@") {
		types = append(types, r.w.namespace+"@"+label)
	}
	text := r.text(n)
	var out []*qNode
	for _, typ := range types {
		if err := r.read(); err != nil {
			return nil, err
		}
		room := r.opts.MaxAnchors - len(out)
		page, err := r.w.page(r.ctx, r.tx, NodeQuery{Type: typ, Text: text, Limit: room + 1})
		if err != nil {
			return nil, err
		}
		for _, node := range page.Nodes {
			if node.Type != typ {
				continue
			}
			if len(out) == r.opts.MaxAnchors {
				r.truncated = true
				r.note("only the first %d nodes of %s were read (of %d): narrow the anchor with a key or a WHERE on a property", r.opts.MaxAnchors, label, page.Total)
				break
			}
			out = append(out, r.keep(node))
		}
	}
	return out, nil
}

// bindNode binds a node to a node pattern when it matches: its labels, its properties, the variable (the same node
// when bound already) and the conditions it completes; undo takes the binding back.
func (r *queryRun) bindNode(p cypher.NodePattern, n *qNode, b cypher.MapScope) (bool, func(), error) {
	for _, l := range p.Labels {
		if !r.isLabel(n.n.Type, l) {
			return false, nop, nil
		}
	}
	if ok, err := r.propsMatch(p.Props, n, b); err != nil || !ok {
		return false, nop, err
	}
	if p.Var == "" {
		return true, nop, nil
	}
	return r.bind(p.Var, n, b)
}

func nop() {}

// isLabel reports a node type matching a label of a query: the type or a type it extends, the label naming its
// namespace or not.
func (r *queryRun) isLabel(typ, label string) bool {
	if cypher.LabelMatches(typ, label) {
		return true
	}
	if !strings.Contains(label, "@") {
		label = domain.NamespaceOf(typ) + "@" + label
	}
	return r.g.isA(typ, label)
}

func (r *queryRun) propsMatch(props map[string]cypher.Expr, en cypher.Entity, b cypher.MapScope) (bool, error) {
	for k, e := range props {
		want, err := cypher.Eval(e, b)
		if err != nil {
			return false, err
		}
		got, _ := en.Property(k)
		if want == nil || !cypher.Equal(cypher.Normalize(got), want) {
			return false, nil
		}
	}
	return true, nil
}

// bind binds a variable (an equal value when bound already) and checks the conditions it completes.
func (r *queryRun) bind(name string, v any, b cypher.MapScope) (bool, func(), error) {
	if old, ok := b[name]; ok {
		return cypher.Equal(old, v), nop, nil
	}
	b[name] = v
	undo := func() { delete(b, name) }
	for _, c := range r.conds {
		if !slices.Contains(c.vars, name) || !bound(c.vars, b) {
			continue
		}
		ok, err := r.holds(c.expr, b)
		if err != nil || !ok {
			undo()
			return false, nop, err
		}
	}
	return true, undo, nil
}

func bound(vars []string, b cypher.MapScope) bool {
	for _, v := range vars {
		if _, ok := b[v]; !ok {
			return false
		}
	}
	return true
}

func (r *queryRun) holds(e cypher.Expr, b cypher.MapScope) (bool, error) {
	v, err := cypher.Eval(e, b)
	return cypher.Truthy(v), err
}

// qTrail is what a pattern bound so far: its nodes, the links of each relationship (several for a variable length
// one, from its left node to its right one) and the nodes between them.
type qTrail struct {
	nodes []*qNode
	rels  [][]domain.Link
	mids  [][]*qNode
}

func (t *qTrail) path(r *queryRun) cypher.Path {
	var p cypher.Path
	for i, n := range t.nodes {
		p.Nodes = append(p.Nodes, n)
		if i < len(t.rels) {
			for j, l := range t.rels[i] {
				p.Rels = append(p.Rels, qLink{l})
				if j < len(t.mids[i]) {
					p.Nodes = append(p.Nodes, t.mids[i][j])
				}
			}
		}
	}
	return p
}

// right extends a pattern from its node k to its last one.
func (r *queryRun) right(p cypher.Pattern, k int, tr *qTrail, b cypher.MapScope, used map[domain.LinkID]bool, cont func() error) error {
	if k == len(p.Nodes)-1 {
		return cont()
	}
	return r.step(tr.nodes[k], p.Rels[k], p.Rels[k].Dir, p.Nodes[k+1], b, used, func(n *qNode, links []domain.Link, mids []*qNode) error {
		tr.nodes[k+1], tr.rels[k], tr.mids[k] = n, links, mids
		return r.right(p, k+1, tr, b, used, cont)
	})
}

// left extends a pattern from its node k to its first one, the relationships read backwards.
func (r *queryRun) left(p cypher.Pattern, k int, tr *qTrail, b cypher.MapScope, used map[domain.LinkID]bool, cont func() error) error {
	if k == 0 {
		return cont()
	}
	rel := p.Rels[k-1]
	dir := rel.Dir
	switch dir {
	case cypher.Right:
		dir = cypher.Left
	case cypher.Left:
		dir = cypher.Right
	}
	return r.step(tr.nodes[k], rel, dir, p.Nodes[k-1], b, used, func(n *qNode, links []domain.Link, mids []*qNode) error {
		links, mids = slices.Clone(links), slices.Clone(mids)
		slices.Reverse(links)
		slices.Reverse(mids)
		tr.nodes[k-1], tr.rels[k-1], tr.mids[k-1] = n, links, mids
		return r.left(p, k-1, tr, b, used, cont)
	})
}

// qHop is a link of a node in the view, with the node at its other end.
type qHop struct {
	link  domain.Link
	other *qNode
	out   bool // the link leaves the node
	in    bool // the link enters the node (both for a link to itself)
}

// step follows a relationship pattern from a node in a direction (Right: along the links, Left: against them), binding
// its variable and the next node pattern; a variable length one walks up to its range.
func (r *queryRun) step(from *qNode, rel cypher.RelPattern, dir cypher.Direction, next cypher.NodePattern, b cypher.MapScope,
	used map[domain.LinkID]bool, cont func(*qNode, []domain.Link, []*qNode) error) error {
	follow := func(at *qNode, visit func(qHop) error) error {
		hops, err := r.hops(at, rel.Types)
		if err != nil {
			return err
		}
		for _, h := range hops {
			if used[h.link.ID] || (dir == cypher.Right && !h.out) || (dir == cypher.Left && !h.in) {
				continue
			}
			if ok, err := r.propsMatch(rel.Props, qLink{h.link}, b); err != nil {
				return err
			} else if !ok {
				continue
			}
			used[h.link.ID] = true
			err := visit(h)
			delete(used, h.link.ID)
			if err != nil {
				return err
			}
		}
		return nil
	}
	finish := func(to *qNode, links []domain.Link, mids []*qNode, relValue any) error {
		undoRel := nop
		if rel.Var != "" {
			ok, undo, err := r.bind(rel.Var, relValue, b)
			if err != nil || !ok {
				return err
			}
			undoRel = undo
		}
		defer undoRel()
		ok, undo, err := r.bindNode(next, to, b)
		if err != nil || !ok {
			return err
		}
		defer undo()
		return cont(to, links, mids)
	}
	if rel.Range == nil {
		return follow(from, func(h qHop) error {
			return finish(h.other, []domain.Link{h.link}, nil, qLink{h.link})
		})
	}
	lo, hi := rel.Range.Min, rel.Range.Max
	if hi == 0 || hi > QueryMaxDepth {
		if hi > QueryMaxDepth {
			return fmt.Errorf("%w: a variable length relationship goes at most %d hops", ErrInvalid, QueryMaxDepth)
		}
		hi = QueryMaxDepth
		r.note("an unbounded * is read as *%d..%d", lo, QueryMaxDepth)
	}
	var links []domain.Link
	var mids []*qNode
	var walk func(at *qNode, depth int) error
	walk = func(at *qNode, depth int) error {
		if depth >= lo {
			list := make([]any, len(links))
			for i, l := range links {
				list[i] = qLink{l}
			}
			inner := mids
			if len(inner) > 0 {
				inner = inner[:len(inner)-1] // the last node is the end, not a node between
			}
			if err := finish(at, slices.Clone(links), slices.Clone(inner), list); err != nil {
				return err
			}
		}
		if depth == hi {
			return nil
		}
		return follow(at, func(h qHop) error {
			links, mids = append(links, h.link), append(mids, h.other)
			err := walk(h.other, depth+1)
			links, mids = links[:len(links)-1], mids[:len(mids)-1]
			return err
		})
	}
	return walk(from, 0)
}

// hops reads the links of some types of a node in the view (nil: every type) and the nodes at their other ends: one
// neighbourhood read, kept for the query.
func (r *queryRun) hops(n *qNode, types []string) ([]qHop, error) {
	key := string(n.n.ID) + "\x00" + strings.Join(types, "|")
	if hs, ok := r.hoods[key]; ok {
		return hs, nil
	}
	if err := r.read(); err != nil {
		return nil, err
	}
	if types != nil {
		types = r.w.linkTypes(types)
	}
	nb, err := r.w.neighbourhood(r.ctx, r.tx, n.n.ID, types)
	if err != nil {
		return nil, err
	}
	near := map[domain.NodeID]*qNode{}
	for _, m := range nb.Nodes {
		near[m.ID] = r.keep(m)
	}
	near[n.n.ID] = n
	var hs []qHop
	for _, l := range nb.Links {
		if l.External() {
			continue
		}
		h := qHop{link: l, out: l.From.ID == n.n.ID, in: l.To.ID == n.n.ID}
		other := l.To.ID
		if !h.out {
			other = l.From.ID
		}
		if h.other = near[other]; h.other == nil {
			continue
		}
		r.links[l.ID] = l
		hs = append(hs, h)
	}
	r.hoods[key] = hs
	return hs, nil
}

// export turns a value of a row into JSON, noting the nodes and links it names.
func (r *queryRun) export(v any) any {
	if r.outNodes == nil {
		r.outNodes, r.outLinks = map[domain.NodeID]bool{}, map[domain.LinkID]bool{}
	}
	switch x := v.(type) {
	case *qNode:
		r.outNodes[x.n.ID] = true
		return map[string]any{"$node": string(x.n.ID)}
	case qLink:
		r.outLinks[x.l.ID] = true
		return map[string]any{"$link": string(x.l.ID)}
	case cypher.Path:
		nodes, links := []any{}, []any{}
		for _, n := range x.Nodes {
			q := n.(*qNode)
			r.outNodes[q.n.ID] = true
			nodes = append(nodes, string(q.n.ID))
		}
		for _, l := range x.Rels {
			q := l.(qLink)
			r.outLinks[q.l.ID] = true
			links = append(links, string(q.l.ID))
		}
		return map[string]any{"$path": map[string]any{"nodes": nodes, "links": links}}
	case []any:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = r.export(it)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, it := range x {
			out[k] = r.export(it)
		}
		return out
	case float64:
		if x == float64(int64(x)) && x > -1e15 && x < 1e15 {
			return int64(x)
		}
	}
	return v
}

// exported are the nodes and links the rows name, by key and id: a link's ends read by the query come with it.
func (r *queryRun) exported() ([]domain.Node, []domain.Link) {
	var links []domain.Link
	for id := range r.outLinks {
		l := r.links[id]
		links = append(links, l)
		for _, end := range []domain.NodeID{l.From.ID, l.To.ID} {
			if _, ok := r.nodes[end]; ok {
				r.outNodes[end] = true
			}
		}
	}
	var nodes []domain.Node
	for id := range r.outNodes {
		nodes = append(nodes, r.nodes[id].n)
	}
	slices.SortFunc(nodes, func(a, b domain.Node) int { return strings.Compare(a.Key, b.Key) })
	slices.SortFunc(links, func(a, b domain.Link) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return nodes, links
}

// qNode is a node of the view as a query sees it.
type qNode struct{ n domain.Node }

func (q *qNode) Kind() string     { return "node" }
func (q *qNode) ID() string       { return string(q.n.ID) }
func (q *qNode) Labels() []string { return []string{q.n.Type} }

// Property reads the node's own fields first (key, type, state, id, version, namespace, owner, project), then its
// properties.
func (q *qNode) Property(key string) (any, bool) {
	switch key {
	case "key":
		return q.n.Key, true
	case "type":
		return q.n.Type, true
	case "state":
		return nilIfEmpty(q.n.State), true
	case "id":
		return string(q.n.ID), true
	case "version":
		return float64(q.n.Version), true
	case "namespace":
		return q.n.Namespace, true
	case "owner":
		return nilIfEmpty(q.n.Owner.Key), true
	case "project":
		return nilIfEmpty(q.n.Project.Key), true
	}
	v, ok := q.n.Properties[key]
	return v, ok
}

func (q *qNode) Properties() map[string]any { return q.n.Properties }

// qLink is a link of the view as a query sees it.
type qLink struct{ l domain.Link }

func (q qLink) Kind() string     { return "relationship" }
func (q qLink) ID() string       { return string(q.l.ID) }
func (q qLink) Labels() []string { return []string{q.l.Type} }

// Property reads the link's type and id, then its properties.
func (q qLink) Property(key string) (any, bool) {
	switch key {
	case "type":
		return q.l.Type, true
	case "id":
		return string(q.l.ID), true
	}
	v, ok := q.l.Properties[key]
	return v, ok
}

func (q qLink) Properties() map[string]any { return q.l.Properties }

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
