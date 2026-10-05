package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/dsl"
)

// This file runs the algorithms plugged in the domain (ADR 0018): the attribute
// and node validators of a node type, and the guards and actions of lifecycle
// transitions. Like the lifecycle they come from the type catalogue in force,
// resolved (instance parameters and script).

// validatorsOf lists the property validators of a type in call order: the ones
// of the supertypes first, each type's in declaration order.
func (ix *typeIndex) validatorsOf(typ string) []algo.Bound {
	if ix == nil || ix.cat == nil {
		return nil
	}
	return ix.cat.Validators(typ)
}

func dslNode(n domain.Node, props map[string]any) dsl.Node {
	if props == nil {
		props = map[string]any{}
	}
	return dsl.Node{ID: string(n.ID), Version: int(n.Version), Key: n.Key, Type: n.Type, State: n.State, Props: props}
}

// checkAttributes checks the type and enum membership of the values of the attributes of a node: what every edit of
// a working version is held to (ADR 0076), a draft being otherwise free to be incomplete.
func (ix *typeIndex) checkAttributes(n domain.Node, props map[string]any) error {
	if ix == nil || ix.cat == nil {
		return nil
	}
	for _, a := range ix.cat.AttributeChecks(n.Type) {
		if err := checkAttributeValue(a, props[a.Name]); err != nil {
			return invalidf("%s (%s): property %q is invalid: %v", n.Key, n.Type, a.Name, err)
		}
	}
	return nil
}

// checkLinkAttributes checks the type and enum membership of the properties of a link against the attributes of its
// link type.
func (ix *typeIndex) checkLinkAttributes(typ string, props map[string]any) error {
	if ix == nil || ix.cat == nil {
		return nil
	}
	for _, a := range ix.cat.LinkAttributeChecks(typ) {
		if err := checkAttributeValue(a, props[a.Name]); err != nil {
			return invalidf("link %s: property %q is invalid: %v", typ, a.Name, err)
		}
	}
	return nil
}

// validateProps checks the properties a version is frozen with (ImpactNodeCheckin) and lands with (Apply): the type and
// enum membership of the values of its attributes, then the validators of its type (attribute validators and node
// validators). The first rejection is returned as ErrInvalid.
func (g *Graph) validateProps(ctx context.Context, ix *typeIndex, n domain.Node, props map[string]any) error {
	if err := ix.checkAttributes(n, props); err != nil {
		return err
	}
	vs := ix.validatorsOf(n.Type)
	if len(vs) == 0 {
		return nil
	}
	view := dslNode(n, props)
	for _, v := range vs {
		out, err := dsl.RunAlgorithm(ctx, v, dsl.AlgorithmInput{Node: view, Property: v.Property, Value: props[v.Property]})
		if v.Type == algo.UsageNodeValidator {
			if err != nil {
				return invalidf("%s (%s): node validator %s: %v", n.Key, n.Type, v.Instance, err)
			}
			if !out.OK() {
				return invalidf("%s (%s) is invalid: %s (validator %s)", n.Key, n.Type, out.Failures[0], v.Instance)
			}
			continue
		}
		if err != nil {
			return invalidf("%s (%s): property %q: %v", n.Key, n.Type, v.Property, err)
		}
		if !out.OK() {
			return invalidf("%s (%s): property %q is invalid: %s (validator %s)", n.Key, n.Type, v.Property, out.Failures[0], v.Instance)
		}
	}
	return nil
}

// checkAttributeValue checks a value against the type of its attribute. A missing or empty value is accepted
// (requiring one is the job of a validator), and so is any value of an attribute that names no type.
func checkAttributeValue(a domain.AttributeCheck, v any) error {
	if v == nil || v == "" {
		return nil
	}
	switch a.Type {
	case def.AttrNumber:
		switch x := v.(type) {
		case float64, float32, int, int32, int64, uint, uint32, uint64, json.Number:
			return nil
		case string:
			if _, err := strconv.ParseFloat(x, 64); err == nil {
				return nil
			}
		}
		return fmt.Errorf("a number is expected, got %v", v)
	case def.AttrBoolean:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("a boolean is expected, got %v", v)
		}
	case def.AttrDate:
		s, ok := v.(string)
		if ok {
			for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05"} {
				if _, err := time.Parse(layout, s); err == nil {
					return nil
				}
			}
		}
		return fmt.Errorf("a date (YYYY-MM-DD or RFC 3339) is expected, got %v", v)
	case def.AttrEnum:
		s, ok := v.(string)
		if ok {
			if slices.Contains(a.Values, s) {
				return nil
			}
		}
		return fmt.Errorf("%v is not a value of the enum %s", v, a.Enum)
	case def.AttrString:
		if _, ok := v.(string); !ok {
			return fmt.Errorf("a string is expected, got %v", v)
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
	if a.impact != nil {
		in.Impact = dsl.ImpactInfo{Intent: string(a.impact.Intent), Review: string(a.impact.Review)}
	}
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
	if err := a.g.validateProps(a.ctx, a.ix, n, props); err != nil {
		return fmt.Errorf("after the actions of %s: %w", t.Name, err)
	}
	return a.tx.SetNodeProps(a.ctx, n.Ref(), props)
}
