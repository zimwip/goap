package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/guard"
)

// This file implements the node lifecycle rules (ADR 0014). The lifecycle of
// a node type is metadata: it is read from the NodeType nodes of the change's
// reference baseline, so a change is always judged by the model it started from.
//
//   - a node is modified only in an editable state, and a persisted version is
//     never editable: a change reopens a node (a transition into an editable
//     state), modifies it, and must move it out again before it is applied;
//   - every transition is checked (it exists, the actor may take it, the node
//     has the required attributes and links, its guard holds, and for a
//     document its children are in an allowed state) when the change is applied.

// NodeTypeNode is the type of the graph nodes that hold node types (the
// metadata layer, ADR 0012).
const NodeTypeNode = "NodeType"

// TransitionAuthorizer decides whether the caller may take a transition on a
// node. Nil allows every transition. n.State is the state it leaves.
type TransitionAuthorizer func(ctx context.Context, n domain.Node, t domain.Transition) error

type typeInfo struct {
	extends    string
	lifecycle  *domain.Lifecycle
	document   *domain.DocumentSpec
	controlled *bool
}

// typeIndex is the node type metadata of a baseline.
type typeIndex struct{ byName map[string]typeInfo }

func decodeProp(v any, out any) bool {
	if v == nil {
		return false
	}
	b, err := json.Marshal(v)
	return err == nil && json.Unmarshal(b, out) == nil
}

// typesAt reads (and caches: baselines are immutable) the node type metadata of a baseline.
func (g *Graph) typesAt(ctx context.Context, tx Tx, baseline domain.BaselineID) (*typeIndex, error) {
	if ix, ok := g.types.Load(baseline); ok {
		return ix.(*typeIndex), nil
	}
	nodes, err := tx.NodesIn(ctx, baseline, NodeTypeNode)
	if err != nil {
		return nil, err
	}
	ix := &typeIndex{byName: map[string]typeInfo{}}
	for _, n := range nodes {
		name, _ := n.Properties["name"].(string)
		if _, dup := ix.byName[name]; name == "" || dup {
			continue
		}
		info := typeInfo{}
		info.extends, _ = n.Properties["extends"].(string)
		var lc domain.Lifecycle
		if decodeProp(n.Properties["lifecycle"], &lc) {
			info.lifecycle = &lc
		}
		var doc domain.DocumentSpec
		if decodeProp(n.Properties["document"], &doc) {
			info.document = &doc
		}
		if b, ok := n.Properties["changeControlled"].(bool); ok {
			info.controlled = &b
		}
		ix.byName[name] = info
	}
	g.types.Store(baseline, ix)
	return ix, nil
}

// find returns the nearest declaration along the extends chain.
func (ix *typeIndex) find(typ string, pick func(typeInfo) bool) (typeInfo, bool) {
	for seen := map[string]bool{}; typ != "" && !seen[typ]; {
		seen[typ] = true
		info, ok := ix.byName[typ]
		if !ok {
			return typeInfo{}, false
		}
		if pick(info) {
			return info, true
		}
		typ = info.extends
	}
	return typeInfo{}, false
}

func (ix *typeIndex) lifecycleOf(typ string) *domain.Lifecycle {
	info, _ := ix.find(typ, func(i typeInfo) bool { return i.lifecycle != nil })
	return info.lifecycle
}

func (ix *typeIndex) documentOf(typ string) *domain.DocumentSpec {
	info, _ := ix.find(typ, func(i typeInfo) bool { return i.document != nil })
	return info.document
}

// controlledType tells whether the nodes of a type are modified through changes only.
func (ix *typeIndex) controlledType(typ string) bool {
	info, ok := ix.find(typ, func(i typeInfo) bool { return i.controlled != nil })
	return !ok || *info.controlled
}

// LifecycleControlled tells whether direct writes to nodes of a type are refused
// (the type has a lifecycle): they must go through a change. It reads the
// metadata of the head of main.
func (g *Graph) LifecycleControlled(ctx context.Context, typ string) (bool, error) {
	var controlled bool
	err := g.repo.InTx(ctx, func(tx Tx) error {
		head, err := g.headOrLatest(ctx, tx)
		if err != nil || head == "" {
			return err
		}
		ix, err := g.typesAt(ctx, tx, head)
		if err != nil {
			return err
		}
		controlled = ix.lifecycleOf(typ) != nil
		return nil
	})
	return controlled, err
}

