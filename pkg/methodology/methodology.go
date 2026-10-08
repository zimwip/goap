// Package methodology defines the declarative format of an enterprise
// methodology (conditions, actions, goals, agents) and of a domain, and compiles them
// into planner inputs.
package methodology

import (
	"bytes"
	"fmt"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/events"
	"maps"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/goap"
)

// Methodology is the declarative definition deployed in the registry.
type Methodology struct {
	Name        string `yaml:"name" json:"name"`
	Version     string `yaml:"version" json:"version"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Namespace is the target namespace (ADR 0013 §3): the namespace the changes of the methodology act on, hence the
	// domain of the nodes it creates and modifies (alm; methodology for the self-observation). Required.
	Namespace string `yaml:"namespace" json:"namespace"`
	// Administrative exempts the changes of the methodology from naming a project (ADR 0039): set on
	// methodologies that manage organisation/project/policy/adapter data (the admin surface itself), not on
	// methodologies that do the enterprise's actual work, which must run in the context of a project.
	Administrative bool `yaml:"administrative,omitempty" json:"administrative,omitempty"`
	// Lifecycle names the lifecycle of the domain the changes of the methodology follow (ADR 0058); none: they have no
	// state. A step is tied to a state through its pre: "state:<name>" is a generated condition, true while the change
	// is in that state, and the exit criteria of the steps (their done) are what a gate of the lifecycle asks for.
	Lifecycle string `yaml:"lifecycle,omitempty" json:"lifecycle,omitempty"`
	// Criticality is the default criticality of the changes of the methodology (ADR 0075 §3): C1, C2 or C3, empty for
	// the platform default (C2). The requester may raise it; lowering it asks the permission change:lower-criticality.
	// What each level requires is the policy of the organisation, not of the methodology.
	Criticality string `yaml:"criticality,omitempty" json:"criticality,omitempty"`
	// Goal names the main goal of the methodology (ADR 0096): a declared goal or a process (a process reaches the goal
	// of its name). The changes of the methodology start with it as their goal; empty: Compiled.MainGoal falls back
	// on the first declared goal, else the first process.
	Goal string `yaml:"goal,omitempty" json:"goal,omitempty"`
	// Additions are what the methodology adds to the view of its changes (ADR 0098): the tabs of the change view, each
	// showing change objects of the types it names. Empty: none.
	Additions  *Additions  `yaml:"additions,omitempty" json:"additions,omitempty"`
	Conditions []Condition `yaml:"conditions" json:"conditions"`
	Actions    []Action    `yaml:"actions" json:"actions"`
	Goals      []Goal      `yaml:"goals" json:"goals"`
	// Agents run the methodology; without agents an implicit "default" agent
	// has every action and goal and the goap planner.
	Agents []Agent `yaml:"agents,omitempty" json:"agents,omitempty"`
	// Processes describe how the objective of a change is reached, as steps and sub-steps done by actions, agents,
	// nested processes or people (ADR 0034); each is run by an agent and reaches a goal of its name.
	Processes []Process `yaml:"processes,omitempty" json:"processes,omitempty"`
	// Methods are the documentary references of how a step capability is carried out in a context, each naming the
	// agent that acts (ADR 0035 §1).
	Methods []Method `yaml:"methods,omitempty" json:"methods,omitempty"`
	// Roles are the roles the processes and methods assign (ADR 0035 §2).
	Roles []Role `yaml:"roles,omitempty" json:"roles,omitempty"`
	// AppliesTo makes the methodology transverse (ADR 0036 §3): its processes run alongside the changes of these
	// methodologies, on the same change. A transverse methodology acts in the namespace of the change.
	AppliesTo []string `yaml:"appliesTo,omitempty" json:"appliesTo,omitempty"`
	// Imports names the built-in condition libraries (condition.LibraryNames, ADR 0064) whose conditions the
	// methodology uses without declaring them; a condition it declares under the same name wins. Nothing else is
	// added to its conditions.
	Imports []string `yaml:"imports,omitempty" json:"imports,omitempty"`
	// On are the events of those changes a transverse methodology reacts to (ADR 0036 §3): each matching event runs its
	// processes again, with the event in vars.event. Default: a process attached to the change, a step completed.
	On []Subscription `yaml:"on,omitempty" json:"on,omitempty"`
	// Types resolves the qualified type references of the methodology (the type catalogue, set by Resolve). Nil: the
	// references are only checked for their form.
	Types def.TypeSet `yaml:"-" json:"-"`
	// Builtins resolves the builtin names of the actions (set by WithBuiltins, ADR 0062). Nil: they are not checked.
	Builtins BuiltinSet `yaml:"-" json:"-"`
}

// Planners.
const (
	PlannerGOAP       = "goap"        // A* over the world state
	PlannerUtility    = "utility"     // greedy: the applicable action with the highest utility
	PlannerHybrid     = "hybrid"      // A* with costs weighted by utilities (CEL)
	PlannerLLM        = "llm"         // the LLM picks the next action directly
	PlannerLLMScoring = "llm-scoring" // the LLM scores utility; A* costs weighted by it, like hybrid
)

// DefaultAgent is the name of the implicit agent.
const DefaultAgent = "default"

// Agent is a planner with a set of admissible actions and goals (Embabel
// terminology). Description and examples drive intent identification.
type Agent struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Examples    []string `yaml:"examples,omitempty" json:"examples,omitempty"`
	Planner     string   `yaml:"planner,omitempty" json:"planner,omitempty"`
	// Model is the LLM alias (resolved by the platform, ADR 0021) the llm and llm-scoring
	// planners call each planning cycle; required when Planner is one of them, unused otherwise.
	Model string `yaml:"model,omitempty" json:"model,omitempty"`
	// Actions admissible for the agent (empty: all).
	Actions []string `yaml:"actions,omitempty" json:"actions,omitempty"`
	// Goals of the agent (empty: all).
	Goals []string `yaml:"goals,omitempty" json:"goals,omitempty"`
	// Triggers run the agent automatically (outside the intent loop).
	Triggers []Trigger `yaml:"triggers,omitempty" json:"triggers,omitempty"`
	// MCPs whose tools the llm and script actions of the agent may use, in addition to the ones
	// the actions declare themselves. They do not make an action unschedulable when unbound.
	MCPs []string `yaml:"mcps,omitempty" json:"mcps,omitempty"`
	// Roles allowed to run the agent (ADR 0043), roles the methodology declares: starting it needs one of them
	// on the project, and its actions that declare no roles of their own need it too. Empty: any member of the
	// project.
	Roles []string `yaml:"roles,omitempty" json:"roles,omitempty"`
	// Role is the role the agent acts as (ADR 0050): the one responsible for the method an agent generated for it
	// applies. It gives the agent its overall objective; the method says how.
	Role string `yaml:"role,omitempty" json:"role,omitempty"`
	// process is set on the agent generated to run a process (ADR 0034).
	process string
}

// Trigger types, events and targets.
const (
	TriggerEvent    = "event"
	TriggerSchedule = "schedule"

	TargetNewChange   = "new_change"
	TargetEventChange = "event_change"
)

// TriggerEvents lists the events a trigger can react to.
var TriggerEvents = events.All

// Trigger starts an agent automatically on an event or a schedule.
type Trigger struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Type        string `yaml:"type" json:"type"`
	// Event and Filter (CEL over `event`) for event triggers.
	Event  string `yaml:"event,omitempty" json:"event,omitempty"`
	Filter string `yaml:"filter,omitempty" json:"filter,omitempty"`
	// Schedule is a 5-field cron expression (UTC) for schedule triggers.
	Schedule string `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	Goal     string `yaml:"goal,omitempty" json:"goal,omitempty"`
	Intent   string `yaml:"intent,omitempty" json:"intent,omitempty"`
	// Target: new_change (default) or event_change (the change of the event).
	Target string `yaml:"target,omitempty" json:"target,omitempty"`
	// Roles of the service identity running the process.
	Roles   []string `yaml:"roles,omitempty" json:"roles,omitempty"`
	Enabled bool     `yaml:"enabled" json:"enabled"`
}

