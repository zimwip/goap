package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// Merge and split of nodes, seen from the parent side (ADR 0077, "Merge and split"). A node never disappears and a
// merge or a split retires nothing: merging A and B into C is a modification of the parent P of A and B, whose working
// version loses the links to A and B and gains one to C; splitting A into C and D loses the link to A and gains the
// links to C and D. A and B stay in history, untouched; their change impacts carry Via, the impact of the parent that
// realizes the move (ADR 0076 §3). The successors record their lineage (Node.Origins, on their first version).

// NodeName names a node a merge or a split works on: a change impact, or a node (its id, or its key).
type NodeName struct {
	Impact domain.ChangeImpactID
	Node   domain.NodeID
	Key    string
}

// MergeInput is a merge (ImpactNodeMerge): Sources are replaced, in their parents, by the new node Into.
type MergeInput struct {
	Sources []NodeName
	Into    NodeCreate
	// Rationale says why, on the impacts the merge declares (the sources, the parents); empty: the one of Into.
	Rationale string
	// Flow and Execution replace the ones of Into (see NodeCreate).
	Flow, Execution string
	// Gate, when set, is asked about the type of every node the call writes or modifies, the parents it discovers
	// included (the access nodes, ADR 0068, are not the caller's to change): its error refuses the call. It runs inside
	// the transaction and must not read the graph.
	Gate func(nodeType string) error
}

// SplitInput is a split (ImpactNodeSplit): Source is replaced, in its parents, by the new nodes Into.
type SplitInput struct {
	Source          NodeName
	Into            []NodeCreate
	Rationale       string
	Flow, Execution string
	// Gate is the one of MergeInput.
	Gate func(nodeType string) error
}

// SuspectLink is a link of another node to a source that a split leaves as it is: which successor it should follow
// is for the reviewer to say (ADR 0003: it becomes suspect).
type SuspectLink struct {
	From    domain.NodeRef
	FromKey string
	Type    string
	To      domain.NodeRef
	ToKey   string
}

// Restructured is what a merge or a split did to the change.
type Restructured struct {
	// Successors are the impacts of the new nodes (created, checked out, with their origins).
	Successors []domain.ChangeImpact
	// Sources are the impacts of the merged or split nodes, Via the impact of their parent.
	Sources []domain.ChangeImpact
	// Parents are the impacts of the nodes whose links moved (each checked out): the parents, and for a merge the nodes
	// holding other links to the sources.
	Parents []domain.ChangeImpact
	// Suspect lists the links a split leaves to its source (see SuspectLink).
	Suspect []SuspectLink
}

// ImpactNodeMerge merges nodes into a new one, in one transaction (see the top of the file). Every source needs a
// parent: a node holding a link of a type flagged `compose` to it. The links of other types to the sources are moved
// to the new node too. Refused (ErrInvalid) for a source with no parent, nodes of another namespace or of a type
// incompatible with the new node, or created by the change; (ErrConflict) for a source checked out.
func (g *Graph) ImpactNodeMerge(ctx context.Context, id domain.ChangeID, in MergeInput) (out Restructured, err error) {
	if len(in.Sources) < 2 {
		return out, invalidf("a merge needs at least two nodes")
	}
	into := in.Into
	into.Flow, into.Execution = in.Flow, in.Execution
	err = g.repo.InTx(ctx, func(tx Tx) error {
		var err error
		out, err = g.restructure(ctx, tx, id, in.Sources, []NodeCreate{into}, true, in.Rationale, in.Flow, in.Execution, in.Gate)
		return err
	})
	return
}

// ImpactNodeSplit splits a node into new ones, in one transaction (see the top of the file). The links of other types
// to the source are left and returned as suspect.
func (g *Graph) ImpactNodeSplit(ctx context.Context, id domain.ChangeID, in SplitInput) (out Restructured, err error) {
	if len(in.Into) < 2 {
		return out, invalidf("a split needs at least two new nodes")
	}
	into := slices.Clone(in.Into)
	for i := range into {
		into[i].Flow, into[i].Execution = in.Flow, in.Execution
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		var err error
		out, err = g.restructure(ctx, tx, id, []NodeName{in.Source}, into, false, in.Rationale, in.Flow, in.Execution, in.Gate)
		return err
	})
	return
}

