package methodology

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
)

// Method is the documentary reference of how a step capability is carried out in a context (ADR 0035 §1): what is
// being worked on, which technology. It carries the guidance, the reference documents, the checklist and the
// deliverables of that way of working, and names its actor: either an agent of the methodology (and the goal it
// reaches), even when the method amounts to one action, or - composing its own Steps, exactly like a Process - an
// agent generated for it, of its own name (architecture plan "Activity concept": Method and MethodStep are
// Activities too). A step names a capability (`method: build`); at execution, the applicable method (When holds)
// with the highest Priority is chosen and its agent carries out the step as a sub-agent, with the method's guidance.
type Method struct {
	Name string `yaml:"name" json:"name"`
	// For is the capability the method provides; several methods provide one, in different contexts.
	For string `yaml:"for" json:"for"`
	// When is a CEL condition on the blackboard: the context where the method applies (empty: always).
	When     string `yaml:"when,omitempty" json:"when,omitempty"`
	Priority int    `yaml:"priority,omitempty" json:"priority,omitempty"`

	Description  string      `yaml:"description,omitempty" json:"description,omitempty"`
	Guidance     string      `yaml:"guidance,omitempty" json:"guidance,omitempty"`
	Checklist    []string    `yaml:"checklist,omitempty" json:"checklist,omitempty"`
	Deliverables []string    `yaml:"deliverables,omitempty" json:"deliverables,omitempty"`
	References   []Reference `yaml:"references,omitempty" json:"references,omitempty"`

	// Roles involved when the method is used (replacing those of the step, the method being more precise).
	Roles *Responsibilities `yaml:"roles,omitempty" json:"roles,omitempty"`

	// Agent is the actor: an agent of the methodology; Goal the goal it reaches (default: its only goal). Exactly
	// one of Agent or Steps is set.
	Agent string `yaml:"agent,omitempty" json:"agent,omitempty"`
	Goal  string `yaml:"goal,omitempty" json:"goal,omitempty"`
	// Steps composes the method from its own steps and sub-steps, done by actions, agents or nested processes -
	// the same shape and compilation as a Process's (not a capability dispatch: a method step may not itself name
	// a capability, to avoid methods resolving each other circularly while methods are still being compiled).
	// Compiling it generates an agent and a goal of the method's own name, exactly like a Process. Mutually
	// exclusive with Agent/Goal.
	Steps []Step `yaml:"steps,omitempty" json:"steps,omitempty"`
}

// ActorAgent is the agent that carries out the method: the one it names, or - composing its own Steps - the agent
// generated for it, of its own name.
func (m Method) ActorAgent() string {
	if m.Agent != "" {
		return m.Agent
	}
	return m.Name
}

// GuardCondition is the name of the condition of the method's context.
func (m Method) GuardCondition() string { return "method:" + m.Name }

// MethodCapability is the method of a step that names a capability.
const MethodCapability = "method"

// compiledMethods holds the validated methods: their goals and guards.
type compiledMethods struct {
	goals  map[string]Goal // by method name
	guards []condition.Definition
}

