package methodology

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Event is an event a methodology declares (ADR 0110), in addition to the standard events (events.All). It is raised
// by a signal item of its name on a change (domain.KindSignal, Type = Name): an action declaring it in emits, a person,
// a script; the engine then publishes it as an event of that name, with the signal items in event.items.
type Event struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// EventNameRE is the form of the name of a declared event: lower case words separated by dots.
var EventNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z0-9_-]+)*$`)

// reservedEventFamilies are the prefixes of the standard events: a declared event takes none of them.
var reservedEventFamilies = []string{"change.", "process.", "step.", "methodology."}

// StandardEvent reports whether the event is one of the platform's (events.All).
func StandardEvent(name string) bool { return slices.Contains(TriggerEvents, name) }

// Declares reports whether the methodology declares the event.
func (m *Methodology) Declares(name string) bool {
	return slices.ContainsFunc(m.Events, func(e Event) bool { return e.Name == name })
}

// KnowsEvent reports whether a subscription or a trigger of the methodology may follow the event: a standard event or
// one it declares.
func (m *Methodology) KnowsEvent(name string) bool { return StandardEvent(name) || m.Declares(name) }

// EventNames are the events the methodology may follow: the standard ones, then its own.
func (m *Methodology) EventNames() []string {
	out := slices.Clone(TriggerEvents)
	for _, e := range m.Events {
		out = append(out, e.Name)
	}
	return out
}

// checkEvents checks the declared events (a name of the right form, none of the standard families, no duplicate) and
// the emits of the actions (declared events).
func (s *compileState) checkEvents() {
	m := s.m
	seen := map[string]bool{}
	for i, e := range m.Events {
		path := fmt.Sprintf("events[%d].name", i)
		switch {
		case !EventNameRE.MatchString(e.Name):
			s.add(path, "an event name is lower case words separated by dots (%s)", EventNameRE)
		case slices.ContainsFunc(reservedEventFamilies, func(p string) bool { return strings.HasPrefix(e.Name, p) }):
			s.add(path, "%q takes a prefix of the standard events (%s): name it after the methodology's own vocabulary", e.Name, strings.Join(reservedEventFamilies, " "))
		case seen[e.Name]:
			s.add(path, "event %q is declared twice", e.Name)
		}
		seen[e.Name] = true
	}
	for i, a := range m.Actions {
		for j, name := range a.Emits {
			if !m.Declares(name) {
				s.add(fmt.Sprintf("actions[%d].emits[%d]", i, j), "unknown event %q: an action emits an event of the methodology (events)", name)
			}
		}
	}
}

// checkEvent checks the event a subscription or a trigger follows: a standard event or one of the methodology.
func (s *compileState) checkEvent(path, name string) {
	if !s.m.KnowsEvent(name) {
		s.add(path, "unknown event %q: one of the standard events (%s) or an event the methodology declares (events)", name, strings.Join(TriggerEvents, ", "))
	}
}

// PathSubscription is a subscription of a methodology with the path of its definition.
type PathSubscription struct {
	Path string
	Sub  Subscription
}

// AllSubscriptions lists the subscriptions of the methodology: its own on (a transverse methodology, the events of
// related changes), then those of its reactive activities (processes and their steps, methods and their steps,
// actions, ADR 0110).
func (m *Methodology) AllSubscriptions() []PathSubscription {
	var out []PathSubscription
	add := func(path string, subs []Subscription) {
		for i, sub := range subs {
			out = append(out, PathSubscription{Path: fmt.Sprintf("%son[%d]", path, i), Sub: sub})
		}
	}
	add("", m.On)
	var steps func(list []Step, path string)
	steps = func(list []Step, path string) {
		for i, st := range list {
			sp := fmt.Sprintf("%s.steps[%d]", path, i)
			add(sp+".", st.On)
			steps(st.Steps, sp)
		}
	}
	for i, p := range m.Processes {
		path := fmt.Sprintf("processes[%d]", i)
		add(path+".", p.On)
		steps(p.Steps, path)
	}
	for i, me := range m.Methods {
		path := fmt.Sprintf("methods[%d]", i)
		add(path+".", me.On)
		steps(me.Steps, path)
	}
	for i, a := range m.Actions {
		add(fmt.Sprintf("actions[%d].", i), a.On)
	}
	return out
}
