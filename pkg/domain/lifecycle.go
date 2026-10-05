package domain

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/algo"
)

// Lifecycle is a named state machine of a domain (ADR 0014). Node types refer
// to it by name (NodeType.Lifecycle), inherit it through extends (a subtype that
// names its own lifecycle replaces the inherited one), and every node version
// stores its state. A state is either editable or not: an editable state is normally
// a working state that a node only holds through a change it is attached to, so a
// change can only be applied when its nodes end in a non-editable state — unless the
// lifecycle itself declares RestInEditable (ADR 0048), for a type whose editable
// states are ordinary long-lived statuses (not a review draft) rather than a document
// workflow's checkout state.
type Lifecycle struct {
	// Name identifies the lifecycle in its domain; node types refer to it.
	Name        string           `yaml:"name" json:"name"`
	Description string           `yaml:"description,omitempty" json:"description,omitempty"`
	Initial     string           `yaml:"initial" json:"initial"`
	States      []LifecycleState `yaml:"states" json:"states"`
	// RestInEditable allows a change to apply with a node of this lifecycle left in an editable state (ADR
	// 0048): the type's editable states are ordinary statuses a node may rest in indefinitely, not a draft a
	// change must move the node out of before landing. Default false preserves ADR 0014's original invariant
	// for every lifecycle declared before this field existed.
	RestInEditable bool         `yaml:"restInEditable,omitempty" json:"restInEditable,omitempty"`
	Transitions    []Transition `yaml:"transitions,omitempty" json:"transitions,omitempty"`
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
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	From        string `yaml:"from" json:"from"`
	To          string `yaml:"to" json:"to"`
	// Permission ("type:action") the actor must hold; empty: node:transition.
	Permission string `yaml:"permission,omitempty" json:"permission,omitempty"`
	// Guard is a CEL predicate over the node (node.props, node.state) and its
	// contained children (children), evaluated when the change is applied.
	Guard string `yaml:"guard,omitempty" json:"guard,omitempty"`
	// Vetos and Objectives split the criteria of a gate of the lifecycle of a change (ADR 0075 §3, ADR 0058), evaluated
	// in the environment of Guard: a veto not met blocks the transition, whatever else holds; an objective not met
	// blocks it too unless a derogation in force names it (its rule is the name of the objective), when the transition
	// goes with reserve. They are two lists of the guard, not another kind of gate.
	Vetos      []Criterion `yaml:"vetos,omitempty" json:"vetos,omitempty"`
	Objectives []Criterion `yaml:"objectives,omitempty" json:"objectives,omitempty"`
	// Guards are algorithm instances of type transition_guard (ADR 0018), run
	// in order after the CEL guard; all must accept.
	Guards []string `yaml:"guards,omitempty" json:"guards,omitempty"`
	// Actions are algorithm instances of type transition_action, run in order
	// once the transition is accepted; they may change properties of the node.
	Actions []string `yaml:"actions,omitempty" json:"actions,omitempty"`
	// GuardAlgos and ActionAlgos are the instances resolved with their algorithm.
	// They are filled when the type catalogue resolves the lifecycle (never authored),
	// so that evaluating a change needs no other lookup.
	GuardAlgos  []algo.Bound       `yaml:"-" json:"guardAlgos,omitempty"`
	ActionAlgos []algo.Bound       `yaml:"-" json:"actionAlgos,omitempty"`
	Requires    TransitionRequires `yaml:"requires,omitempty" json:"requires,omitempty"`
	// Children (documents): every contained child must be in one of these
	// states for the transition to be accepted. Validation only, no cascade.
	Children *ChildrenRule `yaml:"children,omitempty" json:"children,omitempty"`
}

// Criterion is one criterion of a gate (ADR 0075 §3): a named CEL predicate over the same variables as a guard.
type Criterion struct {
	Name        string `yaml:"name" json:"name"`
	Expr        string `yaml:"expr" json:"expr"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// GateResult is what the criteria of a gate came to for a change.
type GateResult struct {
	// Vetoed are the vetos not met: any blocks the transition.
	Vetoed []string `json:"vetoed,omitempty"`
	// Unmet are the objectives not met, covered or not.
	Unmet []string `json:"unmet,omitempty"`
	// Reserve are the keys of the records (derogations in force) that cover the unmet objectives, by objective name:
	// the transition goes with reserve.
	Reserve map[string]string `json:"reserve,omitempty"`
}

// Uncovered are the objectives not met that nothing covers.
func (r GateResult) Uncovered() []string {
	var out []string
	for _, n := range r.Unmet {
		if _, ok := r.Reserve[n]; !ok {
			out = append(out, n)
		}
	}
	return out
}

// Passed reports whether the transition may go: no veto is unmet and every unmet objective is covered.
func (r GateResult) Passed() bool { return len(r.Vetoed) == 0 && len(r.Uncovered()) == 0 }

// WithReserve reports whether the transition goes although objectives are unmet.
func (r GateResult) WithReserve() bool { return len(r.Vetoed) == 0 && len(r.Unmet) > 0 && r.Passed() }

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

// LinkContains is the name of the link type between a document and the nodes it embeds (declared by the domain of
// the document: alm@contains).
const LinkContains = "contains"

// IsContains reports a document link: a link type named LinkContains, bare or qualified.
func IsContains(linkType string) bool {
	return linkType == LinkContains || strings.HasSuffix(linkType, TypeSep+LinkContains)
}

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
	} else if s, _ := l.State(l.Initial); !s.Editable {
		// a node is created checked out and filled in its first version (ADR 0076): it is born editable
		out = append(out, fmt.Sprintf("initial state %q is not editable (a created node is written in its initial state)", l.Initial))
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
	c := &Lifecycle{Name: l.Name, Description: l.Description, Initial: l.Initial, States: slices.Clone(l.States), RestInEditable: l.RestInEditable, Transitions: slices.Clone(l.Transitions)}
	for i, t := range c.Transitions {
		c.Transitions[i].Guards, c.Transitions[i].Actions = slices.Clone(t.Guards), slices.Clone(t.Actions)
		c.Transitions[i].GuardAlgos, c.Transitions[i].ActionAlgos = slices.Clone(t.GuardAlgos), slices.Clone(t.ActionAlgos)
		c.Transitions[i].Requires = TransitionRequires{Attributes: slices.Clone(t.Requires.Attributes), OutgoingLinks: slices.Clone(t.Requires.OutgoingLinks)}
		if t.Children != nil {
			c.Transitions[i].Children = &ChildrenRule{States: slices.Clone(t.Children.States)}
		}
	}
	return c
}
