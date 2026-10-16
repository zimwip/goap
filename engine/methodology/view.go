package methodology

import (
	"slices"
	"strings"
)

// View is the compiled shape of the activities of a methodology as they are shown (ADR 0126 step 3): the processes
// and the methods, their step trees with entry and exit conditions, roles and guidance, and what each condition named
// there means, which actions produce it and which conditions it cannot hold without. It holds no value of a change:
// the engine binds it to a change (the conditions evaluated, the runs laid over it, the rights of the caller) and the
// editors show it as it is. It is computed once per compiled methodology, and the flow of an activity (ProcessGraph)
// is a projection of it.
type View struct {
	Methodology string `json:"methodology"`
	Version     string `json:"version,omitempty"`
	// Goal is the main goal of the methodology (ADR 0096).
	Goal      string         `json:"goal,omitempty"`
	Processes []ActivityView `json:"processes"`
	// Methods are the methods (ADR 0050), with their steps when they compose some; a method the compiler refused has
	// no goal and is never chosen.
	Methods []ActivityView `json:"methods,omitempty"`
	// Conditions describes every condition an entry, an exit or a goal of the view names, and their prerequisites.
	Conditions map[string]ConditionView `json:"conditions,omitempty"`
}

// Kinds of activity of a view.
const (
	ActivityProcess = "process"
	ActivityMethod  = "method"
)

// ActivityView is a process or a method of the view.
type ActivityView struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	// Agent and Goal run the activity as a whole; Done is what its goal asks.
	Agent string          `json:"agent,omitempty"`
	Goal  string          `json:"goal,omitempty"`
	Done  map[string]bool `json:"done,omitempty"`
	// Nested: a process another step of the methodology nests (shown in that step); Reactive: the activity or one of
	// its steps runs again on events (ADR 0110).
	Nested   bool `json:"nested,omitempty"`
	Reactive bool `json:"reactive,omitempty"`
	// For is the capability a method provides, Roles the roles it assigns, Actions the pool of actions it owns.
	For     string            `json:"for,omitempty"`
	Roles   *Responsibilities `json:"roles,omitempty"`
	Actions []string          `json:"actions,omitempty"`
	// References are the reference documents describing the activity.
	References []Reference `json:"references,omitempty"`
	Steps      []StepView  `json:"steps,omitempty"`
}

// StepView is a step of an activity of the view, with its sub-steps.
type StepView struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Owner string `json:"owner"`
	// Method: steps, action, agent, process, capability or manual; Target what it runs (action names, the nested
	// process, the capability).
	Method     string `json:"method"`
	Target     string `json:"target,omitempty"`
	Capability string `json:"capability,omitempty"`
	// Process is the process a step nests ("<process>" or "<methodology>/<process>").
	Process string `json:"process,omitempty"`
	// Foreach and GroupBy: the step runs once per element (or group) of the list (ADR 0050).
	Foreach string `json:"foreach,omitempty"`
	GroupBy string `json:"groupBy,omitempty"`
	// Review: the step is a review of a subject, automatic when it starts by itself (ADR 0130).
	Review *Review `json:"review,omitempty"`
	// Gate: the step is a gate of its process (ADR 0132).
	Gate        *Gate       `json:"gate,omitempty"`
	Description string      `json:"description,omitempty"`
	Guidance    string      `json:"guidance,omitempty"`
	Checklist   []string    `json:"checklist,omitempty"`
	References  []Reference `json:"references,omitempty"`
	// Roles are the roles in force for the step.
	Roles *Responsibilities `json:"roles,omitempty"`
	Entry map[string]bool   `json:"entry,omitempty"`
	Exit  map[string]bool   `json:"exit,omitempty"`
	// Planned are the actions whose runs are the runs of the step.
	Planned []string `json:"planned,omitempty"`
	// Unavailable, for a step naming a capability, are the context conditions of the methods providing it: what one
	// of them needs when none applies.
	Unavailable []string   `json:"unavailable,omitempty"`
	Steps       []StepView `json:"steps,omitempty"`
}

// ConditionView is what a condition means, the actions making it hold and the conditions it cannot hold without.
type ConditionView struct {
	Description string   `json:"description,omitempty"`
	ProducedBy  []string `json:"producedBy,omitempty"`
	Requires    []string `json:"requires,omitempty"`
}

// Process is the process of that name in the view.
func (v *View) Process(name string) (ActivityView, bool) {
	for _, p := range v.Processes {
		if p.Name == name {
			return p, true
		}
	}
	return ActivityView{}, false
}