// headOrLatest returns the head of main, else the most recent baseline ("" when none).
func (g *Graph) headOrLatest(ctx context.Context, tx Tx) (domain.BaselineID, error) {
	if b, err := tx.Branch(ctx, domain.MainBranch); err == nil {
		return b.Head, nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	bs, err := tx.Baselines(ctx)
	if err != nil || len(bs) == 0 {
		return "", err
	}
	latest := bs[0]
	for _, b := range bs[1:] {
		if b.CreatedAt.After(latest.CreatedAt) {
			latest = b
		}
	}
	return latest.ID, nil
}

// walked is the outcome of replaying the transitions of a change.
type walked struct {
	ix      *typeIndex
	state   map[domain.NodeID]string              // final state of the existing nodes the change moves
	moves   map[domain.NodeID][]domain.Transition // transitions taken, in order
	created map[domain.ItemID]string              // state of the nodes the change creates
	createT map[domain.ItemID]domain.Transition   // creation moves (initial → state)
}

func (w *walked) stateOf(n domain.Node) (state string, managed bool) {
	if s, ok := w.state[n.ID]; ok {
		return s, true
	}
	return n.State, n.State != ""
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, ErrInvalid)...)
}

// walk replays, in item order, the non-rejected proposals of c against the
// lifecycles of its reference baseline. It is the single implementation of the
// edit rules, used when items are added (early feedback) and when the change is
// applied. Transition permissions are only checked when applying: the actor who
// applies a change is the one who validates the transitions it contains.
func (g *Graph) walk(ctx context.Context, tx Tx, c domain.ChangeSet, authorize bool) (*walked, error) {
	if err := checkNamespace(ctx, tx, c); err != nil {
		return nil, err
	}
	ix, err := g.typesAt(ctx, tx, c.BaselineID)
	if err != nil {
		return nil, err
	}
	w := &walked{ix: ix, state: map[domain.NodeID]string{}, moves: map[domain.NodeID][]domain.Transition{},
		created: map[domain.ItemID]string{}, createT: map[domain.ItemID]domain.Transition{}}
	editable := func(ref *domain.NodeRef, what string) error {
		if ref == nil {
			return nil
		}
		n, err := tx.Node(ctx, *ref)
		if err != nil {
			return err
		}
		lc := ix.lifecycleOf(n.Type)
		if lc == nil {
			return nil
		}
		if s, managed := w.stateOf(n); managed && !lc.Editable(s) {
			return invalidf("cannot %s %s (%s): it is %s, not editable; reopen it in this change with a transition", what, n.Key, n.Type, s)
		}
		return nil
	}
	for _, it := range c.Items {
		if it.Kind != domain.KindProposal || !c.InEffect(it.ID) {
			continue
		}
		p := it.Proposal
		switch p.Op {
		case domain.OpCreateNode:
			lc := ix.lifecycleOf(p.Node.Type)
			if lc == nil {
				if p.Node.State != "" {
					return nil, invalidf("node type %s has no lifecycle: state %q", p.Node.Type, p.Node.State)
				}
				continue
			}
			st := p.Node.State
			if st == "" {
				st = lc.Initial
			}
			if _, ok := lc.State(st); !ok {
				return nil, invalidf("%s has no state %q", p.Node.Type, st)
			}
			if st != lc.Initial {
				t, ok := lc.Move(lc.Initial, st)
				if !ok {
					return nil, invalidf("a %s cannot be created in %s: no transition from %s", p.Node.Type, st, lc.Initial)
				}
				if authorize && g.Authorizer != nil {
					if err := g.Authorizer(ctx, domain.Node{Key: p.Node.Key, Type: p.Node.Type, State: lc.Initial}, t); err != nil {
						return nil, err
					}
				}
				w.createT[it.ID] = t
			}
			w.created[it.ID] = st
		case domain.OpTransitionNode:
			n, err := tx.Node(ctx, *p.Node.Base)
			if err != nil {
				return nil, err
			}
			lc := ix.lifecycleOf(n.Type)
			if lc == nil {
				return nil, invalidf("node type %s has no lifecycle", n.Type)
			}
			cur, managed := w.stateOf(n)
			if !managed {
				cur = lc.Initial
			}
			t, ok := lc.Move(cur, p.Node.State)
			if !ok {
				return nil, invalidf("%s (%s) cannot go from %s to %s", n.Key, n.Type, cur, p.Node.State)
			}
			if authorize && g.Authorizer != nil {
				n.State = cur
				if err := g.Authorizer(ctx, n, t); err != nil {
					return nil, err
				}
			}
			w.state[n.ID] = p.Node.State
			w.moves[n.ID] = append(w.moves[n.ID], t)
		case domain.OpUpdateNode:
			if err := editable(p.Node.Base, "update"); err != nil {
				return nil, err
			}
		case domain.OpDeleteNode:
			if err := editable(p.Node.Base, "delete"); err != nil {
				return nil, err
			}
		case domain.OpAddLink:
			if err := editable(p.Link.From.Node, "link from"); err != nil {
				return nil, err
			}
		}
	}
	return w, nil
}

