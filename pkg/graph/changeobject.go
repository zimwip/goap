package graph

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/guard"
	"github.com/zimwip/goap/pkg/typecat"
)

// Change objects (ADR 0098): what a change carries beyond its impacts, typed by the change object types of the domains.
// The graph checks a change object against its type (key, attributes, validators, lifecycle) without knowing why it is
// there; every write is an object.<type> entry of the log of the change and the last version of each change object is
// kept in a projection (Tx.PutChangeObject), as the impacts are (ADR 0029).

// ObjectTypes is what the graph needs of the change object types of the catalogue in force; typecat.Catalog
// implements it. An untyped graph judges change objects by the built-in domains.
type ObjectTypes interface {
	ObjectType(ref string) (*typecat.ObjectType, bool)
}

// objectTypes is the catalogue of change object types in force.
// ObjectType is the change object type the graph validates a write of (the catalogue's, else the built-in one).
func (g *Graph) ObjectType(ref string) (*typecat.ObjectType, bool) {
	return g.objectTypes().ObjectType(ref)
}

func (g *Graph) objectTypes() ObjectTypes {
	if g.Types != nil {
		if ot, ok := g.Types().(ObjectTypes); ok {
			return ot
		}
	}
	return typecat.Builtin()
}

var labelKeyRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// validLabels checks the labels of a write: short keys of letters, digits and _ . : -, at most 16.
func validLabels(l map[string]string) error {
	if len(l) > 16 {
		return invalidf("at most 16 labels")
	}
	for k, v := range l {
		if !labelKeyRE.MatchString(k) {
			return invalidf("invalid label %q (letters, digits, _ . : -)", k)
		}
		if len(v) > 256 {
			return invalidf("label %s is longer than 256 bytes", k)
		}
	}
	return nil
}

// PutObjects writes change objects on a change, in order and as one write: each a new change object or a new version
// of one (ADR 0098). The change must be open (neither committed, applied nor abandoned).
func (g *Graph) PutObjects(ctx context.Context, id domain.ChangeID, writes []domain.ObjectWrite) (out []domain.ChangeObject, err error) {
	if len(writes) == 0 {
		return nil, invalidf("no change object to write")
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		out, err = g.putObjectsTx(ctx, tx, id, writes)
		return err
	})
	return
}

