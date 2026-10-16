package domain

import (
	"slices"
	"strings"
)

// The difference between two flows of a change (ADR 0083): each flow sees its change impacts through drafts (ADR 0079),
// and an impact is added, removed or modified by one flow compared with the other. The functions here are pure: they
// compare two shapes of a node, the state a flow sees of it.

// Kinds of a FieldChange.
const (
	FieldProperty = "property"
	FieldState    = "state"
	FieldOwner    = "owner"
	FieldLink     = "link"
)

// Operations of a FieldChange, from the left shape to the right one.
const (
	OpAdded   = "added"
	OpRemoved = "removed"
	OpChanged = "changed"
)

// Categories of an impact in a FlowDiff (identical impacts are counted, not listed).
const (
	DiffAdded    = "added"
	DiffRemoved  = "removed"
	DiffModified = "modified"
)

// LinkShape is an outgoing link of a node as a flow sees it: the type and the target node (the key names it for a
// reader), not the version, since a draft points at the draft of its target.
type LinkShape struct {
	Type       string         `json:"type"`
	To         NodeID         `json:"to"`
	ToKey      string         `json:"toKey,omitempty"`
	Properties map[string]any `json:"props,omitempty"`
}

// NodeShape is the content of a node as a flow sees it: what its draft carries.
type NodeShape struct {
	Key        string         `json:"key"`
	Type       string         `json:"type"`
	State      string         `json:"state,omitempty"`
	Owner      NodeID         `json:"owner,omitempty"`
	Properties map[string]any `json:"props,omitempty"`
	Links      []LinkShape    `json:"links,omitempty"`
}

// FieldChange is one difference between two shapes: a property (Name), the lifecycle state, the owner, or a link (Name is
// its type, Target the key of its target).
type FieldChange struct {
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	Op     string `json:"op"`
	Target string `json:"target,omitempty"`
	Old    any    `json:"old,omitempty"`
	New    any    `json:"new,omitempty"`
}

// ImpactSide is what one flow sees of an impact: its identity and review on that flow.
type ImpactSide struct {
	Impact ChangeImpactID `json:"impact"`
	Flow   string         `json:"flow,omitempty"`
	Intent NodeIntent     `json:"intent"`
	Review NodeReview     `json:"review"`
	// Drafted is false for a planned impact: its node is not checked out, no draft exists.
	Drafted bool   `json:"drafted"`
	State   string `json:"state,omitempty"`
	Owner   NodeID `json:"owner,omitempty"`
}

// ImpactDiff is an impact that differs between the two flows. Changes is empty for an added or removed impact.
type ImpactDiff struct {
	Node     NodeID        `json:"node"`
	Key      string        `json:"key"`
	Type     string        `json:"type"`
	Category string        `json:"category"`
	Left     *ImpactSide   `json:"left,omitempty"`
	Right    *ImpactSide   `json:"right,omitempty"`
	Changes  []FieldChange `json:"changes,omitempty"`
}

// FlowDiff compares the impacts two flows of a change see, at a level (written or accepted). Left and Right are the flows
// ("main" for the main flow, else an option id).
type FlowDiff struct {
	Change  ChangeID     `json:"change"`
	Left    string       `json:"left"`
	Right   string       `json:"right"`
	Level   string       `json:"level"`
	Impacts []ImpactDiff `json:"impacts"`
	// Identical counts the impacts both flows see with the same content: they are left out of Impacts.
	Identical int `json:"identical"`
}

// DiffCounts counts the impacts of each category.
func (d FlowDiff) DiffCounts() map[string]int {
	out := map[string]int{DiffAdded: 0, DiffRemoved: 0, DiffModified: 0}
	for _, i := range d.Impacts {
		out[i.Category]++
	}
	return out
}

// DiffShapes lists what changes from the left shape to the right one: the state, the owner, the properties (by name), then
// the links (a link is the type and the target node; the same one with other properties changed). Empty: identical. The
// result is ordered: state, owner, properties by name, links by type then target.
func DiffShapes(left, right NodeShape) []FieldChange {
	var out []FieldChange
	if left.State != right.State {
		out = append(out, FieldChange{Kind: FieldState, Op: changeOp(left.State == "", right.State == ""), Old: left.State, New: right.State})
	}
	if left.Owner != right.Owner {
		out = append(out, FieldChange{Kind: FieldOwner, Op: changeOp(left.Owner == "", right.Owner == ""), Old: string(left.Owner), New: string(right.Owner)})
	}
	names := map[string]bool{}
	for k := range left.Properties {
		names[k] = true
	}
	for k := range right.Properties {
		names[k] = true
	}
	sorted := make([]string, 0, len(names))
	for k := range names {
		sorted = append(sorted, k)
	}
	slices.Sort(sorted)
	for _, k := range sorted {
		l, lok := left.Properties[k]
		r, rok := right.Properties[k]
		switch {
		case lok && !rok:
			out = append(out, FieldChange{Kind: FieldProperty, Name: k, Op: OpRemoved, Old: l})
		case !lok && rok:
			out = append(out, FieldChange{Kind: FieldProperty, Name: k, Op: OpAdded, New: r})
		case !sameValue(l, true, r, true):
			out = append(out, FieldChange{Kind: FieldProperty, Name: k, Op: OpChanged, Old: l, New: r})
		}
	}
	return append(out, diffLinks(left.Links, right.Links)...)
}

// changeOp names the operation of a value that was empty or is: set from nothing is added, to nothing removed.
func changeOp(wasEmpty, isEmpty bool) string {
	switch {
	case wasEmpty:
		return OpAdded
	case isEmpty:
		return OpRemoved
	}
	return OpChanged
}

func shapeLinkKey(l LinkShape) string { return l.Type + "\x00" + string(l.To) }

func linkLabel(l LinkShape) string {
	if l.ToKey != "" {
		return l.ToKey
	}
	return string(l.To)
}

func diffLinks(left, right []LinkShape) []FieldChange {
	byKey := func(ls []LinkShape) map[string]LinkShape {
		m := map[string]LinkShape{}
		for _, l := range ls {
			m[shapeLinkKey(l)] = l
		}
		return m
	}
	lm, rm := byKey(left), byKey(right)
	keys := map[string]bool{}
	for k := range lm {
		keys[k] = true
	}
	for k := range rm {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	slices.SortFunc(sorted, func(a, b string) int {
		la, lb := lm[a], lm[b]
		if _, ok := lm[a]; !ok {
			la = rm[a]
		}
		if _, ok := lm[b]; !ok {
			lb = rm[b]
		}
		if c := strings.Compare(la.Type, lb.Type); c != 0 {
			return c
		}
		return strings.Compare(linkLabel(la), linkLabel(lb))
	})
	var out []FieldChange
	for _, k := range sorted {
		l, lok := lm[k]
		r, rok := rm[k]
		switch {
		case lok && !rok:
			out = append(out, FieldChange{Kind: FieldLink, Name: l.Type, Op: OpRemoved, Target: linkLabel(l), Old: emptyAsNil(l.Properties)})
		case !lok && rok:
			out = append(out, FieldChange{Kind: FieldLink, Name: r.Type, Op: OpAdded, Target: linkLabel(r), New: emptyAsNil(r.Properties)})
		case !sameValue(emptyAsNil(l.Properties), true, emptyAsNil(r.Properties), true):
			out = append(out, FieldChange{Kind: FieldLink, Name: r.Type, Op: OpChanged, Target: linkLabel(r), Old: emptyAsNil(l.Properties), New: emptyAsNil(r.Properties)})
		}
	}
	return out
}

func emptyAsNil(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	return m
}