// modifies lists the existing nodes (with the version the change starts from)
// that the items modify: the ones the change is attached to.
func modifies(items []domain.ChangeItem) []domain.NodeRef {
	var out []domain.NodeRef
	for _, it := range items {
		p := it.Proposal
		if it.Kind != domain.KindProposal || p == nil {
			continue
		}
		switch p.Op {
		case domain.OpUpdateNode, domain.OpDeleteNode, domain.OpTransitionNode, domain.OpMergeNode:
			if p.Node != nil && p.Node.Base != nil {
				out = append(out, *p.Node.Base)
			}
		case domain.OpAddLink:
			if p.Link != nil && p.Link.From.Node != nil {
				out = append(out, *p.Link.From.Node)
			}
		}
	}
	return out
}

// ---- checks made when the change is applied ---------------------------------

func nodeView(n domain.Node) map[string]any {
	props := n.Properties
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"key": n.Key, "type": n.Type, "state": n.State, "props": props}
}

// checkMoves validates, once the target graph is built, the last transition
// taken by each moved node and the states the change leaves nodes in.
func (a *applier) checkMoves() error {
	// nodes moved (existing) and created with a state move
	type moved struct {
		node domain.Node
		t    domain.Transition
	}
	var moves []moved
	var editable []string
	check := func(n domain.Node) {
		if lc := a.walk.ix.lifecycleOf(n.Type); lc != nil && n.State != "" && lc.Editable(n.State) {
			editable = append(editable, fmt.Sprintf("%s (%s) in %s", n.Key, n.Type, n.State))
		}
	}
	for _, id := range a.order {
		b := a.bumped[id]
		if b.deleted {
			continue
		}
		n, err := a.tx.Node(a.ctx, b.next)
		if err != nil {
			return err
		}
		check(n)
		if ts := a.walk.moves[id]; len(ts) > 0 {
			moves = append(moves, moved{n, ts[len(ts)-1]})
		}
	}
	for item, ref := range a.created {
		n, err := a.tx.Node(a.ctx, ref)
		if err != nil {
			return err
		}
		check(n)
		if t, ok := a.walk.createT[item]; ok {
			moves = append(moves, moved{n, t})
		}
	}
	if len(editable) > 0 {
		return invalidf("the change leaves nodes in an editable state, move them out of it before applying: %s", strings.Join(editable, ", "))
	}
	for _, m := range moves {
		if err := a.checkTransition(m.node, m.t); err != nil {
			return err
		}
	}
	return nil
}

func (a *applier) checkTransition(n domain.Node, t domain.Transition) error {
	for _, k := range t.Requires.Attributes {
		if v, ok := n.Properties[k]; !ok || v == nil || v == "" {
			return invalidf("%s (%s) cannot take %s: attribute %q is required", n.Key, n.Type, t.Name, k)
		}
	}
	out, err := a.tx.OutLinks(a.ctx, n.Ref())
	if err != nil {
		return err
	}
	for _, typ := range t.Requires.OutgoingLinks {
		found := false
		for _, l := range out {
			found = found || l.Type == typ
		}
		if !found {
			return invalidf("%s (%s) cannot take %s: an outgoing %q link is required", n.Key, n.Type, t.Name, typ)
		}
	}
	var children []any
	if t.Children != nil || t.Guard != "" {
		for _, l := range out {
			if l.Type != domain.LinkContains {
				continue
			}
			v, ok := a.target[l.To.ID]
			if !ok {
				continue
			}
			c, err := a.tx.Node(a.ctx, domain.NodeRef{ID: l.To.ID, Version: v})
			if err != nil {
				return err
			}
			if t.Children != nil && !containsString(t.Children.States, c.State) {
				got := c.State
				if got == "" {
					got = "no state"
				}
				return invalidf("%s (%s) cannot take %s: contained %s (%s) is in %s, expected %s", n.Key, n.Type, t.Name, c.Key, c.Type, got, strings.Join(t.Children.States, " or "))
			}
			children = append(children, nodeView(c))
		}
	}
	if t.Guard != "" {
		gd, err := guard.Compile(t.Guard)
		if err != nil {
			return invalidf("guard of %s: %v", t.Name, err)
		}
		ok, err := gd.Check(nodeView(n), children, map[string]any{"id": string(a.change.ID), "title": a.change.Title,
			"intent": a.change.Intent, "methodology": a.change.Methodology, "goal": a.change.Goal})
		if err != nil {
			return invalidf("guard of %s on %s: %v", t.Name, n.Key, err)
		}
		if !ok {
			return invalidf("%s (%s) cannot take %s: guard not satisfied (%s)", n.Key, n.Type, t.Name, t.Guard)
		}
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
