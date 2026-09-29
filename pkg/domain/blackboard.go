package domain

import "time"

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
	// Options are the options of the change and ActiveOption the one it works on (ADR 0032 §6): the view of a flow
	// carries no flow event, so the graph gives them apart. DecisionPoints are replayed at At (ADR 0009 §4).
	Options        []Flow          `json:"options,omitempty"`
	ActiveOption   string          `json:"activeOption,omitempty"`
	DecisionPoints []DecisionPoint `json:"decisionPoints,omitempty"`
	At             time.Time       `json:"at,omitempty"`
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
