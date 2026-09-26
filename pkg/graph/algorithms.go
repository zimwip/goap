package graph

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
)

// This file runs the algorithms plugged in the domain (ADR 0018): the property
// validators of a node type, and the guards and actions of lifecycle
// transitions. Like the lifecycle they are metadata: the NodeType nodes of the
// change's reference baseline embed them resolved (instance parameters and
// script), so a change is judged by the model it started from.

// validatorsOf lists the property validators of a type in call order: the ones
// of the supertypes first, each type's in declaration order.
func (ix *typeIndex) validatorsOf(typ string) []algo.Bound {
	var chain [][]algo.Bound
	for seen := map[string]bool{}; typ != "" && !seen[typ]; {
		seen[typ] = true
		info, ok := ix.byName[typ]
		if !ok {
			break
		}
		chain = append(chain, info.validators)
		typ = info.extends
	}
	var out []algo.Bound
	for _, v := range slices.Backward(chain) {
		out = append(out, v...)
	}
	return out
}

func dslNode(n domain.Node, props map[string]any) dsl.Node {
	if props == nil {
		props = map[string]any{}
	}
	return dsl.Node{ID: string(n.ID), Version: int(n.Version), Key: n.Key, Type: n.Type, State: n.State, Props: props}
}

// validateProps runs the property validators of the node's type on the
// properties it will have. The first rejection is returned as ErrInvalid.
func (g *Graph) validateProps(ctx context.Context, ix *typeIndex, n domain.Node, props map[string]any) error {
	vs := ix.validatorsOf(n.Type)
	if len(vs) == 0 {
		return nil
	}
	view := dslNode(n, props)
	for _, v := range vs {
		out, err := dsl.RunAlgorithm(ctx, v, dsl.AlgorithmInput{Node: view, Property: v.Property, Value: props[v.Property]})
		if err != nil {
			return invalidf("%s (%s): property %q: %v", n.Key, n.Type, v.Property, err)
		}
		if !out.OK() {
			return invalidf("%s (%s): property %q is invalid: %s (validator %s)", n.Key, n.Type, v.Property, out.Failures[0], v.Instance)
		}
	}
	return nil
}

// checkProps validates the properties of the nodes the change creates or updates
// in the target graph.
func (a *applier) checkProps() error {
	for _, id := range a.order {
		b := a.bumped[id]
		if b.deleted || b.merge || !b.updated {
			continue
		}
		n, err := a.tx.Node(a.ctx, b.next)
		if err != nil {
			return err
		}
		if err := a.g.validateProps(a.ctx, a.walk.ix, n, n.Properties); err != nil {
			return err
		}
	}
	for _, ref := range a.created {
		n, err := a.tx.Node(a.ctx, ref)
		if err != nil {
			return err
		}
		if n.Version != 1 {
			continue
		}
		if err := a.g.validateProps(a.ctx, a.walk.ix, n, n.Properties); err != nil {
			return err
		}
	}
	return nil
}

func (a *applier) changeInfo() dsl.ChangeInfo {
	return dsl.ChangeInfo{ID: string(a.change.ID), Title: a.change.Title, Intent: a.change.Intent, Methodology: a.change.Methodology, Goal: a.change.Goal}
}

func childViews(children []domain.Node) []dsl.Node {
	out := make([]dsl.Node, 0, len(children))
	for _, c := range children {
		out = append(out, dslNode(c, c.Properties))
	}
	return out
}

// runGuards runs the guard algorithms of a transition: all must accept.
func (a *applier) runGuards(n domain.Node, t domain.Transition, children []domain.Node) error {
	in := dsl.AlgorithmInput{Node: dslNode(n, n.Properties), Children: childViews(children), Change: a.changeInfo(),
		Transition: dsl.TransitionInfo{Name: t.Name, From: t.From, To: t.To}}
	for _, g := range t.GuardAlgos {
		out, err := dsl.RunAlgorithm(a.ctx, g, in)
		if err != nil {
			return invalidf("guard %s of %s on %s: %v", g.Instance, t.Name, n.Key, err)
		}
		if !out.OK() {
			return invalidf("%s (%s) cannot take %s: %s (guard %s)", n.Key, n.Type, t.Name, out.Failures[0], g.Instance)
		}
	}
	return nil
}

// runActions runs the action algorithms of a transition in order and writes the
// property changes they ask for into the version the transition produced. The
// changed properties are validated again.
func (a *applier) runActions(n domain.Node, t domain.Transition, children []domain.Node) error {
	if len(t.ActionAlgos) == 0 {
		return nil
	}
	props := maps.Clone(n.Properties)
	if props == nil {
		props = map[string]any{}
	}
	changed := false
	for _, act := range t.ActionAlgos {
		cur := n
		cur.Properties = props
		out, err := dsl.RunAlgorithm(a.ctx, act, dsl.AlgorithmInput{Node: dslNode(cur, props), Children: childViews(children), Change: a.changeInfo(),
			Transition: dsl.TransitionInfo{Name: t.Name, From: t.From, To: t.To}})
		if err != nil {
			return invalidf("action %s of %s on %s: %v", act.Instance, t.Name, n.Key, err)
		}
		if !out.OK() {
			return invalidf("%s (%s): action %s of %s failed: %s", n.Key, n.Type, act.Instance, t.Name, out.Failures[0])
		}
		if len(out.Set) > 0 || len(out.Unset) > 0 {
			props = maps.Clone(props)
			maps.Copy(props, out.Set)
			for _, k := range out.Unset {
				delete(props, k)
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := a.g.validateProps(a.ctx, a.walk.ix, n, props); err != nil {
		return fmt.Errorf("after the actions of %s: %w", t.Name, err)
	}
	return a.tx.SetNodeProps(a.ctx, n.Ref(), props)
}
