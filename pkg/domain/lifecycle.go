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
// stores its state. Every state is landable unless it opts out (NotLandable, ADR 0078): a
// change can only land when each node it holds ends in a landable state. A node is edited
// in any state while it is in a change.
type Lifecycle struct {
	// Name identifies the lifecycle in its domain; node types refer to it.
	Name        string           `yaml:"name" json:"name"`
	Description string           `yaml:"description,omitempty" json:"description,omitempty"`
	Initial     string           `yaml:"initial" json:"initial"`
	States      []LifecycleState `yaml:"states" json:"states"`
	Transitions []Transition     `yaml:"transitions,omitempty" json:"transitions,omitempty"`
}

// LifecycleState is one state of a lifecycle.
type LifecycleState struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// NotLandable opts the state out of landing (ADR 0078): a change cannot land while a node it holds rests
	// in it (a draft to finish, a review to pass). Every other state is landable.
	NotLandable bool `yaml:"notLandable,omitempty" json:"notLandable,omitempty"`
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

// Landable tells whether a node in the state may land with its change. Without a lifecycle, or with no
// state (or one the lifecycle does not know), a node is always landable.
func (l *Lifecycle) Landable(state string) bool {
	if l == nil || state == "" {
		return true
	}
	s, ok := l.State(state)
	return !ok || !s.NotLandable
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
	landable := 0
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
		if !s.NotLandable {
			landable++
		} else if s.Final {
			out = append(out, fmt.Sprintf("state %s is final and not landable: a node there could never land", s.Name))
		}
	}
	if len(l.States) == 0 {
		return append(out, "at least one state required")
	}
	if !names[l.Initial] {
		out = append(out, fmt.Sprintf("initial state %q is not a state", l.Initial))
	}
	if landable == 0 {
		out = append(out, "at least one landable state required (every state is flagged notLandable)")
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
	// a state that cannot land must be able to reach one that can
	for _, s := range l.States {
		if !s.NotLandable || !names[s.Name] {
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
				if st, _ := l.State(n); !st.NotLandable {
					ok = true
					break
				}
				todo = append(todo, n)
			}
		}
		if !ok {
			out = append(out, fmt.Sprintf("state %s is not landable and cannot reach a landable state", s.Name))
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
	c := &Lifecycle{Name: l.Name, Description: l.Description, Initial: l.Initial, States: slices.Clone(l.States), Transitions: slices.Clone(l.Transitions)}
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
