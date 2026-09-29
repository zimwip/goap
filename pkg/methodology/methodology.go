// Package methodology defines the declarative format of an enterprise
// methodology (conditions, actions, goals, agents) and of a domain, and compiles them
// into planner inputs.
package methodology

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/mcp"
)

// Methodology is the declarative definition deployed in the registry.
type Methodology struct {
	Name        string `yaml:"name" json:"name"`
	Version     string `yaml:"version" json:"version"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Namespace is the target namespace (ADR 0013 §3): the namespace the changes of the methodology act on, hence the
	// domain of the nodes it creates and modifies (alm; methodology for the self-observation). Required.
	Namespace  string      `yaml:"namespace" json:"namespace"`
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
	// Types resolves the qualified type references of the methodology (the type catalogue, set by Resolve). Nil: the
	// references are only checked for their form.
	Types TypeSet `yaml:"-" json:"-"`
}

// TypeSet is what a methodology needs of the types in force (pkg/typecat.Catalog, or DomainTypes): the qualified
// node and link types ("alm@Requirement") and the ancestors of each node type.
type TypeSet interface {
	HasNodeType(ref string) bool
	HasLinkType(ref string) bool
	Supertypes() map[string][]string
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
var TriggerEvents = []string{"change.created", "change.applied", "change.item_added", "change.signal", "process.completed", "process.failed", "process.stuck", "process.attached", "methodology.published"}

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

// NodeType is a domain node type.
type NodeType struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Properties  []string `yaml:"properties,omitempty" json:"properties,omitempty"`
	// Extends makes the type a subtype: it inherits the properties and link
	// types of its parent, and conditions on the parent apply to it
	// (x.types contains every supertype, ADR 0009 §6).
	Extends string `yaml:"extends,omitempty" json:"extends,omitempty"`
	// Lifecycle names the state machine of the nodes of the type (one of the
	// domain's lifecycles, ADR 0014). Inherited through extends; empty: the
	// nodes have no state.
	Lifecycle string `yaml:"lifecycle,omitempty" json:"lifecycle,omitempty"`
	// Document makes the type a document embedding nodes of other types
	// through outgoing "contains" links.
	Document *domain.DocumentSpec `yaml:"document,omitempty" json:"document,omitempty"`
	// ChangeControlled: nodes are only modified through a change (default
	// true). False: direct writes, and no lifecycle.
	ChangeControlled *bool `yaml:"changeControlled,omitempty" json:"changeControlled,omitempty"`
	// Validators plug property validator instances (ADR 0018) on the properties
	// of the type (its own or inherited). They run in this order when a node of
	// the type is created or modified, the validators of the supertypes first.
	Validators []PropertyValidator `yaml:"validators,omitempty" json:"validators,omitempty"`
	// Search declares which properties the node index keeps (ADR 0026): text
	// goes into the full-text and embedding document, facet makes the value
	// filterable and countable. Inherited through extends.
	Search []SearchProperty `yaml:"search,omitempty" json:"search,omitempty"`
	// Editor names the editor the user interface opens the nodes of the type
	// with (agent, action, methodology, ...); the UI falls back to its default
	// node editor when it has no editor of that name. Inherited through
	// extends; empty: the default node editor.
	Editor string `yaml:"editor,omitempty" json:"editor,omitempty"`
}

// SearchProperty declares how the node index uses a property of a node type.
type SearchProperty struct {
	Property string `yaml:"property" json:"property"`
	Text     bool   `yaml:"text,omitempty" json:"text,omitempty"`
	Facet    bool   `yaml:"facet,omitempty" json:"facet,omitempty"`
}

// PropertyValidator plugs an algorithm instance of type property_validator on a property.
type PropertyValidator struct {
	Property string `yaml:"property" json:"property"`
	Instance string `yaml:"instance" json:"instance"`
}

// IsChangeControlled tells whether the nodes of the type are modified through changes only.
func (n NodeType) IsChangeControlled() bool { return n.ChangeControlled == nil || *n.ChangeControlled }

// UnmarshalYAML accepts either a plain name or a full object.
func (n *NodeType) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		n.Name = v.Value
		return nil
	}
	type plain NodeType
	return v.Decode((*plain)(n))
}

// LinkType is a domain link type.
type LinkType struct {
	Name string `yaml:"name" json:"name"`
	From string `yaml:"from,omitempty" json:"from,omitempty"`
	To   string `yaml:"to,omitempty" json:"to,omitempty"`
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
	// Permission required from the process initiator to run the action
	// automatically; otherwise the process waits for an authorized approver.
	Permission string `yaml:"permission,omitempty" json:"permission,omitempty"`
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

// Issue is a validation problem located by a field path such as
// "conditions[2].expr" or "actions[0].pre.has_impacts".
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (i Issue) String() string {
	if i.Path == "" {
		return i.Message
	}
	return i.Path + ": " + i.Message
}

// Issues is a list of validation problems; it implements error.
type Issues []Issue

func (is Issues) Error() string {
	msgs := make([]string, len(is))
	for i, x := range is {
		msgs[i] = x.String()
	}
	return strings.Join(msgs, "; ")
}

var lifecycleNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Validate returns every problem of the definition (empty when valid).
func (m *Methodology) Validate() Issues {
	_, issues := m.compile()
	return issues
}

// ValidateStored is Validate plus what a stored methodology must also have (the registry): its target namespace.
func (m *Methodology) ValidateStored() Issues {
	issues := m.Validate()
	if m.Namespace == "" {
		issues = append(Issues{{Path: "namespace", Message: "namespace required: the namespace (domain) the changes of the methodology act on"}}, issues...)
	}
	return issues
}

// Compile validates the methodology and compiles its conditions, including
// the conditions generated from action expectations. The methodology itself
// is not modified; generated effects live in the compiled actions only.
func (m *Methodology) Compile() (*Compiled, error) {
	c, issues := m.compile()
	if len(issues) > 0 {
		return nil, fmt.Errorf("methodology %s: %w", m.Name, issues)
	}
	return c, nil
}

func (m *Methodology) compile() (*Compiled, Issues) {
	var issues Issues
	add := func(path, format string, args ...any) {
		issues = append(issues, Issue{Path: path, Message: fmt.Sprintf(format, args...)})
	}
	switch {
	case m.Name == "":
		add("name", "name required")
	case !nameRE.MatchString(m.Name):
		add("name", "name must be lowercase letters, digits, '-' or '_' and start with a letter")
	}
	if len(m.Goals) == 0 && len(m.Processes) == 0 {
		add("goals", "at least one goal or process required")
	}
	if m.Namespace != "" && !nameRE.MatchString(m.Namespace) {
		add("namespace", "namespace must be lowercase letters, digits, '-' or '_' and start with a letter")
	}
	m.lintTypeRefs(add)

	// conditions: each expression is compiled on its own to report every error
	var defs []condition.Definition
	known := map[string]bool{}
	for i, c := range m.Conditions {
		path := fmt.Sprintf("conditions[%d]", i)
		switch {
		case c.Name == "":
			add(path+".name", "name required")
			continue
		case known[c.Name]:
			add(path+".name", "duplicate condition %s", c.Name)
			continue
		}
		known[c.Name] = true
		d := condition.Definition{Name: c.Name, Expr: c.Expr}
		if _, err := condition.Compile([]condition.Definition{d}); err != nil {
			add(path+".expr", "%s", strings.TrimPrefix(err.Error(), fmt.Sprintf("condition %q: ", c.Name)))
			continue
		}
		defs = append(defs, d)
	}
	// the platform conditions (decision loops, options: ADR 0009 §4), unless the methodology declares its own
	for _, d := range condition.Platform {
		if !known[d.Name] {
			known[d.Name] = true
			defs = append(defs, d)
		}
	}

	actions := map[string]Action{}
	var utilities, whens []condition.Definition
	for i, src := range m.Actions {
		path := fmt.Sprintf("actions[%d]", i)
		a := src
		a.Effects = maps.Clone(src.Effects)
		if a.Name == "" {
			add(path+".name", "name required")
			continue
		}
		if _, dup := actions[a.Name]; dup {
			add(path+".name", "duplicate action %s", a.Name)
			continue
		}
		if !slices.Contains([]string{KindLLM, KindTool, KindHuman, KindBuiltin, KindScript, KindAbstract}, a.Kind) {
			add(path+".kind", "unknown kind %q", a.Kind)
		}
		if a.IsSpecialization() {
			if a.Kind == KindAbstract {
				add(path+".kind", "a specialization must be implemented")
			}
			if strings.Count(a.Specializes, "/") > 1 || strings.HasSuffix(a.Specializes, "/") || a.Specializes == a.Name {
				add(path+".specializes", "specializes must be <action> or <methodology>/<action>")
			}
			if len(a.Pre) > 0 || len(a.Effects) > 0 || a.Expects != nil {
				add(path+".specializes", "a specialization inherits pre, effects and expects from the action it specializes")
			}
			if a.When != "" {
				d := condition.Definition{Name: a.WhenCondition(), Expr: a.When}
				if _, err := condition.Compile([]condition.Definition{d}); err != nil {
					add(path+".when", "%s", strings.TrimPrefix(err.Error(), fmt.Sprintf("condition %q: ", d.Name)))
				} else {
					whens = append(whens, d)
				}
			}
		} else if a.When != "" || a.Priority != 0 {
			add(path+".when", "when and priority apply to specializations only")
		}
		if a.Kind == KindScript {
			if a.Language != LangJavaScript && a.Language != LangGo {
				add(path+".language", "script language must be javascript or go")
			}
			if strings.TrimSpace(a.Code) == "" {
				add(path+".code", "script action requires code")
			}
		}
		if a.Utility != "" {
			if err := condition.CheckNumber(a.Utility); err != nil {
				add(path+".utility", "%v", err)
			} else {
				utilities = append(utilities, condition.Definition{Name: a.Name, Expr: a.Utility})
			}
		}
		switch {
		case a.Kind == KindLLM && a.Prompt == "":
			add(path+".prompt", "llm action requires a prompt")
		case a.Kind == KindTool && a.Tool == "":
			add(path+".tool", "tool action requires a tool")
		case a.Kind == KindTool:
			if _, _, err := mcp.SplitTool(a.Tool); err != nil {
				add(path+".tool", "tool must be <mcp>/<tool>")
			}
		case a.Kind == KindBuiltin && a.Builtin == "":
			add(path+".builtin", "builtin action requires a builtin")
		}
		for _, name := range a.MCPs {
			if !mcp.ValidName(name) {
				add(path+".mcps", "invalid MCP name %q", name)
			}
		}
		if len(a.MCPs) > 0 && a.Kind != KindLLM && a.Kind != KindScript {
			add(path+".mcps", "mcps apply to llm and script actions (a tool action names its tool)")
		}
		if a.Permission != "" && !strings.Contains(a.Permission, ":") {
			add(path+".permission", "permission must be <resource>:<action>")
		}
		if e := a.Expects; e != nil {
			if e.Produce.NodeType != "" {
				if msg := m.checkTypeRef(e.Produce.NodeType, false); msg != "" {
					add(path+".expects.produce.nodeType", "%s", msg)
				} else if r, _ := domain.ParseTypeRef(e.Produce.NodeType); m.Namespace != "" && r.Namespace != m.Namespace {
					add(path+".expects.produce.nodeType", "%s is not a type of %s: a methodology creates nodes of its namespace", e.Produce.NodeType, m.Namespace)
				}
			}
			if e.Link != nil {
				if msg := m.checkTypeRef(e.Link.Type, true); msg != "" {
					add(path+".expects.link.type", "%s", msg)
				}
			}
			expr, err := e.Expr()
			if err != nil {
				add(path+".expects", "%v", err)
			} else {
				d := condition.Definition{Name: a.ExpectCondition(), Expr: expr}
				if _, err := condition.Compile([]condition.Definition{d}); err != nil {
					add(path+".expects.where", "%v", err)
				} else {
					defs = append(defs, d)
					known[d.Name] = true
					if a.Effects == nil {
						a.Effects = map[string]bool{}
					}
					a.Effects[a.ExpectCondition()] = true
				}
			}
		}
		if len(a.Effects) == 0 && !a.IsSpecialization() {
			add(path+".effects", "no effect: the action can never be planned")
		}
		actions[a.Name] = a
	}
	// local specializations must target a planned action of this methodology
	for i, a := range m.Actions {
		target, local := a.SpecializedAction(m.Name)
		if !a.IsSpecialization() || !local {
			continue
		}
		if t, ok := actions[target]; !ok {
			add(fmt.Sprintf("actions[%d].specializes", i), "unknown action %q", target)
		} else if t.IsSpecialization() {
			add(fmt.Sprintf("actions[%d].specializes", i), "cannot specialize the specialization %q", target)
		}
	}
	// references to conditions (expect:* conditions are known once every
	// action has been processed)
	for i, a := range m.Actions {
		for k := range a.Pre {
			if !known[k] {
				add(fmt.Sprintf("actions[%d].pre.%s", i, k), "unknown condition %q", k)
			}
		}
		for k := range a.Effects {
			if !known[k] {
				add(fmt.Sprintf("actions[%d].effects.%s", i, k), "unknown condition %q", k)
			}
		}
	}
	goals := map[string]bool{}
	for i, g := range m.Goals {
		path := fmt.Sprintf("goals[%d]", i)
		if g.Name == "" {
			add(path+".name", "name required")
		} else if goals[g.Name] {
			add(path+".name", "duplicate goal %s", g.Name)
		}
		goals[g.Name] = true
		if len(g.Pre) == 0 {
			add(path+".pre", "a goal needs at least one condition")
		}
		for k := range g.Pre {
			if !known[k] {
				add(path+".pre."+k, "unknown condition %q", k)
			}
		}
	}
	agents := map[string]Agent{}
	for i, ag := range m.Agents {
		path := fmt.Sprintf("agents[%d]", i)
		switch {
		case ag.Name == "":
			add(path+".name", "name required")
		case !nameRE.MatchString(ag.Name):
			add(path+".name", "name must be lowercase letters, digits, '-' or '_'")
		case agents[ag.Name].Name != "":
			add(path+".name", "duplicate agent %s", ag.Name)
		}
		switch ag.Planner {
		case "", PlannerGOAP, PlannerUtility, PlannerHybrid:
		case PlannerLLM, PlannerLLMScoring:
			if ag.Model == "" {
				add(path+".model", "model is required for the %s planner", ag.Planner)
			}
		default:
			add(path+".planner", "planner must be goap, utility, hybrid, llm or llm-scoring")
		}
		for _, name := range ag.MCPs {
			if !mcp.ValidName(name) {
				add(path+".mcps", "invalid MCP name %q", name)
			}
		}
		for j, a := range ag.Actions {
			if act, ok := actions[a]; !ok {
				add(fmt.Sprintf("%s.actions[%d]", path, j), "unknown action %q", a)
			} else if act.IsSpecialization() {
				add(fmt.Sprintf("%s.actions[%d]", path, j), "%q is a specialization: list the action it specializes", a)
			}
		}
		for j, g := range ag.Goals {
			if !goals[g] {
				add(fmt.Sprintf("%s.goals[%d]", path, j), "unknown goal %q", g)
			}
		}
		names := map[string]bool{}
		for j, tr := range ag.Triggers {
			tp := fmt.Sprintf("%s.triggers[%d]", path, j)
			switch {
			case tr.Name == "":
				add(tp+".name", "name required")
			case names[tr.Name]:
				add(tp+".name", "duplicate trigger %s", tr.Name)
			}
			names[tr.Name] = true
			switch tr.Type {
			case TriggerEvent:
				if !slices.Contains(TriggerEvents, tr.Event) {
					add(tp+".event", "event must be one of %s", strings.Join(TriggerEvents, ", "))
				}
				if _, err := condition.CompileEventFilter(tr.Filter); err != nil {
					add(tp+".filter", "%v", err)
				}
			case TriggerSchedule:
				if _, err := cron.ParseStandard(tr.Schedule); err != nil {
					add(tp+".schedule", "invalid cron expression: %v", err)
				}
			default:
				add(tp+".type", "type must be event or schedule")
			}
			switch tr.Target {
			case "", TargetNewChange:
			case TargetEventChange:
				if tr.Type != TriggerEvent || !(strings.HasPrefix(tr.Event, "change.") || strings.HasPrefix(tr.Event, "process.")) {
					add(tp+".target", "event_change requires a change.* or process.* event")
				}
			default:
				add(tp+".target", "target must be new_change or event_change")
			}
			if tr.Goal != "" && !goals[tr.Goal] {
				add(tp+".goal", "unknown goal %q", tr.Goal)
			} else if tr.Goal != "" && len(ag.Goals) > 0 && !slices.Contains(ag.Goals, tr.Goal) {
				add(tp+".goal", "goal %q is not a goal of the agent", tr.Goal)
			}
		}
		if ag.Planner == "" {
			ag.Planner = PlannerGOAP
		}
		agents[ag.Name] = ag
	}
	if len(m.Agents) == 0 && len(m.Goals) > 0 {
		agents[DefaultAgent] = Agent{Name: DefaultAgent, Description: m.Description, Planner: PlannerGOAP}
	}
	roles := m.compileRoles(add)
	meths := m.compileMethods(add, agents, roles)
	procs := m.compileProcesses(add, actions, known, agents, meths, roles)
	defs = append(defs, procs.conditions...)
	for _, a := range procs.actions {
		actions[a.Name] = a
	}
	for _, ag := range procs.agents {
		agents[ag.Name] = ag
	}
	if len(issues) > 0 {
		sort.SliceStable(issues, func(i, j int) bool { return issues[i].Path < issues[j].Path })
		return nil, issues
	}
	set, err := condition.Compile(defs)
	if err != nil {
		return nil, Issues{{Message: err.Error()}}
	}
	uset, err := condition.CompileNumbers(utilities)
	if err != nil {
		return nil, Issues{{Message: err.Error()}}
	}
	wset, err := condition.Compile(whens)
	if err != nil {
		return nil, Issues{{Message: err.Error()}}
	}
	mset, err := condition.Compile(meths.guards)
	if err != nil {
		return nil, Issues{{Message: err.Error()}}
	}
	return &Compiled{Methodology: m, Conditions: set, actions: actions, utilities: uset, agents: agents, whens: wset, processes: procs,
		methods: meths, methodGuards: mset}, nil
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
	return Goal{}, false
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

// nodeTypeMeta is the part of a node type stored as a JSON document by the
// structured (PostgreSQL) registry store.
type nodeTypeMeta struct {
	Lifecycle        string               `json:"lifecycle,omitempty"`
	Document         *domain.DocumentSpec `json:"document,omitempty"`
	ChangeControlled *bool                `json:"changeControlled,omitempty"`
	Validators       []PropertyValidator  `json:"validators,omitempty"`
	Search           []SearchProperty     `json:"search,omitempty"`
}

// MetaJSON serializes the lifecycle, document and change-control declarations.
func (n NodeType) MetaJSON() []byte {
	b, _ := json.Marshal(nodeTypeMeta{Lifecycle: n.Lifecycle, Document: n.Document, ChangeControlled: n.ChangeControlled, Validators: n.Validators, Search: n.Search})
	return b
}

// SetMeta restores what MetaJSON stored.
func (n *NodeType) SetMeta(raw []byte) {
	var m nodeTypeMeta
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return
	}
	n.Lifecycle, n.Document, n.ChangeControlled, n.Validators, n.Search = m.Lifecycle, m.Document, m.ChangeControlled, m.Validators, m.Search
}