// Method is the method of that name in the view.
func (v *View) Method(name string) (ActivityView, bool) {
	for _, me := range v.Methods {
		if me.Name == name {
			return me, true
		}
	}
	return ActivityView{}, false
}

// Condition is what the view says of a condition (nothing when it names none).
func (v *View) Condition(name string) ConditionView { return v.Conditions[name] }

// View is the view of the activities of the methodology (computed at compilation).
func (c *Compiled) View() *View {
	if c.view != nil {
		return c.view
	}
	return c.buildView()
}

func (c *Compiled) buildView() *View {
	v := &View{Methodology: c.Name(), Version: c.Version(), Goal: c.MainGoal(), Processes: []ActivityView{}, Conditions: map[string]ConditionView{}}
	reactive := map[string]bool{}
	for _, r := range c.Reactions() {
		reactive[r.Owner] = true
	}
	agents := map[string]string{}
	for _, ag := range c.AgentList() {
		if p := ag.ProcessOf(); p != "" {
			if _, ok := agents[p]; !ok {
				agents[p] = ag.Name
			}
		}
	}
	for _, p := range c.Processes() {
		a := ActivityView{Name: p.Name, Kind: ActivityProcess, Description: p.Description, Agent: agents[p.Name], Nested: p.Nested,
			Reactive: reactive[p.Name], Steps: c.stepViews(c.ProcessSteps(p.Name))}
		if def, ok := c.definition.ProcessByName(p.Name); ok {
			a.References = def.References
		}
		if g, ok := c.Goal(p.Name); ok {
			a.Goal, a.Done = g.Name, g.Pre
		}
		v.Processes = append(v.Processes, a)
	}
	for _, me := range c.definition.Methods {
		a := ActivityView{Name: me.Name, Kind: ActivityMethod, Description: me.Description, Agent: me.ActorAgent(), Reactive: reactive[me.Name],
			For: me.For, Roles: me.Roles, Actions: me.Actions, References: me.References, Steps: c.stepViews(c.MethodSteps(me.Name))}
		if g, ok := c.methods.goals[me.Name]; ok {
			a.Goal, a.Done = g.Name, g.Pre
		}
		v.Methods = append(v.Methods, a)
	}
	// the conditions named, and their prerequisites
	var todo []string
	note := func(m map[string]bool) {
		for k := range m {
			todo = append(todo, k)
		}
	}
	var walk func([]StepView)
	walk = func(steps []StepView) {
		for _, s := range steps {
			note(s.Entry)
			note(s.Exit)
			todo = append(todo, s.Unavailable...)
			walk(s.Steps)
		}
	}
	for _, list := range [][]ActivityView{v.Processes, v.Methods} {
		for _, a := range list {
			note(a.Done)
			walk(a.Steps)
		}
	}
	for len(todo) > 0 {
		name := strings.TrimPrefix(todo[len(todo)-1], "!")
		todo = todo[:len(todo)-1]
		if _, seen := v.Conditions[name]; seen {
			continue
		}
		cv := ConditionView{Description: c.ConditionDescription(name), ProducedBy: c.Producers(name), Requires: c.Prerequisites(name)}
		v.Conditions[name] = cv
		todo = append(todo, cv.Requires...)
	}
	return v
}

func (c *Compiled) stepViews(steps []StepInfo) []StepView {
	if len(steps) == 0 {
		return nil
	}
	out := make([]StepView, 0, len(steps))
	for _, s := range steps {
		sv := StepView{Path: s.Path, Name: s.Name, Owner: s.Owner, Method: s.Method(), Capability: s.Capability, Process: s.Process,
			Foreach: s.Foreach, GroupBy: s.GroupBy, Review: s.Review, Gate: s.Gate, Description: s.Description, Guidance: s.Guidance, Checklist: s.Checklist,
			References: s.References, Roles: s.Effective, Entry: s.Entry, Exit: s.Exit, Planned: slices.Clone(s.Planned), Steps: c.stepViews(s.Steps)}
		switch sv.Method {
		case MethodAction:
			sv.Target = s.Action
			if sv.Target == "" {
				sv.Target = strings.Join(s.Actions, " | ")
			}
		case MethodProcess:
			sv.Target = s.Process
		case MethodCapability:
			sv.Target = s.Capability
			sv.Unavailable = c.CapabilityGuards(s.Capability)
		}
		out = append(out, sv)
	}
	return out
}