// restructured is a node whose links move: its current version and the links it holds to the sources.
type restructured struct {
	node    domain.Node
	parent  bool // holds a composition link to a source
	links   []domain.Link
	compose map[domain.LinkID]bool
}

func (g *Graph) restructure(ctx context.Context, tx Tx, id domain.ChangeID, sources []NodeName, into []NodeCreate, merge bool, rationale, flow, execution string, gate func(string) error) (Restructured, error) {
	var out Restructured
	if gate == nil {
		gate = func(string) error { return nil }
	}
	for _, c := range into {
		if c.Key == "" || c.Type == "" {
			return out, invalidf("a node needs a key and a type")
		}
		if err := gate(c.Type); err != nil {
			return out, err
		}
	}
	if rationale == "" {
		rationale = into[0].Rationale
	}
	// 1. the sources, as impacts of the change
	var srcs []domain.Node
	var srcImpacts []domain.ChangeImpactID
	srcSet := map[domain.NodeID]bool{}
	var w *work
	for _, name := range sources {
		var err error
		w, err = g.resolve(ctx, tx, id, target{Impact: name.Impact, Node: name.Node, Key: name.Key, Rationale: rationale, Flow: flow, Execution: execution})
		if err != nil {
			return out, err
		}
		if w.cn.Intent == domain.IntentCreated {
			return out, invalidf("%s is created by the change: create the node you want instead of merging or splitting it", w.cn.Key)
		}
		n, err := w.base(ctx, tx)
		if err != nil {
			return out, err
		}
		if n == nil {
			return out, invalidf("change impact %s has no version to merge or split", w.cn.ID)
		}
		if n.CheckedOut {
			return out, fmt.Errorf("%s is checked out by change impact %s: check it in or cancel it first: %w", n.Key, w.cn.ID, ErrConflict)
		}
		if domain.NamespaceOf(n.Namespace) != w.ns {
			return out, invalidf("node %s is in namespace %s, the change acts on %s", n.Key, domain.NamespaceOf(n.Namespace), w.ns)
		}
		if err := gate(n.Type); err != nil {
			return out, err
		}
		if srcSet[n.ID] {
			return out, invalidf("%s is named twice", n.Key)
		}
		srcSet[n.ID] = true
		for _, c := range into {
			if !w.ix.compatible(n.Type, c.Type) {
				return out, invalidf("%s (%s) cannot be replaced by %s (%s): the types are not compatible", n.Key, n.Type, c.Key, c.Type)
			}
		}
		srcs = append(srcs, *n)
		srcImpacts = append(srcImpacts, w.cn.ID)
	}

	// 2. the nodes holding links to the sources, as the flow sees them
	b, err := tx.Baseline(ctx, w.c.BaselineID)
	if err != nil {
		return out, err
	}
	holders := map[domain.NodeID]*restructured{}
	hasParent := map[domain.NodeID]bool{}
	for _, s := range srcs {
		vs, err := tx.Versions(ctx, s.ID)
		if err != nil {
			return out, err
		}
		for _, v := range vs {
			in, err := tx.InLinks(ctx, v.Ref())
			if err != nil {
				return out, err
			}
			for _, l := range in {
				cur, err := g.currentOf(ctx, tx, w, b, l.From.ID)
				if err != nil {
					return out, err
				}
				if cur == nil || cur.Ref() != l.From || cur.Deleted {
					continue // a link of a version the flow no longer sees
				}
				composes := w.ix.composes(l.Type)
				if srcSet[cur.ID] {
					if composes {
						return out, invalidf("%s is the parent of %s: merge or split nodes that are not parts of each other", cur.Key, s.Key)
					}
					continue
				}
				h := holders[cur.ID]
				if h == nil {
					h = &restructured{node: *cur, compose: map[domain.LinkID]bool{}}
					holders[cur.ID] = h
				}
				if !slices.ContainsFunc(h.links, func(x domain.Link) bool { return x.ID == l.ID }) {
					h.links = append(h.links, l)
				}
				if composes {
					h.parent, h.compose[l.ID] = true, true
					hasParent[s.ID] = true
				}
			}
		}
	}
	for _, s := range srcs {
		if !hasParent[s.ID] {
			return out, invalidf("%s has no parent: a merge or a split is a modification of the parent of the nodes, the node holding a link of a type flagged compose to it", s.Key)
		}
	}
	order := make([]*restructured, 0, len(holders))
	for _, h := range holders {
		order = append(order, h)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].node.Key < order[j].node.Key })

	// 3. the successors
	origins := make([]domain.NodeRef, len(srcs))
	for i, s := range srcs {
		origins[i] = s.Ref()
	}
	succ := make([]domain.NodeRef, len(into))
	for i, c := range into {
		cn, err := g.createTx(ctx, tx, id, c, origins)
		if err != nil {
			return out, err
		}
		succ[i] = *cn.Post
	}

	// 4. the links move in the working versions of the holders
	via := map[domain.NodeID]domain.ChangeImpactID{}
	var holderImpacts []domain.ChangeImpactID
	for _, h := range order {
		if !merge && !h.parent {
			for _, l := range h.links {
				to, _ := tx.Node(ctx, l.To)
				out.Suspect = append(out.Suspect, SuspectLink{From: l.From, FromKey: h.node.Key, Type: l.Type, To: l.To, ToKey: to.Key})
			}
			continue
		}
		if err := gate(h.node.Type); err != nil {
			return out, err
		}
		hw, err := g.resolve(ctx, tx, id, target{Node: h.node.ID, Rationale: rationale, Flow: flow, Execution: execution})
		if err != nil {
			return out, err
		}
		if head, err := hw.head(ctx, tx); err != nil {
			return out, err
		} else if head == nil || !head.CheckedOut {
			if _, err := g.checkoutTx(ctx, tx, hw, execution); err != nil {
				return out, err
			}
			if hw, err = g.resolve(ctx, tx, id, target{Node: h.node.ID, Flow: flow, Execution: execution}); err != nil {
				return out, err
			}
		}
		n, err := hw.working(ctx, tx)
		if err != nil {
			return out, err
		}
		cur, err := tx.OutLinks(ctx, n.Ref())
		if err != nil {
			return out, err
		}
		type moved struct {
			typ   string
			props map[string]any
		}
		var moves []moved
		for _, l := range cur {
			if !srcSet[l.To.ID] || (!merge && !w.ix.composes(l.Type)) {
				continue
			}
			if err := tx.DeleteLink(ctx, l.ID); err != nil {
				return out, err
			}
			if err := g.emitRemoveLink(ctx, tx, hw, n, l, execution); err != nil {
				return out, err
			}
			if w.ix.composes(l.Type) {
				if _, ok := via[l.To.ID]; !ok {
					via[l.To.ID] = hw.cn.ID
				}
			}
			if !slices.ContainsFunc(moves, func(m moved) bool { return m.typ == l.Type }) {
				moves = append(moves, moved{l.Type, l.Properties})
			}
		}
		if len(moves) == 0 {
			return out, fmt.Errorf("%s no longer holds a link to the nodes: %w", h.node.Key, ErrConflict)
		}
		for _, m := range moves {
			for _, to := range succ {
				link, err := g.putLink(ctx, tx, hw, n, LinkWrite{Type: m.typ, To: to, Properties: m.props})
				if err != nil {
					return out, err
				}
				if err := g.emitAddLink(ctx, tx, hw, n, link, execution); err != nil {
					return out, err
				}
			}
		}
		holderImpacts = append(holderImpacts, hw.cn.ID)
	}

	// 5. the impacts of the sources carry the impact of their parent
	for i, s := range srcs {
		v := via[s.ID]
		if v == "" {
			return out, fmt.Errorf("%s: no parent link was moved: %w", s.Key, ErrConflict)
		}
		sw, err := g.resolve(ctx, tx, id, target{Impact: srcImpacts[i], Flow: flow, Execution: execution})
		if err != nil {
			return out, err
		}
		switch sw.cn.Via {
		case v:
		case "":
			cn := sw.cn
			cn.Via = v
			if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: cn.ID, Op: domain.ImpactProposed, Flow: cn.Flow, Execution: cn.Execution, State: &cn}); err != nil {
				return out, err
			}
		default:
			return out, fmt.Errorf("change impact %s of %s is already realized through %s: %w", sw.cn.ID, s.Key, sw.cn.Via, ErrConflict)
		}
	}

	// 6. the result, as the flow sees it
	if w, err = g.workOn(ctx, tx, id, flow); err != nil {
		return out, err
	}
	find := func(f func(domain.ChangeImpact) bool) domain.ChangeImpact {
		i := slices.IndexFunc(w.seen, f)
		if i < 0 {
			return domain.ChangeImpact{}
		}
		return w.seen[i]
	}
	for _, c := range into {
		out.Successors = append(out.Successors, find(func(i domain.ChangeImpact) bool { return i.Key == c.Key && i.Intent == domain.IntentCreated }))
	}
	for _, i := range srcImpacts {
		out.Sources = append(out.Sources, find(func(x domain.ChangeImpact) bool { return x.ID == i }))
	}
	for _, i := range holderImpacts {
		out.Parents = append(out.Parents, find(func(x domain.ChangeImpact) bool { return x.ID == i }))
	}
	return out, nil
}