// compileMethods validates the methods: their names, capabilities, contexts and actors. A method composing its own
// Steps is compiled the same way a Process is (gen collects what that generates: its agent, its goal, the actions
// of its steps); unlike a process step, a method step may not itself name a capability (`method:`), since that
// would need every method already compiled to resolve - methods are not compiled yet while this runs.
func (m *Methodology) compileMethods(add func(path, format string, args ...any), actions map[string]Action, known map[string]bool, agents map[string]Agent, roles map[string]bool) (out compiledMethods, gen compiledProcesses) {
	out = compiledMethods{goals: map[string]Goal{}}
	gen = compiledProcesses{criteria: map[string]stepCriteria{}}
	w := &stepWalker{m: m, add: add, actions: actions, known: known, agents: agents, methods: compiledMethods{}, roles: roles, root: &gen, done: map[string]*processCriteria{}}
	seen := map[string]bool{}
	for i, me := range m.Methods {
		path := fmt.Sprintf("methods[%d]", i)
		hasAgent, hasSteps := me.Agent != "", len(me.Steps) > 0
		switch {
		case !nameRE.MatchString(me.Name):
			add(path+".name", "name required: lowercase letters, digits, '-' or '_', starting with a letter")
			continue
		case seen[me.Name]:
			add(path+".name", "duplicate method %s", me.Name)
			continue
		case hasSteps && agents[me.Name].Name != "":
			add(path+".name", "%s is already an agent: a method composing its own steps is run by an agent of its name", me.Name)
			continue
		case hasSteps && slices.ContainsFunc(m.Processes, func(p Process) bool { return p.Name == me.Name }):
			add(path+".name", "%s is already a process: a method composing its own steps is run by an agent of its name", me.Name)
			continue
		case hasSteps && slices.ContainsFunc(m.Goals, func(g Goal) bool { return g.Name == me.Name }):
			add(path+".name", "%s is already a goal: a method composing its own steps reaches a goal of its name", me.Name)
			continue
		}
		seen[me.Name] = true
		if !nameRE.MatchString(me.For) {
			add(path+".for", "for names the capability the method provides: lowercase letters, digits, '-' or '_'")
		}
		checkReferences(add, path+".references", me.References)
		checkResponsibilities(add, path+".roles", me.Roles, roles)
		if me.When != "" {
			d := condition.Definition{Name: me.GuardCondition(), Expr: me.When}
			if _, err := condition.Compile([]condition.Definition{d}); err != nil {
				add(path+".when", "%s", strings.TrimPrefix(err.Error(), fmt.Sprintf("condition %q: ", d.Name)))
			} else {
				out.guards = append(out.guards, d)
			}
		}
		switch {
		case hasAgent && hasSteps:
			add(path, "a method names its actor (agent) or composes its own steps, not both")
		case !hasAgent && !hasSteps:
			add(path+".agent", "a method names its actor: an agent of the methodology, or declares its own steps")
		case hasSteps:
			sub := *w
			sub.out = &compiledProcesses{}
			done := sub.walk(me.Steps, path+".steps", me.Name, nil)
			gen.conditions = append(gen.conditions, sub.out.conditions...)
			gen.actions = append(gen.actions, sub.out.actions...)
			if len(done) == 0 {
				continue
			}
			own := make([]string, 0, len(sub.out.actions))
			for _, a := range sub.out.actions {
				own = append(own, a.Name)
			}
			goal := Goal{Name: me.Name, Description: me.Description, Pre: done}
			gen.goals = append(gen.goals, goal)
			gen.agents = append(gen.agents, Agent{Name: me.Name, Description: me.Description, Planner: PlannerGOAP,
				Actions: own, Goals: []string{me.Name}, process: me.Name})
			out.goals[me.Name] = goal
		default: // hasAgent
			ag, ok := agents[me.Agent]
			if !ok {
				add(path+".agent", "a method names its actor: an agent of the methodology (unknown %q)", me.Agent)
				continue
			}
			goals := goalsOf(m, ag)
			goal := me.Goal
			if goal == "" {
				if len(goals) != 1 {
					add(path+".goal", "agent %s has several goals: name the one the method reaches", ag.Name)
					continue
				}
				goal = goals[0].Name
			}
			i := slices.IndexFunc(goals, func(g Goal) bool { return g.Name == goal })
			if i < 0 {
				add(path+".goal", "%q is not a goal of agent %s", goal, ag.Name)
				continue
			}
			out.goals[me.Name] = goals[i]
		}
	}
	return out, gen
}

func goalsOf(m *Methodology, ag Agent) []Goal {
	if len(ag.Goals) == 0 {
		return m.Goals
	}
	var out []Goal
	for _, g := range m.Goals {
		if slices.Contains(ag.Goals, g.Name) {
			out = append(out, g)
		}
	}
	return out
}

// methodsFor returns the valid methods providing a capability, in declaration order.
func (m *Methodology) methodsFor(capability string, valid map[string]Goal) []Method {
	var out []Method
	for _, me := range m.Methods {
		if _, ok := valid[me.Name]; ok && me.For == capability {
			out = append(out, me)
		}
	}
	return out
}

// sharedCriteria are the conditions every method's goal makes true with the same value: what a step done by any of
// them can count on.
func sharedCriteria(methods []Method, goals map[string]Goal) map[string]bool {
	var out map[string]bool
	for _, me := range methods {
		pre := goals[me.Name].Pre
		if out == nil {
			out = maps.Clone(pre)
			continue
		}
		for k, v := range out {
			if w, ok := pre[k]; !ok || w != v {
				delete(out, k)
			}
		}
	}
	return out
}

// MethodsFor returns the methods providing a capability that apply on the blackboard, the highest priority first
// (declaration order among equals), each with the goal its agent reaches.
func (c *Compiled) MethodsFor(capability string, bb domain.Blackboard) []MethodChoice {
	state := c.methodGuards.Evaluate(bb).State
	applicable := func(me Method) bool { return me.When == "" || state[me.GuardCondition()] }
	ranked := RankByPriority(c.Methodology.methodsFor(capability, c.methods.goals), applicable, func(me Method) int { return me.Priority })
	out := make([]MethodChoice, 0, len(ranked))
	for _, me := range ranked {
		out = append(out, MethodChoice{Method: me, AgentGoal: c.methods.goals[me.Name].Name})
	}
	return out
}

