package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/algo"
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
	validators []algo.Bound
	search     []domain.SearchProperty
}

// typeIndex is the node type metadata a change is judged by: the type catalogue in force (cat), else the NodeType
// nodes of a baseline (byName).
type typeIndex struct {
	cat    TypeCatalog
	byName map[string]typeInfo
}

func decodeProp(v any, out any) bool {
	if v == nil {
		return false
	}
	b, err := json.Marshal(v)
	return err == nil && json.Unmarshal(b, out) == nil
}

// typesAt reads (and caches: baselines are immutable) the node type metadata of a baseline.
func (g *Graph) typesAt(ctx context.Context, tx Tx, baseline domain.BaselineID) (*typeIndex, error) {
	if g.Types != nil {
		return &typeIndex{cat: g.catalog()}, nil
	}
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
		decodeProp(n.Properties["validators"], &info.validators)
		decodeProp(n.Properties["search"], &info.search)
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
	if ix.cat != nil {
		return ix.cat.Lifecycle(typ)
	}
	info, _ := ix.find(typ, func(i typeInfo) bool { return i.lifecycle != nil })
	return info.lifecycle
}

// checkNode is the existence rule (ADR 0012 §3) for a node of namespace ns; nothing is checked without a catalogue.
func (ix *typeIndex) checkNode(ns, typ string) error {
	if ix.cat == nil {
		return nil
	}
	if err := ix.cat.CheckNode(ns, typ); err != nil {
		return fmt.Errorf("%v: %w", err, ErrInvalid)
	}
	return nil
}

// checkLink checks a link type and its ends; nothing is checked without a catalogue.
func (ix *typeIndex) checkLink(typ, from, to string) error {
	if ix.cat == nil {
		return nil
	}
	if err := ix.cat.CheckLink(typ, from, to); err != nil {
		return fmt.Errorf("%v: %w", err, ErrInvalid)
	}
	return nil
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

func invalidf(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, ErrInvalid)...)
}

// ---- checks made when the change is applied ---------------------------------

func nodeView(n domain.Node) map[string]any {
	props := n.Properties
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"key": n.Key, "type": n.Type, "state": n.State, "props": props}
}

func (a *applier) checkTransition(n domain.Node, t domain.Transition) ([]domain.Node, error) {
	for _, k := range t.Requires.Attributes {
		if v, ok := n.Properties[k]; !ok || v == nil || v == "" {
			return nil, invalidf("%s (%s) cannot take %s: attribute %q is required", n.Key, n.Type, t.Name, k)
		}
	}
	out, err := a.tx.OutLinks(a.ctx, n.Ref())
	if err != nil {
		return nil, err
	}
	for _, typ := range t.Requires.OutgoingLinks {
		found := false
		for _, l := range out {
			found = found || l.Type == typ
		}
		if !found {
			return nil, invalidf("%s (%s) cannot take %s: an outgoing %q link is required", n.Key, n.Type, t.Name, typ)
		}
	}
	var children []domain.Node
	if t.Children != nil || t.Guard != "" || len(t.GuardAlgos) > 0 || len(t.ActionAlgos) > 0 {
		for _, l := range out {
			if !domain.IsContains(l.Type) {
				continue
			}
			v, ok := a.target[l.To.ID]
			if !ok {
				continue
			}
			c, err := a.tx.Node(a.ctx, domain.NodeRef{ID: l.To.ID, Version: v})
			if err != nil {
				return nil, err
			}
			if t.Children != nil && !containsString(t.Children.States, c.State) {
				got := c.State
				if got == "" {
					got = "no state"
				}
				return nil, invalidf("%s (%s) cannot take %s: contained %s (%s) is in %s, expected %s", n.Key, n.Type, t.Name, c.Key, c.Type, got, strings.Join(t.Children.States, " or "))
			}
			children = append(children, c)
		}
	}
	if t.Guard != "" {
		gd, err := guard.Compile(t.Guard)
		if err != nil {
			return nil, invalidf("guard of %s: %v", t.Name, err)
		}
		views := make([]any, 0, len(children))
		for _, c := range children {
			views = append(views, nodeView(c))
		}
		ok, err := gd.Check(nodeView(n), views, map[string]any{"id": string(a.change.ID), "title": a.change.Title,
			"intent": a.change.Intent, "methodology": a.change.Methodology, "goal": a.change.Goal})
		if err != nil {
			return nil, invalidf("guard of %s on %s: %v", t.Name, n.Key, err)
		}
		if !ok {
			return nil, invalidf("%s (%s) cannot take %s: guard not satisfied (%s)", n.Key, n.Type, t.Name, t.Guard)
		}
	}
	return children, a.runGuards(n, t, children)
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