// currentOf is the version of a node the flow sees (nil: none): the head of its impact, else its pre version, else the
// version of the reference baseline.
func (g *Graph) currentOf(ctx context.Context, tx Tx, w *work, b domain.Baseline, nid domain.NodeID) (*domain.Node, error) {
	for _, i := range w.seen {
		if nodeOf(i) != nid {
			continue
		}
		w2 := *w
		if err := w2.selectImpact(i.ID); err != nil {
			return nil, err
		}
		return w2.base(ctx, tx)
	}
	v, ok := b.Nodes[nid]
	if !ok {
		return nil, nil
	}
	n, err := tx.Node(ctx, domain.NodeRef{ID: nid, Version: v})
	return &n, err
}

// composes reports a composition link type; compatible reports that a node of type a may be replaced by one of type b:
// the same type, or one a subtype of the other.
func (ix *typeIndex) composes(linkType string) bool {
	return ix != nil && ix.cat != nil && ix.cat.Composes(linkType)
}

func (ix *typeIndex) compatible(a, b string) bool {
	return a == b || (ix != nil && ix.cat != nil && (ix.cat.IsA(a, b) || ix.cat.IsA(b, a)))
}

// originsPatch describes origins for the created event of a successor: id, version and key of each.
func originsPatch(ctx context.Context, tx Tx, origins []domain.NodeRef) []any {
	out := make([]any, 0, len(origins))
	for _, o := range origins {
		m := map[string]any{"id": string(o.ID), "version": int(o.Version)}
		if n, err := tx.Node(ctx, o); err == nil {
			m["key"] = n.Key
		}
		out = append(out, m)
	}
	return out
}

