package methodology

import (
	"fmt"
	"maps"
	"strings"

	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/lib/def"
)

// The kinds of activity that may react to events (ADR 0110).
const (
	ReactProcess = "process"
	ReactStep    = "step"
	ReactMethod  = "method"
	ReactAction  = "action"
)

// ReactionPrefix starts the name of the agent and the goal generated for a reactive action: "on.<action>".
const ReactionPrefix = "on."

// ReactionAgent is the name of the agent (and of its goal) generated to run a reactive action alone.
func ReactionAgent(action string) string { return ReactionPrefix + action }

// Reaction is an activity of the methodology that declares `on:` (ADR 0110): on each matching event of a change of the
// methodology, the engine runs Agent towards Goal on that change, as a companion run kept per change and activity.
type Reaction struct {
	// Activity is the path of the activity: a process or method name, a step path, an action name.
	Activity string
	Kind     string
	Agent    string
	Goal     string
	On       []Subscription
	// own are the actions whose items are the reaction's own: they do not wake it again.
	own map[string]bool
}

// Owns reports whether an item produced by the action of that name is the reaction's own.
func (r Reaction) Owns(producedBy string) bool { return producedBy != "" && r.own[producedBy] }

// Reactions returns the reactive activities of the methodology, in declaration order (processes and their steps,
// methods and their steps, actions).
func (c *Compiled) Reactions() []Reaction { return c.reactions }

// compileReactions checks the `on:` of the activities and builds the reactions, with the agent of each reactive
// action (ADR 0110).
func (s *compileState) compileReactions() {
	m := s.m
	if len(m.AppliesTo) > 0 {
		// a transverse methodology runs alongside the changes of others: it subscribes at its own level
		s.forEachReactive(func(path string, _ Reaction) {
			s.add(path+".on", "a transverse methodology subscribes at its own level (on of the methodology)")
		})
		return
	}
	s.forEachReactive(func(path string, r Reaction) {
		for i, sub := range r.On {
			s.checkSubscription(fmt.Sprintf("%s.on[%d]", path, i), sub)
		}
		switch r.Kind {
		case ReactAction:
			a := s.actions[r.Activity]
			switch {
			case a.IsSpecialization():
				s.add(path+".on", "a specialization is not planned: put on on the action it specializes")
				return
			case len(a.Effects) == 0:
				s.add(path+".on", "a reactive action declares the effects it reaches (its goal when an event wakes it)")
				return
			}
			s.agents[r.Agent] = Agent{Name: r.Agent, Description: a.Description, Planner: PlannerGOAP, Actions: []string{a.Name}, Goals: []string{r.Goal}}
		case ReactStep:
			cr := s.procs.criteria[r.Activity]
			if len(cr.done) == 1 && cr.done[StepCondition(r.Activity)] {
				s.issues = append(s.issues, def.Issue{Path: path + ".done", Severity: def.SeverityWarning,
					Message: "a reactive step whose exit is only its own run holds once it ran: the events wake it for nothing",
					Hint:    "state a done derived from what the events change"})
			}
		}
		r.own = s.ownActions(r)
		s.reactions = append(s.reactions, r)
	})
}

// forEachReactive calls f for each activity declaring on, with the path of its definition.
func (s *compileState) forEachReactive(f func(path string, r Reaction)) {
	m := s.m
	var steps func(list []Step, path, prefix, agent string)
	steps = func(list []Step, path, prefix, agent string) {
		for i, st := range list {
			sp, full := fmt.Sprintf("%s.steps[%d]", path, i), prefix+"/"+st.Name
			if len(st.On) > 0 {
				f(sp, Reaction{Activity: full, Kind: ReactStep, Agent: agent, Goal: full, On: st.On})
			}
			steps(st.Steps, sp, full, agent)
		}
	}
	for i, p := range m.Processes {
		path := fmt.Sprintf("processes[%d]", i)
		if len(p.On) > 0 {
			f(path, Reaction{Activity: p.Name, Kind: ReactProcess, Agent: p.Name, Goal: p.Name, On: p.On})
		}
		steps(p.Steps, path, p.Name, p.Name)
	}
	for i, me := range m.Methods {
		path := fmt.Sprintf("methods[%d]", i)
		if len(me.On) > 0 {
			f(path, Reaction{Activity: me.Name, Kind: ReactMethod, Agent: me.ActorAgent(), Goal: me.Name, On: me.On})
		}
		steps(me.Steps, path, me.Name, me.ActorAgent())
	}
	for i, a := range m.Actions {
		if len(a.On) > 0 {
			f(fmt.Sprintf("actions[%d]", i), Reaction{Activity: a.Name, Kind: ReactAction, Agent: ReactionAgent(a.Name), Goal: ReactionAgent(a.Name), On: a.On})
		}
	}
}

// checkSubscription checks one subscription of an activity: a known event, a filter that compiles, a known origin.
func (s *compileState) checkSubscription(path string, sub Subscription) {
	s.checkEvent(path+".event", sub.Event)
	if _, err := condition.CompileEventFilter(sub.Filter); err != nil {
		s.add(path+".filter", "%v", err)
	}
	switch sub.From {
	case "", FromSelf, FromIntegrator, FromDependency:
	case FromIntegrated:
		if len(s.m.Integrates) == 0 {
			s.add(path+".from", "from %q needs integrates: the methodology integrates no change", FromIntegrated)
		}
	default:
		s.add(path+".from", "from must be %s, %s, %s or %s", FromSelf, FromIntegrated, FromIntegrator, FromDependency)
	}
}

// ownActions are the actions a reaction runs: the actions generated for its steps (and the declared ones they
// implement, the pools of the methods providing their capabilities), the pool of a method, the action itself.
func (s *compileState) ownActions(r Reaction) map[string]bool {
	own := map[string]bool{r.Activity: true}
	if r.Kind == ReactAction {
		return own
	}
	for _, a := range s.procs.actions {
		if a.Step == r.Activity || strings.HasPrefix(a.Step, r.Activity+"/") {
			own[a.Name] = true
			if a.Implements != "" {
				own[a.Implements] = true
			}
			// a step naming a capability runs the methods providing it: their actions are the reaction's too
			if capability, _ := a.Params["capability"].(string); capability != "" {
				for _, me := range s.m.Methods {
					if me.For == capability {
						for _, x := range me.Actions {
							own[x] = true
						}
					}
				}
			}
		}
	}
	if r.Kind == ReactMethod {
		for _, me := range s.m.Methods {
			if me.Name == r.Activity {
				for _, a := range me.Actions {
					own[a] = true
				}
			}
		}
	}
	return own
}

// reactionGoal is the goal of a reactive action run alone: its effects.
func (c *Compiled) reactionGoal(name string) (Goal, bool) {
	action, ok := strings.CutPrefix(name, ReactionPrefix)
	if !ok {
		return Goal{}, false
	}
	for _, r := range c.reactions {
		if r.Kind == ReactAction && r.Activity == action {
			a := c.actions[action]
			return Goal{Name: name, Description: a.Description, Pre: maps.Clone(a.Effects)}, true
		}
	}
	return Goal{}, false
}
