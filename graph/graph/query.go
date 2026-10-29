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

// A read query in the subset of openCypher (ADR 0160) over a view: MATCH and OPTIONAL MATCH patterns, WHERE, EXISTS,
// RETURN. A pattern starts from an anchor (a variable bound already, a key or an id, else the nodes of a label, read a
// page at a time) and grows a hop at a time through the neighbourhoods of the view, each reading the link types and
// the direction its relationship names, a frontier at a time (hint: the nodes a hop reaches are read together by the
// next). Nothing reads a namespace whole: the anchors, the hops and the rows are capped.

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
	// queryMaxRuns is the most times a query runs to learn what its caller reads (QueryOptions.Visible).
	queryMaxRuns = 4
)

// queryHopBatch is the most nodes whose hops one read takes (the frontier a hop reads ahead, queryRun.hint); 1 reads
// node by node (a test compares both).
var queryHopBatch = 200

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
	plan, err := planQuery(q, opts)
	if err != nil {
		return QueryResult{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	var vis map[domain.NodeID]bool // what the caller reads, learnt run after run
	if opts.Visible != nil {
		vis = map[domain.NodeID]bool{}
	}
	reach, err := g.reachOf(ctx)
	if err != nil {
		return QueryResult{}, err
	}
	reads := 0
	for attempt := 1; ; attempt++ {
		run := &queryRun{g: g, ctx: ctx, plan: plan, opts: opts, vis: vis, nodes: map[domain.NodeID]*qNode{},
			hoods: map[string][]qHop{}, links: map[domain.LinkID]domain.Link{}, wanted: map[string][]*qNode{},
			ahead: map[string]Neighbourhood{}}
		if attempt == 1 && vis != nil {
			run.vis = nil // the first run sees every node, to learn which ones the caller reads
		}
		var res QueryResult
		err = g.repo.InTx(ctx, func(tx Tx) error {
			w, err := g.openWorld(ctx, tx, v, reach)
			if err != nil {
				return err
			}
			run.tx, run.w = tx, w
			res.Commit = w.commit
			return run.match()
		})
		reads += run.reads
		if err != nil && !errors.Is(err, errQueryStop) {
			var ee *cypher.EvalError
			if errors.As(err, &ee) {
				return QueryResult{}, fmt.Errorf("%w: %w", ErrInvalid, err)
			}
			return QueryResult{}, err
		}
		if vis != nil {
			met := run.pending
			if run.vis == nil {
				met = map[domain.NodeID]domain.Node{}
				for id, n := range run.nodes {
					met[id] = n.n
				}
			}
			hidden := false
			for id, n := range met {
				ok := opts.Visible(n)
				vis[id] = ok
				hidden = hidden || !ok || run.vis != nil // a node met for the first time was left out of this run
			}
			if hidden && attempt < queryMaxRuns {
				if !v.IsChange() && v.Commit == "" {
					v.Commit = res.Commit // the next run reads the same state
				}
				continue
			}
			if hidden && len(run.pending) > 0 {
				run.truncated = true
				run.note("some nodes met late were left out: anchor the query more narrowly")
			}
		}
		cols, rows, err := cypher.Project(plan.q.Return, plan.vars, run.rows)
		if err != nil {
			return QueryResult{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		res.Columns, res.Truncated, res.Notes, res.Reads = cols, run.truncated, run.notes, reads
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
}

// errQueryStop ends a match early: a cap, or the rows a streaming RETURN keeps; errFound ends the match of an EXISTS
// at its first witness.
var (
	errQueryStop = errors.New("query stopped")
	errFound     = errors.New("found")
)

// queryPlan is a query made ready to match: its clauses, the conditions each checks, the EXISTS of its RETURN.
type queryPlan struct {
	q       *cypher.Query
	clauses []qClause
	all     []queryCond // the conditions of the MATCH clauses, checked again on each row
	vars    []string    // the variables of the patterns, in order: RETURN *
	late    []cypher.Exists
	stream  int // the rows to bind before stopping (-1: all)
}

// qClause is a MATCH or an OPTIONAL MATCH: its patterns, the conditions checked while it matches (those of the MATCH
// clauses up to it, and its own WHERE for an OPTIONAL MATCH) and the variables it binds.
type qClause struct {
	patterns []cypher.Pattern
	optional bool
	own      []queryCond
	conds    []queryCond
	binds    []string
}

// queryCond is a condition with the variables it reads, checked as soon as they are bound.
type queryCond struct {
	expr cypher.Expr
	vars []string
}

// existsVar names the value an EXISTS of RETURN or ORDER BY takes in a row.
func existsVar(i int) string { return fmt.Sprintf("$exists%d", i) }

// planQuery splits the clauses and their WHERE, checks the variables they read and takes the EXISTS out of RETURN.
func planQuery(q *cypher.Query, opts QueryOptions) (*queryPlan, error) {
	p := &queryPlan{q: q}
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
		p.vars = append(p.vars, name)
		return nil
	}
	for name := range opts.Bind {
		if err := declare(name, "node"); err != nil {
			return nil, err
		}
	}
	slices.Sort(p.vars)
	// conds reads a WHERE: each conjunct with the variables of the query it reads (those an EXISTS binds itself apart)
	conds := func(where cypher.Expr) ([]queryCond, error) {
		var out []queryCond
		for _, c := range cypher.Conjuncts(where) {
			if err := defined(c, kinds); err != nil {
				return nil, err
			}
			var vars []string
			for _, v := range cypher.Vars(c) {
				if _, ok := kinds[v]; ok {
					vars = append(vars, v)
				}
			}
			out = append(out, queryCond{expr: c, vars: vars})
		}
		return out, nil
	}
	var mandatory []queryCond
	for _, m := range q.Matches {
		cl := qClause{patterns: m.Patterns, optional: m.Optional}
		for _, pt := range m.Patterns {
			for i, n := range pt.Nodes {
				if _, ok := kinds[n.Var]; !ok && n.Var != "" {
					cl.binds = append(cl.binds, n.Var)
				}
				if err := declare(n.Var, "node"); err != nil {
					return nil, err
				}
				if i < len(pt.Rels) {
					if v := pt.Rels[i].Var; v != "" && !slices.Contains(cl.binds, v) {
						cl.binds = append(cl.binds, v)
					}
					if err := declare(pt.Rels[i].Var, "relationship"); err != nil {
						return nil, err
					}
				}
			}
			if pt.Var != "" {
				cl.binds = append(cl.binds, pt.Var)
			}
			if err := declare(pt.Var, "path"); err != nil {
				return nil, err
			}
		}
		if m.Optional { // its WHERE reads what is bound by it or before it
			own, err := conds(m.Where)
			if err != nil {
				return nil, err
			}
			cl.own = own
		}
		p.clauses = append(p.clauses, cl)
	}
	for i, m := range q.Matches { // a MATCH's WHERE may read any variable of the query
		if m.Optional {
			continue
		}
		cs, err := conds(m.Where)
		if err != nil {
			return nil, err
		}
		p.clauses[i].own = cs
		mandatory = append(mandatory, cs...)
	}
	var upTo []queryCond
	for i := range p.clauses {
		cl := &p.clauses[i]
		if !cl.optional {
			upTo = append(upTo, cl.own...)
			cl.conds = slices.Clone(upTo)
		} else {
			cl.conds = append(slices.Clone(upTo), cl.own...)
		}
	}
	p.all = mandatory
	// the EXISTS of RETURN and ORDER BY are matched with the row, their values variables of it
	late := func(e cypher.Expr) cypher.Expr {
		return cypher.Rewrite(e, func(x cypher.Expr) cypher.Expr {
			if ex, ok := x.(cypher.Exists); ok {
				p.late = append(p.late, ex)
				return cypher.Var{Name: existsVar(len(p.late) - 1)}
			}
			return x
		})
	}
	aliases := map[string]bool{}
	ret := &q.Return
	for i, it := range ret.Items {
		if err := defined(it.Expr, kinds); err != nil {
			return nil, err
		}
		ret.Items[i].Expr = late(it.Expr)
		aliases[it.Name] = true
	}
	for i, s := range ret.Order {
		for _, v := range cypher.Vars(s.Expr) {
			if _, ok := kinds[v]; !ok && !aliases[v] && !slices.Contains(cypher.Declared(s.Expr), v) {
				return nil, fmt.Errorf("variable %s is not defined", v)
			}
		}
		ret.Order[i].Expr = late(s.Expr)
	}
	if _, _, err := cypher.Bounds(*ret); err != nil {
		return nil, err
	}
	p.stream = -1
	// rows a hidden node leaves out are found by the next run: the first one goes on to its caps
	if n, ok := cypher.Streams(*ret); ok && opts.Visible == nil {
		p.stream = n
	}
	return p, nil
}

// defined refuses an expression reading a variable no pattern binds (the patterns of its EXISTS included).
func defined(e cypher.Expr, kinds map[string]string) error {
	local := cypher.Declared(e)
	for _, v := range cypher.Vars(e) {
		if _, ok := kinds[v]; !ok && !slices.Contains(local, v) {
			return fmt.Errorf("variable %s is not defined", v)
		}
	}
	return nil
}

type queryRun struct {
	g    *Graph
	ctx  context.Context
	tx   Tx
	w    *world
	plan *queryPlan
	opts QueryOptions

	conds []queryCond // the conditions bind checks: those of the clause, or of the EXISTS, being matched

	vis     map[domain.NodeID]bool        // what the caller reads (nil: every node)
	pending map[domain.NodeID]domain.Node // the nodes met that vis does not know yet, left out of this run

	nodes map[domain.NodeID]*qNode // the nodes read, by id
	hoods map[string][]qHop        // the hops read, by node, link types and ways
	links map[domain.LinkID]domain.Link
	reads int
	// wanted are the nodes whose hops the match reads next, by link types and ways (hint); ahead the neighbourhoods
	// read with another's in a batch, by node, link types and ways, not counted as reads until the match takes them
	wanted map[string][]*qNode
	ahead  map[string]Neighbourhood

	rows      []cypher.MapScope
	truncated bool
	notes     []string

	outNodes map[domain.NodeID]bool
	outLinks map[domain.LinkID]bool
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
	b := cypher.MapScope{}
	names := make([]string, 0, len(r.opts.Bind))
	for name := range r.opts.Bind {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		id := r.opts.Bind[name]
		ns, err := r.byID(id)
		if err != nil {
			return err
		}
		if len(ns) == 0 {
			return fmt.Errorf("node %s is not in the view: %w", id, ErrNotFound)
		}
		b[name] = ns[0]
	}
	return r.clause(0, b)
}

// clause matches the clauses from i on, then binds a row; an OPTIONAL MATCH that matches nothing binds its variables
// to null.
func (r *queryRun) clause(i int, b cypher.MapScope) error {
	if i == len(r.plan.clauses) {
		return r.emit(b)
	}
	cl := r.plan.clauses[i]
	saved := r.conds
	r.conds = cl.conds
	defer func() { r.conds = saved }()
	found := false
	err := r.patterns(cl.patterns, 0, b, map[domain.LinkID]bool{}, func() error {
		if cl.optional {
			for _, c := range cl.own { // the ones no binding checked (read before the clause, or constants)
				if ok, err := r.holds(c.expr, b); err != nil || !ok {
					return err
				}
			}
		}
		found = true
		r.conds = saved
		defer func() { r.conds = cl.conds }()
		return r.clause(i+1, b)
	})
	if err != nil || found || !cl.optional {
		return err
	}
	r.conds = nil // the nulls are checked with the row (emit)
	var undos []func()
	defer func() {
		for _, u := range slices.Backward(undos) {
			u()
		}
	}()
	for _, v := range cl.binds {
		if _, ok := b[v]; ok {
			continue
		}
		ok, undo, err := r.bind(v, nil, b)
		if err != nil || !ok {
			return err
		}
		undos = append(undos, undo)
	}
	return r.clause(i+1, b)
}

// emit binds a row: the conditions once more (the ones no binding checked), the EXISTS of RETURN.
func (r *queryRun) emit(b cypher.MapScope) error {
	for _, c := range r.plan.all {
		ok, err := r.holds(c.expr, b)
		if err != nil || !ok {
			return err
		}
	}
	if len(r.rows) >= r.opts.MaxRows {
		return r.stop("stopped after %d rows", r.opts.MaxRows)
	}
	row := make(cypher.MapScope, len(b)+len(r.plan.late))
	for k, v := range b {
		row[k] = v
	}
	for i, ex := range r.plan.late {
		ok, err := r.exists(ex, b)
		if err != nil {
			return err
		}
		row[existsVar(i)] = ok
	}
	r.rows = append(r.rows, row)
	if r.plan.stream >= 0 && len(r.rows) >= r.plan.stream {
		return errQueryStop
	}
	return nil
}

// exists matches the patterns of an EXISTS in a row: true at the first witness. The variables the row binds are
// those of the row, the others local to it.
func (r *queryRun) exists(ex cypher.Exists, b cypher.MapScope) (bool, error) {
	var conds []queryCond
	for _, c := range cypher.Conjuncts(ex.Where) {
		conds = append(conds, queryCond{expr: c, vars: cypher.Vars(c)})
	}
	sub := make(cypher.MapScope, len(b))
	for k, v := range b {
		sub[k] = v
	}
	saved := r.conds
	r.conds = conds
	defer func() { r.conds = saved }()
	err := r.patterns(ex.Patterns, 0, sub, map[domain.LinkID]bool{}, func() error {
		for _, c := range conds {
			if ok, err := r.holds(c.expr, sub); err != nil || !ok {
				return err
			}
		}
		return errFound
	})
	if errors.Is(err, errFound) {
		return true, nil
	}
	return false, err
}

// patterns matches the patterns of a clause from i on, then goes on with cont.
func (r *queryRun) patterns(ps []cypher.Pattern, i int, b cypher.MapScope, used map[domain.LinkID]bool, cont func() error) error {
	if i == len(ps) {
		return cont()
	}
	p := ps[i]
	at, cands, err := r.anchor(p, b)
	if err != nil {
		return err
	}
	tr := &qTrail{nodes: make([]*qNode, len(p.Nodes)), rels: make([][]domain.Link, len(p.Rels)), mids: make([][]*qNode, len(p.Rels))}
	if at < len(p.Rels) { // the first hop of each candidate, read for them all
		r.hint(cands, p.Rels[at].Types, p.Rels[at].Dir)
	} else if at > 0 {
		r.hint(cands, p.Rels[at-1].Types, reversed(p.Rels[at-1].Dir))
	}
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
					return r.patterns(ps, i+1, b, used, cont)
				}
				ok, undo, err := r.bind(p.Var, tr.path(r), b)
				if err != nil || !ok {
					return err
				}
				defer undo()
				return r.patterns(ps, i+1, b, used, cont)
			})
		})
		undo()
		if err != nil {
			return err
		}
	}
	return nil
}