// checkOrigins is the review gate of a successor (ADR 0077): a node created by the change with origins is accepted,
// frozen (accepted) and landed only when the change impact of every origin is accepted, the way what a node derives from is
// settled before it. impacts are the ones the flow sees.
func (g *Graph) checkOrigins(ctx context.Context, tx Tx, change domain.ChangeID, impacts []domain.ChangeImpact, cn domain.ChangeImpact) error {
	if cn.Intent != domain.IntentCreated || cn.Post == nil {
		return nil
	}
	first, err := tx.Node(ctx, domain.NodeRef{ID: cn.Post.ID, Version: 1})
	if errors.Is(err, ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if first.ChangeID != change || len(first.Origins) == 0 {
		return nil
	}
	var pending []string
	for _, o := range first.Origins {
		i := slices.IndexFunc(impacts, func(x domain.ChangeImpact) bool { return nodeOf(x) == o.ID })
		switch {
		case i < 0:
			pending = append(pending, string(o.ID)+" (not in the change)")
		case impacts[i].Review != domain.ReviewAccepted:
			pending = append(pending, fmt.Sprintf("%s (%s)", impacts[i].Key, impacts[i].Review))
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("%s derives from nodes whose change impacts are not accepted: %s: %w", cn.Key, strings.Join(pending, ", "), ErrConflict)
	}
	return nil
}

// DerivedNodes returns the first versions of the nodes that derive from a node (ADR 0077): the successors of the merges
// and splits that replaced it. ref.Version 0 matches any version of the node. Node.Origins answers the other way.
func (g *Graph) DerivedNodes(ctx context.Context, ref domain.NodeRef) (out []domain.Node, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		var err error
		out, err = tx.DerivedNodes(ctx, ref)
		return err
	})
	return
}
