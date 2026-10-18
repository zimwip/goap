// Package methodology defines the declarative format of an enterprise
// methodology (conditions, actions, goals, agents) and of a domain, and compiles them
// into planner inputs.
package methodology

import (
	"bytes"
	"fmt"
	"github.com/zimwip/goap/contracts/events"
	"github.com/zimwip/goap/contracts/form"
	"github.com/zimwip/goap/lib/def"
	"maps"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/engine/condition"
)

// Methodology is the declarative definition deployed in the registry.
type Methodology struct {
	Name        string `yaml:"name" json:"name"`
	Version     string `yaml:"version" json:"version"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Namespace is the target namespace (ADR 0013 §3): the namespace the changes of the methodology act on, hence the
	// domain of the nodes it creates and modifies (alm; methodology for the self-observation). Required.
	Namespace string `yaml:"namespace" json:"namespace"`
	// Domains pins the version of the domains the methodology is written against (ADR 0113), by domain name: its
	// definition is validated and compiled against those versions, whatever is published since. A draft pins what it
	// declares and follows the latest published version of the others; publishing pins every published domain in
	// force it does not pin yet, so a published version keeps the types it was validated against. A new version of the
	// methodology moves to the latest domains (Service.CreateVersion drops the pins), the built-in domains are never
	// pinned (they change with the platform).
	Domains map[string]string `yaml:"domains,omitempty" json:"domains,omitempty"`
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
	Goal       string      `yaml:"goal,omitempty" json:"goal,omitempty"`
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
	// Events are the events of the methodology (ADR 0110), in addition to the standard ones (events.All): a signal
	// item of that name raises one (an action's emits, a person, a script), and an on or a trigger may follow it.
	Events []Event `yaml:"events,omitempty" json:"events,omitempty"`
	// Integrates names the methodologies whose changes the changes of this one integrate as sub-changes (ADR 0106):
	// a methodology integrating others is one level above them (a release above the changes it delivers).
	Integrates []Integration `yaml:"integrates,omitempty" json:"integrates,omitempty"`
	// Guardian is what the methodology requires of the changes it integrates, as an outer guardian of their chain
	// (ADR 0106).
	Guardian *Guardian `yaml:"guardian,omitempty" json:"guardian,omitempty"`
	// Assessments say how the assessments of the subjects of its changes read (ADR 0116): per subject, states bound to
	// conditions with a tone, a label and whether the bubble opens; the server evaluates them, the web renders them.
	Assessments []AssessmentView `yaml:"assessments,omitempty" json:"assessments,omitempty"`
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
	// Role is the role the agent acts as (ADR 0050, 0136): its own, else the responsible role of the process or the
	// method it runs (generated agents), else the one role of its actions. It gives the agent its overall objective;
	// the method says how.
	Role string `yaml:"role,omitempty" json:"role,omitempty"`
	// Persistent makes the agent persistent (ADR 0112): one instance per scope, run again in place for every new
	// execution there, keeping its conversation.
	Persistent *Persistence `yaml:"persistent,omitempty" json:"persistent,omitempty"`
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
	// Supersede (event triggers on the event's change): an event firing the trigger cancels its runs still at work on
	// that change before it starts the new one, which works on the change as it is now (ADR 0110).
	Supersede bool `yaml:"supersede,omitempty" json:"supersede,omitempty"`
	// Roles of the service identity running the process.
	Roles   []string `yaml:"roles,omitempty" json:"roles,omitempty"`
	Enabled bool     `yaml:"enabled" json:"enabled"`
}

// Condition is a named CEL predicate on the blackboard: about the change (the default), about one subject of a scope
// (Scope, Where: `self` is the subject, ADR 0109 §3), or an aggregate bringing a scoped condition to the level of the
// change (All, Any or None, Where, Empty).
type Condition struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Expr        string `yaml:"expr,omitempty" json:"expr,omitempty"`
	// Scope is the kind of subject: change (default), impact, item, risk, action or step.
	Scope string `yaml:"scope,omitempty" json:"scope,omitempty"`
	// Where narrows the subjects of a scoped condition or an aggregate (CEL, `self` the subject).
	Where string `yaml:"where,omitempty" json:"where,omitempty"`
	// All, Any, None: the scoped condition an aggregate is over.
	All  string `yaml:"all,omitempty" json:"all,omitempty"`
	Any  string `yaml:"any,omitempty" json:"any,omitempty"`
	None string `yaml:"none,omitempty" json:"none,omitempty"`
	// Empty is the value of an all aggregate over no subject (default false).
	Empty bool `yaml:"empty,omitempty" json:"empty,omitempty"`
}

// Definition is the condition as the condition package compiles it.
func (c Condition) Definition() condition.Definition {
	return condition.Definition{Name: c.Name, Description: c.Description, Expr: c.Expr, Scope: c.Scope, Where: c.Where, All: c.All, Any: c.Any, None: c.None, Empty: c.Empty}
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
	// KindCall is the kind of the actions the compiler generates for the steps done by a sub-agent (a nested
	// process, a method): the action runs the sub-agent its Call describes and waits for it. A methodology does not
	// declare it.
	KindCall = "call"
)

// Call is what an action of kind KindCall runs (ADR 0034, ADR 0035 §1, ADR 0050): a sub-agent on the same change,
// waited for. It is the execution element of a step of a process carried out by a nested process or by the method
// providing a capability, stated by the compiler so that the engine reads it rather than decodes parameters.
type Call struct {
	// Methodology is the methodology of the agent (empty: the one of the action).
	Methodology string `json:"methodology,omitempty"`
	// Agent and Goal are the sub-agent and its goal, when the step names them (a nested process).
	Agent string `json:"agent,omitempty"`
	Goal  string `json:"goal,omitempty"`
	// Capability makes the engine choose, when the step runs, the method providing it that applies there
	// (Compiled.MethodsFor): its agent towards its goal.
	Capability string `json:"capability,omitempty"`
	// Foreach (CEL list) runs the call once per element, in parallel streams, each with `vars.item` bound; GroupBy
	// (CEL value, `self` the element) runs it once per group of elements instead.
	Foreach string `json:"foreach,omitempty"`
	GroupBy string `json:"groupBy,omitempty"`
}

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
	// Role is the role the action acts as (ADR 0136): its own, else the responsible role of the element refining it
	// (its step, the method whose pool lists it), else the role of the action it specializes. A person performing a
	// human action holds it.
	Role string `yaml:"role,omitempty" json:"role,omitempty"`
	// llm
	Model  string `yaml:"model,omitempty" json:"model,omitempty"`
	Prompt string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	// Loop is the budget of the tool loop of an llm action (ADR 0118): rounds, size of a tool result, tokens,
	// duration, compaction; nil: the engine's defaults, fit for an analysis.
	Loop *Loop `yaml:"loop,omitempty" json:"loop,omitempty"`
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
	// Form is what a person fills in to carry out a human action (ADR 0115); its values are checked by the engine and
	// handed to Code when the action has some (ctx.var("form")), else recorded as an artifact with the links they ask.
	Form *form.Form `yaml:"form,omitempty" json:"form,omitempty"`
	// script: code run in the sandbox with the DSL (docs/dsl.md); a human action with a form runs it on its values
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
	// On makes the action reactive (ADR 0110): it runs again on a change of its methodology for each matching event,
	// towards its effects, through an agent generated for it (ReactionAgent).
	On []Subscription `yaml:"on,omitempty" json:"on,omitempty"`
	// Emits are the events of the methodology (Methodology.Events) the action raises when it reaches its effects
	// (ADR 0110): the engine adds a signal item of each name to the change.
	Emits []string `yaml:"emits,omitempty" json:"emits,omitempty"`
	// Step is set on the actions generated for the steps of a process (ADR 0034): the path of the step, read
	// through Compiled.StepByPath; Implements names the declared action a step runs (its specializations apply).
	Step       string `yaml:"-" json:"-"`
	Implements string `yaml:"-" json:"-"`
	// Call is the sub-agent a generated action of kind KindCall runs (compiled only).
	Call *Call `yaml:"-" json:"-"`
	// Scope makes the action scoped (ADR 0109 §3.5): it runs once per pending subject of that scope (within Where),
	// its pre and effects naming conditions of that scope. The planner sees it lifted: its pre is "some subject is
	// pending" (PendingCondition), its effects the aggregates of its scoped effects.
	Scope string `yaml:"scope,omitempty" json:"scope,omitempty"`
	Where string `yaml:"where,omitempty" json:"where,omitempty"`
	// Parallel bounds the subjects a scoped llm or tool action runs for at once (0: the engine's bound, 1: one after
	// the other).
	Parallel int `yaml:"parallel,omitempty" json:"parallel,omitempty"`
	// ScopedPre / ScopedEffects are the scoped conditions of a compiled scoped action, checked on its subject; Lifted
	// are the aggregates its effects were lifted to (compiled only).
	ScopedPre     map[string]bool `yaml:"-" json:"-"`
	ScopedEffects map[string]bool `yaml:"-" json:"-"`
	Lifted        map[string]bool `yaml:"-" json:"-"`
	// Narrowed is set on the action of a step whose where narrows the subjects of the scoped action it runs: its
	// subjects are its own (the generated conditions are named after the step) and its effects are lifted to the
	// aggregates over them (compiled only).
	Narrowed bool `yaml:"-" json:"-"`
	// stepDone are the exit criteria of the step of a narrowed action, merged again once it is lifted.
	stepDone map[string]bool
}

// scopeName names the generated conditions of a scoped action: the declared action, or the step narrowing it.
func (a Action) scopeName() string {
	if a.Narrowed {
		return a.Name
	}
	return a.Declared()
}

// SubjectCondition is the generated scoped condition of a scoped action: true on a subject it is pending for (its
// scoped pre holds there, its scoped effects do not).
func (a Action) SubjectCondition() string { return "subject:" + a.scopeName() }

// PendingCondition is the generated condition of the change of a scoped action: some subject is pending.
func (a Action) PendingCondition() string { return "pending:" + a.scopeName() }

// IsScoped reports whether the action runs per subject of a scope.
func (a Action) IsScoped() bool { return a.Scope != "" && a.Scope != condition.ScopeChange }

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
	// definition is the methodology compiled: Definition gives it to those who edit, store or show it; what runs it
	// (the engine) reads the compiled elements through the methods of Compiled (ADR 0126).
	definition *Methodology
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
	// reactions are the activities declaring on (ADR 0110)
	reactions []Reaction
	// automaticReviews gives the automatic review step of each action generated for it (ADR 0130)
	automaticReviews map[string]string
	// gates are the gates of the processes (ADR 0132)
	gates []GateInfo
	// assessments are the compiled assessment views (ADR 0116); nil: the defaults
	assessments *condition.AssessmentViews
	// view is the shape of the activities as they are shown (ADR 0126 step 3), computed once at compilation
	view *View
}

// Definition is the methodology as written, compiled into c.
func (c *Compiled) Definition() *Methodology { return c.definition }

// WithDefinition is a copy of c holding m as its definition (another version of the same rules, a fake of a test);
// c may be a zero Compiled.
func (c *Compiled) WithDefinition(m *Methodology) *Compiled {
	out := *c
	out.definition = m
	out.view = nil // computed again from m on demand
	return &out
}

// Action returns an action by name.
func (c *Compiled) Action(name string) (Action, bool) {
	a, ok := c.actions[name]
	return a, ok
}

// Goal returns a goal by name (a declared goal, or the goal of a process).
func (c *Compiled) Goal(name string) (Goal, bool) {
	for _, g := range c.definition.Goals {
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
	return c.reactionGoal(name)
}

// NeededBy returns the conditions a state needs to reach the goal conditions, by backward closure over the actions
// (declared and generated for steps): the goal's own conditions, then the preconditions of every action that
// establishes one of them, and so on. It is a static over-approximation (cost and alternatives ignored) that tells
// which steps work towards a goal (ADR 0097).
func (c *Compiled) NeededBy(goal map[string]bool) map[string]bool {
	need := map[string]bool{}
	add := func(k string) bool {
		if need[k] {
			return false
		}
		need[k] = true
		for _, p := range c.Prerequisites(k) { // a condition needs what it cannot hold without (ADR 0112 §5)
			need[p] = true
		}
		return true
	}
	for k := range goal {
		add(k)
	}
	all := c.allActions()
	for changed := true; changed; {
		changed = false
		for _, a := range all {
			if a.IsSpecialization() {
				continue
			}
			hit := false
			for k, v := range a.Effects {
				if v && c.establishes(k, need) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			for k := range a.Pre {
				if add(k) {
					changed = true
				}
			}
		}
	}
	return need
}

// allActions are the declared actions and the ones generated for the steps.
func (c *Compiled) allActions() []Action {
	all := make([]Action, 0, len(c.actions)+len(c.processes.actions))
	for _, a := range c.actions {
		all = append(all, a)
	}
	return append(all, c.processes.actions...)
}

// establishes reports whether making an effect hold makes one of the needed conditions hold: the condition itself,
// or one it cannot hold without.
func (c *Compiled) establishes(effect string, need map[string]bool) bool {
	if need[effect] {
		return true
	}
	for _, p := range c.Prerequisites(effect) {
		if need[p] {
			return true
		}
	}
	return false
}

// Prerequisites are the conditions a condition cannot hold without (ADR 0112 §5): read from their expressions
// (condition.Set.Implied), so `description_accepted` needs `description_rated`. Reaching them is a prerequisite of the
// condition, whatever action does it.
func (c *Compiled) Prerequisites(name string) []string {
	if c.Conditions == nil {
		return nil
	}
	return c.Conditions.Implied(name)
}

// Producers are the actions (declared ones by name, then the steps by path) that make a condition hold: one of their
// effects is the condition, or implies it.
func (c *Compiled) Producers(name string) []string {
	return c.ProducersWhere(func(effect string) bool { return effect == name || slices.Contains(c.Prerequisites(effect), name) })
}

// ProducersWhere are the actions (sorted names) one of whose effects, made true, satisfies establishes.
func (c *Compiled) ProducersWhere(establishes func(effect string) bool) []string {
	var out []string
	for _, a := range c.allActions() {
		if a.IsSpecialization() || slices.Contains(out, a.Name) {
			continue
		}
		for k, v := range a.Effects {
			if v && establishes(k) {
				out = append(out, a.Name)
				break
			}
		}
	}
	slices.Sort(out)
	return out
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

// PlannedActions returns the actions a planner plans over (every declared action but the specializations, which
// replace the implementation of the action they specialize), in declaration order.
func (c *Compiled) PlannedActions() []Action {
	out := make([]Action, 0, len(c.definition.Actions))
	for _, src := range c.definition.Actions {
		if a := c.actions[src.Name]; !a.IsSpecialization() {
			out = append(out, a)
		}
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
	for _, src := range c.definition.Actions {
		a := c.actions[src.Name]
		if !a.IsSpecialization() {
			continue
		}
		target, local := a.SpecializedAction(c.definition.Name)
		if target != action {
			continue
		}
		if (local && methodologyName == c.definition.Name) || (!local && strings.HasPrefix(a.Specializes, methodologyName+"/")) {
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
	if len(c.definition.Agents) == 0 {
		if ag, ok := c.agents[DefaultAgent]; ok {
			out = append(out, ag)
		}
	}
	for _, a := range c.definition.Agents {
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

// AgentActions returns the actions admissible for an agent, which its planner plans over: for the agent of a process,
// the actions of its steps.
func (c *Compiled) AgentActions(ag Agent) []Action {
	if ag.process != "" {
		out := slices.Clone(c.StepActions(ag.process))
		// the agent generated for a method plans over the actions of its pool too (ADR 0050)
		for _, a := range c.PlannedActions() {
			if slices.Contains(ag.Actions, a.Name) {
				out = append(out, a)
			}
		}
		return out
	}
	all := c.PlannedActions()
	if len(ag.Actions) == 0 {
		// an agent of every action plans the gates of the methodology too (ADR 0132): a change lands through them
		return append(all, c.gateActions()...)
	}
	out := make([]Action, 0, len(ag.Actions))
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
		return c.definition.Goals
	}
	var out []Goal
	for _, g := range slices.Concat(c.definition.Goals, c.processes.goals) {
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

// What the engine reads of a compiled methodology to run it (ADR 0126): the identity of the rules and what a change
// run under them is given; the definition itself stays behind Definition.

// Name is the name of the methodology.
func (c *Compiled) Name() string { return c.definition.Name }

// Version is the version compiled.
func (c *Compiled) Version() string { return c.definition.Version }

// Description says what the methodology is for.
func (c *Compiled) Description() string { return c.definition.Description }

// Namespace is the namespace the changes of the methodology act on.
func (c *Compiled) Namespace() string { return c.definition.Namespace }

// Administrative reports whether the methodology manages the organisation, its projects, policies or adapters: the
// engine marks the changes it creates so (domain.DataAdministrative).
func (c *Compiled) Administrative() bool { return c.definition.Administrative }

// Criticality is the criticality the changes of the methodology start with (domain.DataCriticality; empty: none).
func (c *Compiled) Criticality() string { return c.definition.Criticality }

// MainGoal is the goal a change of the methodology is driven towards (Methodology.MainGoal).
func (c *Compiled) MainGoal() string { return c.definition.MainGoal() }

// AppliesTo are the methodologies a transverse methodology runs alongside (ADR 0036 §3).
func (c *Compiled) AppliesTo() []string { return c.definition.AppliesTo }

// Subscriptions are the events the methodology follows at its own level (ADR 0036 §3, ADR 0106).
func (c *Compiled) Subscriptions() []Subscription { return c.definition.Subscriptions() }

// Supertypes maps the node types the methodology's domains declare to their ancestors.
func (c *Compiled) Supertypes() map[string][]string { return c.definition.Supertypes() }

// ProcessInfo is a process of a compiled methodology: its steps are ProcessSteps, its agent the one generated for it
// (Agent.ProcessOf).
type ProcessInfo struct {
	Name, Description string
	// Nested is set on a process a step of another process of the methodology carries out.
	Nested bool
}

// Processes are the processes of the methodology, in declaration order.
func (c *Compiled) Processes() []ProcessInfo {
	nested := map[string]bool{}
	var walk func([]Step)
	walk = func(steps []Step) {
		for _, s := range steps {
			if s.Process != "" {
				if other, proc := s.NestedProcess(); other == "" || other == c.definition.Name {
					nested[proc] = true
				}
			}
			walk(s.Steps)
		}
	}
	for _, p := range c.definition.Processes {
		walk(p.Steps)
	}
	out := make([]ProcessInfo, 0, len(c.definition.Processes))
	for _, p := range c.definition.Processes {
		out = append(out, ProcessInfo{Name: p.Name, Description: p.Description, Nested: nested[p.Name]})
	}
	return out
}

// Process is the process of that name.
func (c *Compiled) Process(name string) (ProcessInfo, bool) {
	for _, p := range c.Processes() {
		if p.Name == name {
			return p, true
		}
	}
	return ProcessInfo{}, false
}
