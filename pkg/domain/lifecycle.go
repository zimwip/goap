package domain

import (
	"fmt"
	"slices"
)

// Lifecycle is a named state machine of a domain (ADR 0014). Node types refer
// to it by name (NodeType.Lifecycle), inherit it through extends (a subtype that
// names its own lifecycle replaces the inherited one), and every node version
// stores its state. A state is either editable or not: an editable state is a working
// state that a node only holds through a change it is attached to, so a change
// can only be applied when its nodes end in a non-editable state.
type Lifecycle struct {
	// Name identifies the lifecycle in its domain; node types refer to it.
	Name        string           `yaml:"name" json:"name"`
	Initial     string           `yaml:"initial" json:"initial"`
	States      []LifecycleState `yaml:"states" json:"states"`
	Transitions []Transition     `yaml:"transitions,omitempty" json:"transitions,omitempty"`
}

// LifecycleState is one state of a lifecycle.
type LifecycleState struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Editable states allow the node to be modified (through a change).
	Editable bool `yaml:"editable,omitempty" json:"editable,omitempty"`
	// Final states have no way out.
	Final bool `yaml:"final,omitempty" json:"final,omitempty"`
}

// Transition moves a node from one state to another.
type Transition struct {
	Name string `yaml:"name" json:"name"`
	From string `yaml:"from" json:"from"`
	To   string `yaml:"to" json:"to"`
	// Permission ("type:action") the actor must hold; empty: node:transition.
	Permission string `yaml:"permission,omitempty" json:"permission,omitempty"`
	// Guard is a CEL predicate over the node (node.props, node.state) and its
	// contained children (children), evaluated when the change is applied.
	Guard    string             `yaml:"guard,omitempty" json:"guard,omitempty"`
	Requires TransitionRequires `yaml:"requires,omitempty" json:"requires,omitempty"`
	// Children (documents): every contained child must be in one of these
	// states for the transition to be accepted. Validation only, no cascade.
	Children *ChildrenRule `yaml:"children,omitempty" json:"children,omitempty"`
}

// TransitionRequires lists what the node must have to take a transition.
type TransitionRequires struct {
	Attributes    []string `yaml:"attributes,omitempty" json:"attributes,omitempty"`
	OutgoingLinks []string `yaml:"outgoingLinks,omitempty" json:"outgoingLinks,omitempty"`
}

// ChildrenRule constrains the state of the children of a document.
type ChildrenRule struct {
	States []string `yaml:"states" json:"states"`
}

// DocumentSpec makes a node type a document: it embeds nodes of the listed
// types through outgoing LinkContains links.
type DocumentSpec struct {
	Contains []string `yaml:"contains" json:"contains"`
}

// LinkContains is the link type between a document and the nodes it embeds.
const LinkContains = "contains"

// State returns the named state.
func (l *Lifecycle) State(name string) (LifecycleState, bool) {
	if l == nil {
		return LifecycleState{}, false
	}
	for _, s := range l.States {
		if s.Name == name {
			return s, true
		}
	}
	return LifecycleState{}, false
}

// Editable tells whether the state is an editable one. Without a lifecycle a
// node is always editable (through a change).
func (l *Lifecycle) Editable(state string) bool {
	if l == nil {
		return true
	}
	s, ok := l.State(state)
	return ok && s.Editable
}

// Transition finds the transition named name out of the state from.
func (l *Lifecycle) Transition(from, name string) (Transition, bool) {
	if l == nil {
		return Transition{}, false
	}
	for _, t := range l.Transitions {
		if t.From == from && t.Name == name {
			return t, true
		}
	}
	return Transition{}, false
}

// Move finds the transition from one state to another (the first declared).
func (l *Lifecycle) Move(from, to string) (Transition, bool) {
	if l == nil {
		return Transition{}, false
	}
	for _, t := range l.Transitions {
		if t.From == from && t.To == to {
			return t, true
		}
	}
	return Transition{}, false
}