// anchor picks the node pattern a pattern starts from and its candidate nodes: a variable already bound, else a key
// or an id, else the nodes of a label (narrowed by the text of a WHERE on it).
func (r *queryRun) anchor(p cypher.Pattern, b cypher.MapScope) (int, []*qNode, error) {
	for i, n := range p.Nodes {
		if v, ok := b[n.Var]; ok && n.Var != "" {
			if q, ok := v.(*qNode); ok {
				return i, []*qNode{q}, nil
			}
			return i, nil, nil // null (an OPTIONAL MATCH that matched nothing): no node
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
	if !r.w.sees(n) {
		return nil, nil
	}
	if q := r.keep(n); q != nil {
		return []*qNode{q}, nil
	}
	return nil, nil
}

// keep notes a node read, nil for a node the caller does not read (or not known yet: the next run asks for it).
func (r *queryRun) keep(n domain.Node) *qNode {
	if q, ok := r.nodes[n.ID]; ok {
		return q
	}
	if r.vis != nil {
		if ok, known := r.vis[n.ID]; !ok {
			if !known {
				if r.pending == nil {
					r.pending = map[domain.NodeID]domain.Node{}
				}
				r.pending[n.ID] = n
			}
			return nil
		}
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
		// a branch view reads the nodes of the type a batch at a time, never its state whole
		nodes, more, ok, err := r.w.ofType(r.ctx, r.tx, typ, text, room)
		if err != nil {
			return nil, err
		}
		total := -1
		if !ok {
			page, err := r.w.page(r.ctx, r.tx, NodeQuery{Type: typ, Text: text, Limit: room + 1})
			if err != nil {
				return nil, err
			}
			nodes, total = page.Nodes, page.Total
		}
		for _, node := range nodes {
			if node.Type != typ {
				continue
			}
			if len(out) == r.opts.MaxAnchors {
				more = true
				break
			}
			if q := r.keep(node); q != nil {
				out = append(out, q)
			}
		}
		if more {
			r.truncated = true
			of := ""
			if total >= 0 {
				of = fmt.Sprintf(" (of %d)", total)
			}
			r.note("only the first %d nodes of %s were read%s: narrow the anchor with a key or a WHERE on a property", r.opts.MaxAnchors, label, of)
			break
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
	v, err := cypher.Eval(e, liveScope{b, r})
	return cypher.Truthy(v), err
}

// liveScope is a row being matched: its variables, and the graph its EXISTS match in.
type liveScope struct {
	cypher.MapScope
	r *queryRun
}

func (s liveScope) Exists(x cypher.Exists) (bool, error) { return s.r.exists(x, s.MapScope) }

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
	then := nop1
	if k+1 < len(p.Rels) {
		next := p.Rels[k+1]
		then = func(ns []*qNode) { r.hint(ns, next.Types, next.Dir) }
	}
	return r.step(tr.nodes[k], p.Rels[k], p.Rels[k].Dir, p.Nodes[k+1], b, used, then, func(n *qNode, links []domain.Link, mids []*qNode) error {
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
	then := nop1
	if k >= 2 {
		next := p.Rels[k-2]
		then = func(ns []*qNode) { r.hint(ns, next.Types, reversed(next.Dir)) }
	}
	return r.step(tr.nodes[k], rel, reversed(rel.Dir), p.Nodes[k-1], b, used, then, func(n *qNode, links []domain.Link, mids []*qNode) error {
		links, mids = slices.Clone(links), slices.Clone(mids)
		slices.Reverse(links)
		slices.Reverse(mids)
		tr.nodes[k-1], tr.rels[k-1], tr.mids[k-1] = n, links, mids
		return r.left(p, k-1, tr, b, used, cont)
	})
}

// reversed is a direction read backwards.
func reversed(d cypher.Direction) cypher.Direction {
	switch d {
	case cypher.Right:
		return cypher.Left
	case cypher.Left:
		return cypher.Right
	}
	return d
}

func nop1([]*qNode) {}

// qHop is a link of a node in the view, with the node at its other end.
type qHop struct {
	link  domain.Link
	other *qNode
	out   bool // the link leaves the node
	in    bool // the link enters the node (both for a link to itself)
}

// step follows a relationship pattern from a node in a direction (Right: along the links, Left: against them), binding
// its variable and the next node pattern; a variable length one walks up to its range. then is told the nodes a hop
// reaches before the match visits them, the frontier of the next hop (hint).
func (r *queryRun) step(from *qNode, rel cypher.RelPattern, dir cypher.Direction, next cypher.NodePattern, b cypher.MapScope,
	used map[domain.LinkID]bool, then func([]*qNode), cont func(*qNode, []domain.Link, []*qNode) error) error {
	lo, hi := 1, 1
	if rel.Range != nil {
		lo, hi = rel.Range.Min, rel.Range.Max
		if hi == 0 || hi > QueryMaxDepth {
			if hi > QueryMaxDepth {
				return fmt.Errorf("%w: a variable length relationship goes at most %d hops", ErrInvalid, QueryMaxDepth)
			}
			hi = QueryMaxDepth
			r.note("an unbounded * is read as *%d..%d", lo, QueryMaxDepth)
		}
	}
	follow := func(at *qNode, depth int, visit func(qHop) error) error {
		hops, err := r.hops(at, rel.Types, dir)
		if err != nil {
			return err
		}
		var reached []*qNode
		for _, h := range hops {
			if (dir != cypher.Right || h.out) && (dir != cypher.Left || h.in) {
				reached = append(reached, h.other)
			}
		}
		if depth+1 < hi { // the next hop of the walk
			r.hint(reached, rel.Types, dir)
		}
		if depth+1 >= lo {
			then(reached)
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
		return follow(from, 0, func(h qHop) error {
			return finish(h.other, []domain.Link{h.link}, nil, qLink{h.link})
		})
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
		return follow(at, depth, func(h qHop) error {
			links, mids = append(links, h.link), append(mids, h.other)
			err := walk(h.other, depth+1)
			links, mids = links[:len(links)-1], mids[:len(mids)-1]
			return err
		})
	}
	return walk(from, 0)
}

// waysOf are the links a hop in a direction reads: those leaving the node along the links, those entering it against
// them, both for either.
func waysOf(dir cypher.Direction) linkWays {
	switch dir {
	case cypher.Right:
		return leaving
	case cypher.Left:
		return entering
	}
	return bothWays
}

// hopKind names the hops of some link types (nil: every type) in a direction.
func hopKind(types []string, dir cypher.Direction) string {
	if types == nil {
		return fmt.Sprintf("*\x00%d", waysOf(dir))
	}
	return fmt.Sprintf("%s\x00%d", strings.Join(types, "|"), waysOf(dir))
}

// hint notes nodes whose hops of some types in a direction the match reads next: the next read of such hops reads
// theirs with it, in one batch, rather than one read per node.
func (r *queryRun) hint(nodes []*qNode, types []string, dir cypher.Direction) {
	if len(nodes) < 2 {
		return
	}
	kind := hopKind(types, dir)
	for _, n := range nodes {
		key := string(n.n.ID) + "\x00" + kind
		if _, ok := r.hoods[key]; ok {
			continue
		}
		if _, ok := r.ahead[key]; ok {
			continue
		}
		r.wanted[kind] = append(r.wanted[kind], n)
	}
}

// hops reads the links of some types of a node in the view (nil: every type) in a direction, and the nodes at their
// other ends: one neighbourhood read, kept for the query, which also reads the neighbourhoods of the nodes hinted for
// the same hop (a frontier, one batch).
func (r *queryRun) hops(n *qNode, types []string, dir cypher.Direction) ([]qHop, error) {
	kind := hopKind(types, dir)
	key := string(n.n.ID) + "\x00" + kind
	if hs, ok := r.hoods[key]; ok {
		return hs, nil
	}
	if err := r.read(); err != nil {
		return nil, err
	}
	nb, ok := r.ahead[key]
	if ok {
		delete(r.ahead, key)
	} else {
		batch, in := []domain.Node{n.n}, map[domain.NodeID]bool{n.n.ID: true}
		wanted := r.wanted[kind]
		for len(wanted) > 0 && len(batch) < queryHopBatch {
			m := wanted[0]
			wanted = wanted[1:]
			mk := string(m.n.ID) + "\x00" + kind
			if _, ok := r.hoods[mk]; ok || in[m.n.ID] {
				continue
			}
			if _, ok := r.ahead[mk]; ok {
				continue
			}
			batch, in[m.n.ID] = append(batch, m.n), true
		}
		r.wanted[kind] = wanted
		lt := types
		if lt != nil {
			lt = r.w.linkTypes(types)
		}
		nbs, err := r.w.neighbourhoods(r.ctx, r.tx, batch, lt, waysOf(dir))
		if err != nil {
			return nil, err
		}
		for _, m := range batch[1:] {
			r.ahead[string(m.ID)+"\x00"+kind] = nbs[m.ID]
		}
		nb = nbs[n.n.ID]
	}
	near := map[domain.NodeID]*qNode{}
	for _, m := range nb.Nodes {
		if q := r.keep(m); q != nil {
			near[m.ID] = q
		}
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
	case "responsible", "owner":
		return nilIfEmpty(q.n.Responsible.Key), true
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