// Condition is a named CEL predicate on the blackboard.
type Condition struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Expr        string `yaml:"expr" json:"expr"`
}

// Action kinds.
const (
	KindLLM     = "llm"
	KindTool    = "tool"
	KindHuman   = "human"
	KindBuiltin = "builtin"
	KindScript  = "script"
	// KindAbstract declares a role without implementation: a specialization
	// is chosen at execution (ADR 0009 §5).
	KindAbstract = "abstract"
)

// Script languages.
const (
	LangJavaScript = "javascript"
	LangGo         = "go"
)

// Action is a unit of work.
type Action struct {
	Name        string                 `yaml:"name" json:"name"`
	Description string                 `yaml:"description,omitempty" json:"description,omitempty"`
	Kind        string                 `yaml:"kind" json:"kind"`
	Pre         map[string]bool        `yaml:"pre,omitempty" json:"pre,omitempty"`
	Effects     map[string]bool        `yaml:"effects,omitempty" json:"effects,omitempty"`
	Cost        float64                `yaml:"cost,omitempty" json:"cost,omitempty"`
	Expects     *condition.Expectation `yaml:"expects,omitempty" json:"expects,omitempty"`
	// Verify says how the effect of the action is verified (ADR 0075): the kind of verifier it needs and whether it
	// must differ from the producer. The methodology names no person and no model.
	Verify *Verify `yaml:"verify,omitempty" json:"verify,omitempty"`
	// Permission required from the process initiator to run the action
	// automatically; otherwise the process waits for an authorized approver.
	Permission string `yaml:"permission,omitempty" json:"permission,omitempty"`
	// Roles allowed to run the action (ADR 0043), roles the methodology declares: the process initiator must
	// hold one of them on the project of the change, otherwise the process waits for someone who does. Empty:
	// the roles of the agent running it, any member of the project when it declares none either.
	Roles []string `yaml:"roles,omitempty" json:"roles,omitempty"`
	// llm
	Model  string `yaml:"model,omitempty" json:"model,omitempty"`
	Prompt string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	// tool: "<mcp>/<tool>", a tool of an MCP the organization of the change binds
	Tool string `yaml:"tool,omitempty" json:"tool,omitempty"`
	// MCPs the action uses (llm and script actions; a tool action uses the MCP of its
	// tool). The action can be scheduled only in a change whose organization binds them all.
	MCPs []string `yaml:"mcps,omitempty" json:"mcps,omitempty"`
	// builtin
	Builtin string `yaml:"builtin,omitempty" json:"builtin,omitempty"`
	// human
	Instructions string         `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	Params       map[string]any `yaml:"params,omitempty" json:"params,omitempty"`
	// NodeTypes restricts which qualified node types (<namespace>@<NodeType>) a
	// human task may create or pick to edit; empty: every type of the change's
	// namespace.
	NodeTypes []string `yaml:"nodeTypes,omitempty" json:"nodeTypes,omitempty"`
	// script: code run in the sandbox with the DSL (docs/dsl.md)
	Language string `yaml:"language,omitempty" json:"language,omitempty"`
	Code     string `yaml:"code,omitempty" json:"code,omitempty"`
	// Utility is a CEL expression returning a number, used by the utility
	// and hybrid planners (default: 1).
	Utility string `yaml:"utility,omitempty" json:"utility,omitempty"`
	// Specializes makes the action a specialization of another one: "<action>"
	// of this methodology or "<methodology>/<action>". A specialization is not
	// planned: it inherits the pre-conditions, effects and cost of the action it
	// specializes, and replaces its implementation at execution when When (CEL
	// over the blackboard, empty: always) holds; the highest Priority wins.
	Specializes string `yaml:"specializes,omitempty" json:"specializes,omitempty"`
	When        string `yaml:"when,omitempty" json:"when,omitempty"`
	Priority    int    `yaml:"priority,omitempty" json:"priority,omitempty"`
	// Incremental actions reach their effects over several executions (one
	// per technology, per batch…): an execution that produced items without
	// reaching the effects is progress, not a failure.
	Incremental bool `yaml:"incremental,omitempty" json:"incremental,omitempty"`
	// Step is set on the actions generated for the steps of a process (ADR 0034): the step path
	// "<process>/<step>/<sub-step>"; Implements names the declared action a step runs (its
	// specializations apply).
	Step       string `yaml:"-" json:"-"`
	Implements string `yaml:"-" json:"-"`
}

// Oracles: the kinds of verifier of an action's effect (ADR 0075).
const (
	OracleTool  = "tool"
	OracleHuman = "human"
	OracleModel = "model"
)

// Verify is the verification an action declares (ADR 0075).
type Verify struct {
	// Oracle is the kind of verifier the effect needs: tool, human or model.
	Oracle string `yaml:"oracle" json:"oracle"`
	// Independent says the verifier is not the producer; nil (unset) is true.
	Independent *bool `yaml:"independent,omitempty" json:"independent,omitempty"`
}

// IsIndependent reports whether the verifier must differ from the producer (the default when verify is set).
func (v *Verify) IsIndependent() bool { return v != nil && (v.Independent == nil || *v.Independent) }

// Declared returns the name of the declared action behind a planned one: the action a step runs, or the action itself.
func (a Action) Declared() string {
	if a.Implements != "" {
		return a.Implements
	}
	return a.Name
}

// RequiredMCPs returns the MCPs the action needs: its declared ones and the MCP of its tool.
func (a Action) RequiredMCPs() []string {
	out := slices.Clone(a.MCPs)
	if a.Kind == KindTool {
		if m, _, ok := strings.Cut(a.Tool, "/"); ok && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// IsSpecialization reports whether the action specializes another one.
func (a Action) IsSpecialization() bool { return a.Specializes != "" }

// WhenCondition is the name of the guard condition of a specialization.
func (a Action) WhenCondition() string { return "when:" + a.Name }

// ExpectCondition is the name of the condition generated from an action's expects.
func (a Action) ExpectCondition() string { return "expect:" + a.Name }

// Goal is a target state.
type Goal struct {
	Name        string          `yaml:"name" json:"name"`
	Description string          `yaml:"description,omitempty" json:"description,omitempty"`
	Examples    []string        `yaml:"examples,omitempty" json:"examples,omitempty"`
	Pre         map[string]bool `yaml:"pre" json:"pre"`
	Value       float64         `yaml:"value,omitempty" json:"value,omitempty"`
}

// Parse decodes a YAML (or JSON) methodology. Unknown fields are rejected.
func Parse(data []byte) (*Methodology, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m Methodology
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse methodology: %w", err)
	}
	return &m, nil
}

// Compiled is a validated methodology ready for planning.
type Compiled struct {
	*Methodology
	Conditions *condition.Set
	actions    map[string]Action
	utilities  *condition.NumberSet
	agents     map[string]Agent
	// whens are the guards of the specializations (not part of the world state)
	whens *condition.Set
	// processes are the actions, goals and agents generated for the processes (ADR 0034)
	processes compiledProcesses
	// methods are the valid methods and the guards of their contexts (ADR 0035 §1)
	methods      compiledMethods
	methodGuards *condition.Set
}

// Action returns an action by name.
func (c *Compiled) Action(name string) (Action, bool) {
	a, ok := c.actions[name]
	return a, ok
}

// Goal returns a goal by name (a declared goal, or the goal of a process).
func (c *Compiled) Goal(name string) (Goal, bool) {
	for _, g := range c.Goals {
		if g.Name == name {
			return g, true
		}
	}
	for _, g := range c.processes.goals {
		if g.Name == name {
			return g, true
		}
	}
	// a step path ("<process-or-method>/<step>") names the goal of that step: its exit criteria (ADR 0097). The
	// engine starts an agent towards it to carry out that step alone and let the planner sequence its actions.
	if strings.Contains(name, "/") {
		if s, ok := c.StepByPath(name); ok && len(s.Exit) > 0 {
			return Goal{Name: name, Description: s.Description, Pre: maps.Clone(s.Exit)}, true
		}
	}
	return Goal{}, false
}

// NeededBy returns the conditions a state needs to reach the goal conditions, by backward closure over the actions
// (declared and generated for steps): the goal's own conditions, then the preconditions of every action that
// establishes one of them, and so on. It is a static over-approximation (cost and alternatives ignored) that tells
// which steps work towards a goal (ADR 0097).
func (c *Compiled) NeededBy(goal map[string]bool) map[string]bool {
	need := map[string]bool{}
	for k := range goal {
		need[k] = true
	}
	all := make([]Action, 0, len(c.actions)+len(c.processes.actions))
	for _, a := range c.actions {
		all = append(all, a)
	}
	all = append(all, c.processes.actions...)
	for changed := true; changed; {
		changed = false
		for _, a := range all {
			if a.IsSpecialization() {
				continue
			}
			hit := false
			for k := range a.Effects {
				if need[k] {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			for k := range a.Pre {
				if !need[k] {
					need[k] = true
					changed = true
				}
			}
		}
	}
	return need
}

// MainGoal is the goal the changes of the methodology start with (ADR 0096): the declared Goal, else the first declared
// goal, else the first process (which reaches the goal of its name), else "". It reads the definition only, so a
// Compiled has it too.
func (m *Methodology) MainGoal() string {
	switch {
	case m.Goal != "":
		return m.Goal
	case len(m.Goals) > 0:
		return m.Goals[0].Name
	case len(m.Processes) > 0:
		return m.Processes[0].Name
	}
	return ""
}

// StepActions returns the actions generated for the steps of a process, in step order.
func (c *Compiled) StepActions(process string) []Action {
	var out []Action
	for _, a := range c.processes.actions {
		if p, _, _ := strings.Cut(a.Step, "/"); p == process {
			out = append(out, a)
		}
	}
	return out
}

// PlanningActions returns the planner operators (every action).
func (c *Compiled) PlanningActions() []goap.Action {
	out := make([]goap.Action, 0, len(c.Actions))
	for _, src := range c.Actions {
		a := c.actions[src.Name]
		if a.IsSpecialization() {
			continue
		}
		out = append(out, goap.Action{Name: a.Name, Pre: a.Pre, Effects: a.Effects, Cost: a.Cost})
	}
	return out
}

// Supertypes maps each node type to its ancestors, nearest first (ADR 0009 §6), from the resolved types (nil when
// they are not resolved).
func (m *Methodology) Supertypes() map[string][]string {
	if m.Types == nil {
		return nil
	}
	return m.Types.Supertypes()
}

// SpecializedAction returns the action a specialization targets and whether
// it belongs to the methodology named self.
func (a Action) SpecializedAction(self string) (string, bool) {
	if i := strings.Index(a.Specializes, "/"); i >= 0 {
		return a.Specializes[i+1:], a.Specializes[:i] == self
	}
	return a.Specializes, true
}

// SpecializationsOf returns the specializations this methodology declares
// for the action "<methodology>/<action>", in declaration order.
func (c *Compiled) SpecializationsOf(methodologyName, action string) []Action {
	var out []Action
	for _, src := range c.Actions {
		a := c.actions[src.Name]
		if !a.IsSpecialization() {
			continue
		}
		target, local := a.SpecializedAction(c.Name)
		if target != action {
			continue
		}
		if (local && methodologyName == c.Name) || (!local && strings.HasPrefix(a.Specializes, methodologyName+"/")) {
			out = append(out, a)
		}
	}
	return out
}

// Applicable reports whether the guard of a specialization holds on the
// blackboard (no guard: always; evaluation error: no).
func (c *Compiled) Applicable(a Action, bb domain.Blackboard) bool {
	if a.When == "" {
		return true
	}
	res := c.whens.Evaluate(bb)
	return res.State[a.WhenCondition()]
}

// Agent returns an agent (the implicit default agent when the methodology
// declares none).
func (c *Compiled) Agent(name string) (Agent, bool) {
	a, ok := c.agents[name]
	return a, ok
}

// AgentList returns the agents in declaration order, then the agents of the processes.
func (c *Compiled) AgentList() []Agent {
	var out []Agent
	if len(c.Methodology.Agents) == 0 {
		if ag, ok := c.agents[DefaultAgent]; ok {
			out = append(out, ag)
		}
	}
	for _, a := range c.Methodology.Agents {
		out = append(out, c.agents[a.Name])
	}
	return append(out, c.processes.agents...)
}

// UsesMCPs reports whether an action of the methodology needs an MCP.
func (c *Compiled) UsesMCPs() bool {
	for _, a := range c.actions {
		if len(a.RequiredMCPs()) > 0 {
			return true
		}
	}
	return false
}

// AgentActions returns the planner operators admissible for an agent: for the agent of a process, the actions of its
// steps.
func (c *Compiled) AgentActions(ag Agent) []goap.Action {
	if ag.process != "" {
		var out []goap.Action
		for _, a := range c.StepActions(ag.process) {
			out = append(out, goap.Action{Name: a.Name, Pre: a.Pre, Effects: a.Effects, Cost: a.Cost})
		}
		// the agent generated for a method plans over the actions of its pool too (ADR 0050)
		for _, a := range c.PlanningActions() {
			if slices.Contains(ag.Actions, a.Name) {
				out = append(out, a)
			}
		}
		return out
	}
	all := c.PlanningActions()
	if len(ag.Actions) == 0 {
		return all
	}
	out := make([]goap.Action, 0, len(ag.Actions))
	for _, a := range all {
		if slices.Contains(ag.Actions, a.Name) {
			out = append(out, a)
		}
	}
	return out
}

// AgentGoals returns the goals of an agent.
func (c *Compiled) AgentGoals(ag Agent) []Goal {
	if len(ag.Goals) == 0 {
		return c.Goals
	}
	var out []Goal
	for _, g := range slices.Concat(c.Goals, c.processes.goals) {
		if slices.Contains(ag.Goals, g.Name) {
			out = append(out, g)
		}
	}
	return out
}

// Utilities evaluates the utility of every action against a blackboard
// (actions without utility expression: 1; evaluation errors: 0).
func (c *Compiled) Utilities(bb condition.Input) map[string]float64 {
	out := make(map[string]float64, len(c.actions))
	vals := c.utilities.Evaluate(bb)
	for name := range c.actions {
		v, ok := vals[name]
		if !ok {
			if c.actions[name].Utility != "" {
				v = 0
			} else {
				v = 1
			}
		}
		out[name] = v
	}
	return out
}

// PlanningGoal returns the planner goal.
func (g Goal) PlanningGoal() goap.Goal {
	return goap.Goal{Name: g.Name, Pre: g.Pre, Value: g.Value}
}

// MarshalYAML-friendly export of the definition (import/export format).
func (m *Methodology) YAML() ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return b.Bytes(), enc.Close()
}

// overlap lists the conditions that are both an input (entry, pre) and an output (exit, effect) of an activity, with
// whatever values: an activity takes a condition to do something about it, so it cannot also be what it makes true
// (a guard "pre x: false, effect x: true" says the same thing as the goal and is refused, ADR 0051).
func overlap(entry, exit map[string]bool) []string {
	var out []string
	for k := range exit {
		if _, ok := entry[k]; ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func inputAndOutput(k, of string) string {
	return fmt.Sprintf("condition %s is both an input and an output of %s: an activity does not take what it makes true (drop it from the entry; what is already true is not planned again)", k, of)
}

// Additions are what a methodology adds to the view of its changes (ADR 0098).
type Additions struct {
	Tabs []ChangeTab `yaml:"tabs,omitempty" json:"tabs,omitempty"`
}

// ChangeTab is a tab of the view of the changes of a methodology: the change objects of the types it names, shown by an
// editor of the user interface (empty or unknown: the default object editor).
type ChangeTab struct {
	Title   string   `yaml:"title" json:"title"`
	Editor  string   `yaml:"editor,omitempty" json:"editor,omitempty"`
	Objects []string `yaml:"objects" json:"objects"`
}

// TabsOf lists the tabs a methodology adds to its changes (nil: none).
func (m *Methodology) TabsOf() []ChangeTab {
	if m.Additions == nil {
		return nil
	}
	return m.Additions.Tabs
}