// Issues checks the lifecycle and returns one message per problem.
func (l *Lifecycle) Issues() []string {
	if l == nil {
		return nil
	}
	var out []string
	names := map[string]bool{}
	editable, fixed := 0, 0
	for _, s := range l.States {
		switch {
		case s.Name == "":
			out = append(out, "a state has no name")
			continue
		case names[s.Name]:
			out = append(out, fmt.Sprintf("duplicate state %s", s.Name))
			continue
		}
		names[s.Name] = true
		if s.Editable {
			editable++
			if s.Final {
				out = append(out, fmt.Sprintf("state %s is final and editable: it could never leave the change", s.Name))
			}
		} else {
			fixed++
		}
	}
	if len(l.States) == 0 {
		return append(out, "at least one state required")
	}
	if !names[l.Initial] {
		out = append(out, fmt.Sprintf("initial state %q is not a state", l.Initial))
	}
	if editable == 0 {
		out = append(out, "at least one editable state required (nodes are modified in an editable state)")
	}
	if fixed == 0 {
		out = append(out, "at least one non-editable state required (a change ends with its nodes out of the editable states)")
	}
	seen := map[[2]string]bool{}
	next := map[string][]string{}
	for i, t := range l.Transitions {
		switch {
		case t.Name == "":
			out = append(out, fmt.Sprintf("transition %d has no name", i))
		case !names[t.From]:
			out = append(out, fmt.Sprintf("transition %s: unknown state %q", t.Name, t.From))
		case !names[t.To]:
			out = append(out, fmt.Sprintf("transition %s: unknown state %q", t.Name, t.To))
		case seen[[2]string{t.From, t.Name}]:
			out = append(out, fmt.Sprintf("duplicate transition %s from %s", t.Name, t.From))
		default:
			seen[[2]string{t.From, t.Name}] = true
			next[t.From] = append(next[t.From], t.To)
			if s, _ := l.State(t.From); s.Final {
				out = append(out, fmt.Sprintf("transition %s leaves the final state %s", t.Name, t.From))
			}
		}
		if t.Children != nil {
			for _, st := range t.Children.States {
				if st == "" {
					out = append(out, fmt.Sprintf("transition %s: empty child state", t.Name))
				}
			}
			if len(t.Children.States) == 0 {
				out = append(out, fmt.Sprintf("transition %s: children needs at least one state", t.Name))
			}
		}
	}
	// an editable state must be able to reach a non-editable one
	for _, s := range l.States {
		if !s.Editable || !names[s.Name] {
			continue
		}
		reach, todo := map[string]bool{s.Name: true}, []string{s.Name}
		ok := false
		for len(todo) > 0 && !ok {
			cur := todo[0]
			todo = todo[1:]
			for _, n := range next[cur] {
				if reach[n] {
					continue
				}
				reach[n] = true
				if st, _ := l.State(n); !st.Editable {
					ok = true
					break
				}
				todo = append(todo, n)
			}
		}
		if !ok {
			out = append(out, fmt.Sprintf("editable state %s cannot reach a non-editable state", s.Name))
		}
	}
	return out
}

// StateNames lists the states.
func (l *Lifecycle) StateNames() []string {
	if l == nil {
		return nil
	}
	out := make([]string, 0, len(l.States))
	for _, s := range l.States {
		out = append(out, s.Name)
	}
	return out
}

// Clone returns a deep copy.
func (l *Lifecycle) Clone() *Lifecycle {
	if l == nil {
		return nil
	}
	c := &Lifecycle{Name: l.Name, Initial: l.Initial, States: slices.Clone(l.States), Transitions: slices.Clone(l.Transitions)}
	for i, t := range c.Transitions {
		c.Transitions[i].Requires = TransitionRequires{Attributes: slices.Clone(t.Requires.Attributes), OutgoingLinks: slices.Clone(t.Requires.OutgoingLinks)}
		if t.Children != nil {
			c.Transitions[i].Children = &ChildrenRule{States: slices.Clone(t.Children.States)}
		}
	}
	return c
}