func (g *Graph) putObjectsTx(ctx context.Context, tx Tx, id domain.ChangeID, writes []domain.ObjectWrite) ([]domain.ChangeObject, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned || c.Status == domain.ChangeCommitted {
		return nil, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	out := make([]domain.ChangeObject, 0, len(writes))
	for _, w := range writes {
		o, err := g.putObjectTx(ctx, tx, c, w)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	if c.Status == domain.ChangeDraft { // a change holding something is active, as with items
		c.Status = domain.ChangeActive
		return out, tx.PutChange(ctx, c)
	}
	return out, nil
}

// putObjectTx writes one change object of c inside a transaction.
func (g *Graph) putObjectTx(ctx context.Context, tx Tx, c domain.Change, w domain.ObjectWrite) (domain.ChangeObject, error) {
	ot, ok := g.objectTypes().ObjectType(w.Type)
	if !ok {
		return domain.ChangeObject{}, invalidf("unknown change object type %q", w.Type)
	}
	if err := validLabels(w.Labels); err != nil {
		return domain.ChangeObject{}, err
	}
	o := domain.ChangeObject{Change: c.ID, Type: ot.Ref.String(), Labels: maps.Clone(w.Labels), By: g.caller(ctx), At: g.now()}
	if ot.Scope == def.ScopeWorkspace {
		o.Workspace = c.ResolveFlow(w.Workspace)
		if o.Workspace != "" {
			if _, ok := c.Flow(o.Workspace); !ok {
				return o, fmt.Errorf("workspace %s of change %s: %w", o.Workspace, c.ID, ErrNotFound)
			}
		}
	}
	same, err := tx.ChangeObjects(ctx, c.ID, domain.ObjectFilter{Types: []string{o.Type}, Workspaces: []string{o.Workspace}})
	if err != nil {
		return o, err
	}
	find := func(key string) *domain.ChangeObject {
		for i := range same {
			if same[i].Key == key {
				return &same[i]
			}
		}
		return nil
	}
	var prev *domain.ChangeObject
	// the value first: a natural key is made of it
	value := cloneMap(w.Value)
	switch ot.Key.Kind {
	case def.KeySingleton:
		if w.Key != "" {
			return o, invalidf("%s is a singleton: no key", o.Type)
		}
		prev = find("")
	case def.KeySequence:
		if w.Key == "" {
			o.Key = nextSequenceKey(ot.Key.Prefix, same)
		} else if prev = find(w.Key); prev == nil {
			return o, fmt.Errorf("change object %s/%s: %w", o.Type, w.Key, ErrNotFound)
		}
	case def.KeyNatural:
		// a merge reads the natural key of the version it merges into
		if w.Merge && w.Key != "" {
			prev = find(w.Key)
		}
	case def.KeyRef:
		if w.Key == "" {
			return o, invalidf("%s is keyed by a reference to a %s: the key is required", o.Type, ot.Key.Ref)
		}
		if err := g.checkObjectRef(ctx, tx, c, ot.Key.Ref, w.Key); err != nil {
			return o, err
		}
		prev = find(w.Key)
	}
	if w.Merge && prev != nil {
		merged := cloneMap(prev.Value)
		if merged == nil {
			merged = map[string]any{}
		}
		for k, v := range value {
			if v == nil {
				delete(merged, k)
			} else {
				merged[k] = v
			}
		}
		value = merged
	}
	if ot.Key.Kind == def.KeyNatural {
		key, err := naturalKey(ot, value)
		if err != nil {
			return o, err
		}
		if w.Key != "" && w.Key != key {
			return o, invalidf("the key of %s is made of %s: %q, not %q", o.Type, strings.Join(ot.Key.Attributes, ", "), key, w.Key)
		}
		prev = find(key)
		o.Key = key
	}
	if w.Expect != nil {
		have := 0
		if prev != nil {
			have = prev.Version
		}
		if have != *w.Expect {
			return o, fmt.Errorf("change object %s/%s is at version %d, not %d: %w", o.Type, o.Key, have, *w.Expect, ErrConflict)
		}
	}
	if prev != nil {
		o.Key, o.Version, o.State = prev.Key, prev.Version+1, prev.State
	} else {
		if ot.Key.Kind != def.KeySequence && ot.Key.Kind != def.KeyNatural {
			o.Key = w.Key
		}
		o.Version = 1
		if ot.Lifecycle != nil {
			o.State = ot.Lifecycle.Initial
		}
	}
	if len(value) > 0 {
		o.Value = value
	}
	if err := g.checkObjectValue(ctx, ot, o); err != nil {
		return o, err
	}
	if w.Transition != "" {
		if err := g.moveObject(ctx, c, ot, &o, w.Transition); err != nil {
			return o, err
		}
	}
	e, err := domain.ObjectEntry(g.newID(), o)
	if err != nil {
		return o, err
	}
	if e, err = tx.AppendLog(ctx, e); err != nil {
		return o, err
	}
	o.Seq = e.Seq
	return o, tx.PutChangeObject(ctx, o)
}

// nextSequenceKey allocates the next key of a sequence: one more than the highest number used, never reused.
func nextSequenceKey(prefix string, existing []domain.ChangeObject) string {
	n := 0
	for _, x := range existing {
		if s, ok := strings.CutPrefix(x.Key, prefix+"-"); ok {
			if i, err := strconv.Atoi(s); err == nil && i > n {
				n = i
			}
		}
	}
	return prefix + "-" + strconv.Itoa(n+1)
}

// naturalKey makes the key of a change object from the values of the key attributes of its type, joined by "/".
func naturalKey(ot *typecat.ObjectType, value map[string]any) (string, error) {
	parts := make([]string, 0, len(ot.Key.Attributes))
	for _, a := range ot.Key.Attributes {
		v := value[a]
		if v == nil || v == "" {
			return "", invalidf("%s is keyed by %s: %q is required", ot.Ref, strings.Join(ot.Key.Attributes, ", "), a)
		}
		parts = append(parts, fmt.Sprint(v))
	}
	return strings.Join(parts, "/"), nil
}

// checkObjectRef checks that the reference of a ref key designates an object of the change: one of its impacts, a
// node, one of its workspaces or a change object of the named type. A run is the engine's and a request is not checked
// here: their keys are taken as given.
func (g *Graph) checkObjectRef(ctx context.Context, tx Tx, c domain.Change, ref, key string) error {
	switch ref {
	case def.RefImpact:
		impacts, err := tx.ChangeImpacts(ctx, c.ID)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(impacts, func(cn domain.ChangeImpact) bool { return string(cn.ID) == key }) {
			return fmt.Errorf("change impact %s of change %s: %w", key, c.ID, ErrNotFound)
		}
	case def.RefNode:
		if _, err := tx.Node(ctx, domain.NodeRef{ID: domain.NodeID(key)}); err != nil {
			return fmt.Errorf("node %s: %w", key, err)
		}
	case def.RefWorkspace:
		if key != domain.MainFlow {
			if _, ok := c.Flow(key); !ok {
				return fmt.Errorf("workspace %s of change %s: %w", key, c.ID, ErrNotFound)
			}
		}
	case def.RefRun, def.RefRequest:
	default:
		objs, err := tx.ChangeObjects(ctx, c.ID, domain.ObjectFilter{Types: []string{ref}})
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(objs, func(o domain.ChangeObject) bool { return o.Key == key }) {
			return fmt.Errorf("change object %s/%s of change %s: %w", ref, key, c.ID, ErrNotFound)
		}
	}
	return nil
}

// checkObjectValue checks the value of a change object: attributes of its type only (unless open), their type and enum
// membership, then the property validators of its attributes.
func (g *Graph) checkObjectValue(ctx context.Context, ot *typecat.ObjectType, o domain.ChangeObject) error {
	if !ot.AdditionalProperties {
		names := ot.PropertyNames()
		for _, k := range slices.Sorted(maps.Keys(o.Value)) {
			if !slices.Contains(names, k) {
				return invalidf("%s (%s): property %q is not an attribute of %s", o.Key, o.Type, k, o.Type)
			}
		}
	}
	for _, a := range ot.Attributes {
		check := domain.AttributeCheck{Name: a.Name, Type: a.Type, Enum: a.Enum}
		for _, v := range a.Values {
			check.Values = append(check.Values, v.Value)
		}
		if err := checkAttributeValue(check, o.Value[a.Name]); err != nil {
			return invalidf("%s (%s): property %q is invalid: %v", o.Key, o.Type, a.Name, err)
		}
	}
	if len(ot.Validators) == 0 {
		return nil
	}
	props := o.Value
	if props == nil {
		props = map[string]any{}
	}
	view := dsl.Node{Key: o.Key, Type: o.Type, State: o.State, Props: props}
	for _, v := range ot.Validators {
		if v.Type != algo.UsagePropertyValidator {
			continue
		}
		out, err := dsl.RunAlgorithm(ctx, v, dsl.AlgorithmInput{Node: view, Property: v.Property, Value: props[v.Property]})
		if err != nil {
			return invalidf("%s (%s): property %q: %v", o.Key, o.Type, v.Property, err)
		}
		if !out.OK() {
			return invalidf("%s (%s): property %q is invalid: %s (validator %s)", o.Key, o.Type, v.Property, out.Failures[0], v.Instance)
		}
	}
	return nil
}

// moveObject takes a transition of the lifecycle of the type of a change object, out of its current state: the
// attributes it requires must be set and its CEL guard (object as `node`, the change as `change`) must hold.
func (g *Graph) moveObject(ctx context.Context, c domain.Change, ot *typecat.ObjectType, o *domain.ChangeObject, name string) error {
	if ot.Lifecycle == nil {
		return invalidf("%s has no lifecycle: no transition %s", o.Type, name)
	}
	t, ok := ot.Lifecycle.Transition(o.State, name)
	if !ok {
		return invalidf("%s (%s) cannot take %s from %s", o.Key, o.Type, name, o.State)
	}
	for _, a := range t.Requires.Attributes {
		if v := o.Value[a]; v == nil || v == "" {
			return invalidf("%s (%s) cannot take %s: %q is required", o.Key, o.Type, name, a)
		}
	}
	if t.Guard != "" {
		gd, err := guard.Compile(t.Guard)
		if err != nil {
			return invalidf("guard of %s: %v", t.Name, err)
		}
		props := o.Value
		if props == nil {
			props = map[string]any{}
		}
		ok, err := gd.Check(map[string]any{"key": o.Key, "type": o.Type, "state": o.State, "props": props}, nil,
			map[string]any{"id": string(c.ID), "title": c.Title, "intent": c.Intent, "status": string(c.Status)}, nil, nil)
		if err != nil {
			return invalidf("guard of %s on %s: %v", t.Name, o.Key, err)
		}
		if !ok {
			return invalidf("%s (%s) cannot take %s: guard not satisfied (%s)", o.Key, o.Type, name, t.Guard)
		}
	}
	o.State = t.To
	return nil
}

// Objects returns the change objects of a change matching f: the last version of each, or the versions in force at
// f.AtSeq, in the order of their first version.
func (g *Graph) Objects(ctx context.Context, id domain.ChangeID, f domain.ObjectFilter) (out []domain.ChangeObject, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if _, err := tx.Change(ctx, id); err != nil {
			return err
		}
		out, err = objectsTx(ctx, tx, id, f)
		return err
	})
	return
}

func objectsTx(ctx context.Context, tx Tx, id domain.ChangeID, f domain.ObjectFilter) ([]domain.ChangeObject, error) {
	if f.AtSeq <= 0 {
		return tx.ChangeObjects(ctx, id, f)
	}
	entries, err := tx.Log(ctx, domain.LogFilter{Change: id, Types: []string{domain.LogObject + "."}})
	if err != nil {
		return nil, err
	}
	entries = slices.DeleteFunc(entries, func(e domain.LogEntry) bool { return e.Seq > f.AtSeq })
	all, err := domain.FoldObjects(entries)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(o domain.ChangeObject) bool { return !f.Match(o) }), nil
}
