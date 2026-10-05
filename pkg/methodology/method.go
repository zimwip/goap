package methodology

import (
	"fmt"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/events"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
)

// Method is how a step capability is carried out in a context (ADR 0035 §1, reshaped by ADR 0050): what is being
// worked on, which technology. It carries the guidance, reference documents, checklist and deliverables of that way of
// working, and the activities that realize it: the Actions of its pool and/or its own Steps (a method step inherits
// the pool). A method is not an actor and names none: when a step names a capability (`method: build`), the
// scheduler picks the applicable method (When holds; highest Priority, then the most specific context) and creates
// the agent instance that applies it, of the method's name, which plans over the method's actions towards its goal.
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

	// Steps composes the method from its own steps and sub-steps, done by actions, agents or nested processes -
	// the same shape and compilation as a Process's (not a capability dispatch: a method step may not itself name
	// a capability, to avoid methods resolving each other circularly while methods are still being compiled).
	Steps []Step `yaml:"steps,omitempty" json:"steps,omitempty"`
	// Actions are the actions that realize the method (ADR 0050): the pool its agent plans over, in addition to the
	// actions of its Steps, which are part of it too. A method step inherits the pool of its method. With no Steps,
	// Done states what the method reaches.
	Actions []string `yaml:"actions,omitempty" json:"actions,omitempty"`
	// Done are the exit criteria of a method composing no Steps (a method with Steps reaches theirs).
	Done map[string]bool `yaml:"done,omitempty" json:"done,omitempty"`
	// Planner (goap by default), Model and MCPs configure the agent instance the scheduler creates to apply the
	// method: how it plans depends on the work, not on the actor.
	Planner string   `yaml:"planner,omitempty" json:"planner,omitempty"`
	Model   string   `yaml:"model,omitempty" json:"model,omitempty"`
	MCPs    []string `yaml:"mcps,omitempty" json:"mcps,omitempty"`
}

// ActorAgent is the agent generated for the method (ADR 0050): the agent instance that applies it, of its name.
func (m Method) ActorAgent() string { return m.Name }

// Specificity ranks methods of equal priority: how many conditions a method's context states (none: the generic
// method, the fallback). The conjuncts of its `when` are counted.
func (m Method) Specificity() int {
	if strings.TrimSpace(m.When) == "" {
		return 0
	}
	return 1 + strings.Count(m.When, "&&")
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
	gen = compiledProcesses{criteria: map[string]stepCriteria{}, extend: map[string]agentExt{}}
	w := &stepWalker{m: m, add: add, actions: actions, known: known, agents: agents, methods: compiledMethods{}, roles: roles, root: &gen, done: map[string]*processCriteria{}}
	seen := map[string]bool{}
	for i, me := range m.Methods {
		path := fmt.Sprintf("methods[%d]", i)
		hasSteps, hasActions := len(me.Steps) > 0, len(me.Actions) > 0
		switch {
		case !def.NameRE.MatchString(me.Name):
			add(path+".name", "name required: lowercase letters, digits, '-' or '_', starting with a letter")
			continue
		case seen[me.Name]:
			add(path+".name", "duplicate method %s", me.Name)
			continue
		case agents[me.Name].Name != "":
			add(path+".name", "%s is already an agent: the agent applying a method is generated, of the method's name", me.Name)
			continue
		case slices.ContainsFunc(m.Processes, func(p Process) bool { return p.Name == me.Name }):
			add(path+".name", "%s is already a process: the agent applying a method is generated, of the method's name", me.Name)
			continue
		case slices.ContainsFunc(m.Goals, func(g Goal) bool { return g.Name == me.Name }):
			add(path+".name", "%s is already a goal: a method reaches a goal of its name", me.Name)
			continue
		}
		seen[me.Name] = true
		if !def.NameRE.MatchString(me.For) {
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
		for j, a := range me.Actions {
			if _, ok := actions[a]; !ok {
				add(fmt.Sprintf("%s.actions[%d]", path, j), "unknown action %q", a)
			}
		}
		switch me.Planner {
		case "", PlannerGOAP, PlannerUtility, PlannerHybrid:
		case PlannerLLM, PlannerLLMScoring:
			if me.Model == "" {
				add(path+".model", "model is required for the %s planner", me.Planner)
			}
		default:
			add(path+".planner", "planner must be goap, utility, hybrid, llm or llm-scoring")
		}
		agent := Agent{Name: me.Name, Description: me.Description, Planner: me.Planner, Model: me.Model, MCPs: me.MCPs,
			Actions: slices.Clone(me.Actions), Goals: []string{me.Name}, process: me.Name}
		if agent.Planner == "" {
			agent.Planner = PlannerGOAP
		}
		if me.Roles != nil {
			agent.Role = me.Roles.Responsible
		}
		var goal Goal
		switch {
		case !hasSteps && !hasActions:
			add(path+".steps", "a method says how: declare the actions that realize it, or its own steps")
			continue
		case hasSteps:
			if len(me.Done) > 0 {
				add(path+".done", "done applies to a method without steps: the exit criteria of a method composing steps are theirs")
			}
			sub := *w
			sub.out = &compiledProcesses{}
			done := sub.walk(me.Steps, path+".steps", me.Name, nil)
			gen.conditions = append(gen.conditions, sub.out.conditions...)
			gen.actions = append(gen.actions, sub.out.actions...)
			if len(done) == 0 {
				continue
			}
			for _, a := range sub.out.actions {
				agent.Actions = append(agent.Actions, a.Name)
			}
			goal = Goal{Name: me.Name, Description: me.Description, Pre: done}
		default:
			if len(me.Done) == 0 {
				add(path+".done", "a method without steps states what it reaches: its exit criteria")
				continue
			}
			for c := range me.Done {
				if !known[c] {
					add(path+".done", "unknown condition %q", c)
				}
			}
			goal = Goal{Name: me.Name, Description: me.Description, Pre: maps.Clone(me.Done)}
		}
		gen.goals = append(gen.goals, goal)
		gen.agents = append(gen.agents, agent)
		out.goals[me.Name] = goal
	}
	return out, gen
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
// then the most specific context (declaration order among equals), each with the goal its agent reaches.
func (c *Compiled) MethodsFor(capability string, bb domain.Blackboard) []MethodChoice {
	state := c.methodGuards.Evaluate(bb).State
	applicable := func(me Method) bool { return me.When == "" || state[me.GuardCondition()] }
	ranked := RankByPriority(c.Methodology.methodsFor(capability, c.methods.goals), applicable, func(me Method) int { return me.Priority*100 + min(me.Specificity(), 99) })
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
		case !def.NameRE.MatchString(r.Name):
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
	{Event: events.ProcessAttached},
	{Event: events.StepCompleted},
}

// Subscriptions returns the events the methodology reacts to (its own, or the default ones).
func (m *Methodology) Subscriptions() []Subscription {
	if len(m.On) > 0 {
		return m.On
	}
	return DefaultSubscriptions
}
