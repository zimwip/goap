package graph

import (
	"context"
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/guard"
)

// This file implements the node lifecycle rules (ADR 0014). The lifecycle of
// a node type comes from the type catalogue in force (ADR 0012 §2).
//
//   - a node is modified only in an editable state, and a persisted version is
//     never editable: a change reopens a node (a transition into an editable
//     state), modifies it, and must move it out again before it is applied;
//   - every transition is checked (it exists, the actor may take it, the node
//     has the required attributes and links, its guard holds, and for a
//     document its children are in an allowed state) when it is taken: a
//     transition is a version of its own, from a checked-in version
//     (ImpactNodeTransition, ADR 0076).

// TransitionAuthorizer decides whether the caller may take a transition on a
// node. Nil allows every transition. n.State is the state it leaves.
type TransitionAuthorizer func(ctx context.Context, n domain.Node, t domain.Transition) error

// typeIndex is the node type metadata a change is judged by: the type catalogue in force (ADR 0012 §2). Without one
// (Graph.Types unset) the graph is untyped: no lifecycle, no validator, no check.
type typeIndex struct {
	cat TypeCatalog
}

// typesAt returns the types a change of the baseline is judged by: the catalogue in force.
func (g *Graph) typesAt(context.Context, Tx, domain.BaselineID) (*typeIndex, error) {
	if g.Types == nil {
		return &typeIndex{}, nil
	}
	return &typeIndex{cat: g.catalog()}, nil
}

func (ix *typeIndex) lifecycleOf(typ string) *domain.Lifecycle {
	if ix == nil || ix.cat == nil {
		return nil
	}
	return ix.cat.Lifecycle(typ)
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
// (the type has a lifecycle): they must go through a change. The type catalogue is
// process-global, not namespace-scoped graph data (typesAt ignores its baseline
// argument), so this needs no baseline lookup at all.
func (g *Graph) LifecycleControlled(_ context.Context, typ string) (bool, error) {
	if g.Types == nil {
		return false, nil
	}
	return g.catalog().Lifecycle(typ) != nil, nil
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, ErrInvalid)...)
}

// ---- checks made when a transition is taken ---------------------------------

func nodeView(n domain.Node) map[string]any {
	props := n.Properties
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"key": n.Key, "type": n.Type, "state": n.State, "props": props}
}

// impactView is the impact of a node as a guard sees it (ADR 0076): intent, review and the review history.
func impactView(cn *domain.ChangeImpact) map[string]any {
	if cn == nil {
		return map[string]any{}
	}
	reviews := make([]any, 0, len(cn.Reviews))
	for _, r := range cn.Reviews {
		reviews = append(reviews, map[string]any{"status": string(r.Status), "by": r.By, "comment": r.Comment})
	}
	return map[string]any{"intent": string(cn.Intent), "review": string(cn.Review), "reviews": reviews}
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
			"intent": a.change.Intent, "methodology": a.change.Methodology, "goal": a.change.Goal}, impactView(a.impact))
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