// MethodChoice is an applicable method and the goal its agent reaches.
type MethodChoice struct {
	Method
	AgentGoal string
}

// MethodByName returns a method by name.
func (c *Compiled) MethodByName(name string) (Method, bool) {
	i := slices.IndexFunc(c.Methodology.Methods, func(me Method) bool { return me.Name == name })
	if i < 0 {
		return Method{}, false
	}
	return c.Methodology.Methods[i], true
}

// MethodGoal is the goal the agent of a valid method reaches ("" for an unknown method).
func (c *Compiled) MethodGoal(name string) string { return c.methods.goals[name].Name }

// Role is a role a methodology needs (ADR 0035 §2): the methodology names roles, never users or units; the
// organisation assigns them to users, per unit ("developer@TEAM-PAY", held in the units below it too).
type Role struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Responsibilities assign roles to a step or a method, RACI style: the responsible role carries it out (performs its
// human tasks), the accountable role answers for it (may approve its gates, never on its own change), the consulted
// and informed roles are involved.
type Responsibilities struct {
	Responsible string   `yaml:"responsible,omitempty" json:"responsible,omitempty"`
	Accountable string   `yaml:"accountable,omitempty" json:"accountable,omitempty"`
	Consulted   []string `yaml:"consulted,omitempty" json:"consulted,omitempty"`
	Informed    []string `yaml:"informed,omitempty" json:"informed,omitempty"`
}

// compileRoles validates the declared roles and returns their names.
func (m *Methodology) compileRoles(add func(path, format string, args ...any)) map[string]bool {
	out := map[string]bool{}
	for i, r := range m.Roles {
		path := fmt.Sprintf("roles[%d].name", i)
		switch {
		case !nameRE.MatchString(r.Name):
			add(path, "role name required: lowercase letters, digits, '-' or '_', starting with a letter")
		case out[r.Name]:
			add(path, "duplicate role %s", r.Name)
		default:
			out[r.Name] = true
		}
	}
	return out
}

// checkElementRoles reports the roles agents and actions name that the methodology does not declare (ADR 0043):
// who may run them is said with the methodology's own roles. The roles of a trigger are those of the service
// identity running its process, not checked: they may be platform roles (admin) no methodology declares.
func (m *Methodology) checkElementRoles(add func(path, format string, args ...any), roles map[string]bool) {
	check := func(path string, list []string) {
		for j, r := range list {
			if !roles[r] {
				add(fmt.Sprintf("%s[%d]", path, j), "unknown role %q (declare it in roles)", r)
			}
		}
	}
	for i, ag := range m.Agents {
		check(fmt.Sprintf("agents[%d].roles", i), ag.Roles)
	}
	for i, a := range m.Actions {
		check(fmt.Sprintf("actions[%d].roles", i), a.Roles)
	}
}

// checkResponsibilities reports the roles a step or a method names that the methodology does not declare.
func checkResponsibilities(add func(path, format string, args ...any), path string, r *Responsibilities, roles map[string]bool) {
	if r == nil {
		return
	}
	check := func(field, role string) {
		if role != "" && !roles[role] {
			add(path+"."+field, "unknown role %q (declare it in roles)", role)
		}
	}
	check("responsible", r.Responsible)
	check("accountable", r.Accountable)
	for _, x := range r.Consulted {
		check("consulted", x)
	}
	for _, x := range r.Informed {
		check("informed", x)
	}
}

// Subscription is an event a transverse methodology reacts to: its type and a CEL filter over `event` ({type, change,
// process, step, items}).
type Subscription struct {
	Event  string `yaml:"event" json:"event"`
	Filter string `yaml:"filter,omitempty" json:"filter,omitempty"`
}

// DefaultSubscriptions are the events a transverse methodology without `on` reacts to.
var DefaultSubscriptions = []Subscription{
	{Event: "process.attached"},
	{Event: "step.completed"},
	{Event: "change.item_added", Filter: `event.items.exists(i, i.kind == "risk" || i.kind == "action")`},
}

// Subscriptions returns the events the methodology reacts to (its own, or the default ones).
func (m *Methodology) Subscriptions() []Subscription {
	if len(m.On) > 0 {
		return m.On
	}
	return DefaultSubscriptions
}
