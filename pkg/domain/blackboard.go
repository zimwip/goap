package domain

import (
	"maps"
	"time"
)

// NodeView is a node version hydrated with its neighbourhood in the domain
// graph. It lets conditions navigate the reference graph without I/O.
type NodeView struct {
	Node
	Latest Version `json:"latest"`
	// Frozen: the node has a lifecycle and is in a state that is not editable
	// (it must be reopened by a transition before it can be modified).
	Frozen bool   `json:"frozen,omitempty"`
	Out    []Link `json:"out,omitempty"`
	In     []Link `json:"in,omitempty"`
}

// Blackboard is the state an agent process observes: the change (axis change)
// plus a hydrated view of every domain node it references (axis domain).
type Blackboard struct {
	Change Change               `json:"change"`
	Nodes  map[NodeRef]NodeView `json:"-"`
	// Neighbors holds the endpoints of the links of hydrated nodes.
	Neighbors map[NodeRef]Node `json:"-"`
	Vars      map[string]any   `json:"vars,omitempty"`
	// Supertypes maps node types to their ancestors (subtyping of the
	// methodology schema), exposed to conditions as x.types.
	Supertypes map[string][]string `json:"-"`
	// Facets are the use-case parts of the observation the graph gives apart from the change and its nodes, by name:
	// what a view of a flow does not carry, replayed at At. The graph fills the built-in ones (options, active
	// option, decision points: FacetOptions, FacetActiveOption, FacetDecisionPoints, ADR 0009, 0032) and the
	// providers set on it (Graph.Facets); readers use the typed accessors (OptionsOf, ActiveOptionOf,
	// DecisionPointsOf, Facet) rather than the map.
	Facets map[string]any `json:"-"`
	At     time.Time      `json:"at,omitempty"`
}

// Names of the built-in facets of a blackboard.
const (
	FacetOptions        = "options"        // []Flow: the options of the change (ADR 0032 §6)
	FacetActiveOption   = "activeOption"   // string: the option the change works on
	FacetDecisionPoints = "decisionPoints" // []DecisionPoint: replayed at Blackboard.At (ADR 0009 §4)
	// FacetCriticalityPolicy is what the organisation of the change requires of its criticality (ADR 0075 §3), set by a
	// provider of Graph.Facets that reads the organisation; opaque to the graph. Absent: the compiled-in table.
	FacetCriticalityPolicy = "criticalityPolicy"
)

// Facet is the facet of the blackboard with a name, as the type the caller expects (the zero value when absent or of
// another type).
func Facet[T any](bb Blackboard, name string) T {
	v, _ := bb.Facets[name].(T)
	return v
}

// WithFacet is the blackboard with a facet set or replaced; the facets of the original are not changed.
func (bb Blackboard) WithFacet(name string, v any) Blackboard {
	f := make(map[string]any, len(bb.Facets)+1)
	maps.Copy(f, bb.Facets)
	f[name] = v
	bb.Facets = f
	return bb
}

// OptionsOf are the options of the change.
func OptionsOf(bb Blackboard) []Flow { return Facet[[]Flow](bb, FacetOptions) }

// ActiveOptionOf is the option the change works on ("" when none).
func ActiveOptionOf(bb Blackboard) string { return Facet[string](bb, FacetActiveOption) }

// DecisionPointsOf are the decision points of the change, replayed at bb.At.
func DecisionPointsOf(bb Blackboard) []DecisionPoint {
	return Facet[[]DecisionPoint](bb, FacetDecisionPoints)
}

// TypesOf returns a type followed by its supertypes.
func (bb Blackboard) TypesOf(t string) []any {
	out := []any{t}
	for _, s := range bb.Supertypes[t] {
		out = append(out, s)
	}
	return out
}

// ReferencedNodes lists every domain reference held by the change impacts.
func (c *Change) ReferencedNodes() []NodeRef {
	seen := map[NodeRef]bool{}
	var out []NodeRef
	add := func(r *NodeRef) {
		if r == nil || r.IsZero() || seen[*r] {
			return
		}
		seen[*r] = true
		out = append(out, *r)
	}
	for _, cn := range c.Nodes {
		add(cn.Pre)
		add(cn.Post)
		add(cn.Landed)
	}
	return out
}
