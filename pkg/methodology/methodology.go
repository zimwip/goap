// Package methodology defines the declarative format of an enterprise
// methodology (domain schema, conditions, actions, goals) and compiles it
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
	"github.com/zimwip/goap/pkg/guard"
)

// Methodology is the declarative definition deployed in the registry.
type Methodology struct {
	Name        string `yaml:"name" json:"name"`
	Version     string `yaml:"version" json:"version"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Namespace is the graph namespace the changes of the methodology act on
	// (default sdlc). A methodology that edits the meta model (methodology
	// nodes, node types) targets "metadata".
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	// DomainRef references a shared Domain "<name>@<version>" (version empty:
	// latest published) instead of embedding one; Resolve fills Domain from it.
	DomainRef  string      `yaml:"domainRef,omitempty" json:"domainRef,omitempty"`
	Domain     Schema      `yaml:"domain,omitempty" json:"domain"`
	Conditions []Condition `yaml:"conditions" json:"conditions"`
	Actions    []Action    `yaml:"actions" json:"actions"`
	Goals      []Goal      `yaml:"goals" json:"goals"`
	// Agents run the methodology; without agents an implicit "default" agent
	// has every action and goal and the goap planner.
	Agents []Agent `yaml:"agents,omitempty" json:"agents,omitempty"`
}

// Planners.
const (
	PlannerGOAP    = "goap"    // A* over the world state
	PlannerUtility = "utility" // greedy: the applicable action with the highest utility
	PlannerHybrid  = "hybrid"  // A* with costs weighted by utilities
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
	// Actions admissible for the agent (empty: all).
	Actions []string `yaml:"actions,omitempty" json:"actions,omitempty"`
	// Goals of the agent (empty: all).
	Goals []string `yaml:"goals,omitempty" json:"goals,omitempty"`
	// Triggers run the agent automatically (outside the intent loop).
	Triggers []Trigger `yaml:"triggers,omitempty" json:"triggers,omitempty"`
}

// Trigger types, events and targets.
const (
	TriggerEvent    = "event"
	TriggerSchedule = "schedule"

	TargetNewChange   = "new_change"
	TargetEventChange = "event_change"
)

// TriggerEvents lists the events a trigger can react to.
var TriggerEvents = []string{"change.created", "change.applied", "change.item_added", "process.completed", "process.failed", "process.stuck", "methodology.published"}

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

// Schema is the domain model of the methodology.
type Schema struct {
	NodeTypes []NodeType `yaml:"nodeTypes" json:"nodeTypes"`
	LinkTypes []LinkType `yaml:"linkTypes" json:"linkTypes"`
	// Lifecycles are the state machines node types refer to by name.
	Lifecycles []domain.Lifecycle `yaml:"lifecycles,omitempty" json:"lifecycles,omitempty"`
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
	// tool
	Tool string `yaml:"tool,omitempty" json:"tool,omitempty"`
	// builtin
	Builtin string `yaml:"builtin,omitempty" json:"builtin,omitempty"`
	// human
	Instructions string         `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	Params       map[string]any `yaml:"params,omitempty" json:"params,omitempty"`
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

// check validates the schema and returns the sets of node and link type
// names. Issue paths start with prefix.
func (s Schema) check(prefix string, add func(path, format string, args ...any)) (nodeTypes, linkTypes map[string]bool) {
	nodeTypes = map[string]bool{}
	for i, n := range s.NodeTypes {
		path := fmt.Sprintf(prefix+"nodeTypes[%d]", i)
		if n.Name == "" {
			add(path+".name", "name required")
		} else if nodeTypes[n.Name] {
			add(path+".name", "duplicate node type %s", n.Name)
		}
		nodeTypes[n.Name] = true
	}
	parents := map[string]string{}
	for i, n := range s.NodeTypes {
		if n.Extends == "" {
			continue
		}
		if !nodeTypes[n.Extends] {
			add(fmt.Sprintf(prefix+"nodeTypes[%d].extends", i), "unknown node type %s", n.Extends)
			continue
		}
		parents[n.Name] = n.Extends
	}
	for i, n := range s.NodeTypes {
		seen := map[string]bool{n.Name: true}
		for t := parents[n.Name]; t != ""; t = parents[t] {
			if seen[t] {
				add(fmt.Sprintf(prefix+"nodeTypes[%d].extends", i), "cyclic subtyping through %s", t)
				break
			}
			seen[t] = true
		}
	}
	s.checkLifecycles(prefix, nodeTypes, add)
	linkTypes = map[string]bool{}
	for i, l := range s.LinkTypes {
		path := fmt.Sprintf(prefix+"linkTypes[%d]", i)
		if l.Name == "" {
			add(path+".name", "name required")
		} else if linkTypes[l.Name] {
			add(path+".name", "duplicate link type %s", l.Name)
		}
		linkTypes[l.Name] = true
		if l.From != "" && !nodeTypes[l.From] {
			add(path+".from", "unknown node type %s", l.From)
		}
		if l.To != "" && !nodeTypes[l.To] {
			add(path+".to", "unknown node type %s", l.To)
		}
	}
	return nodeTypes, linkTypes
}

// Lifecycle returns the named lifecycle of the schema.
func (s Schema) Lifecycle(name string) *domain.Lifecycle {
	for i := range s.Lifecycles {
		if s.Lifecycles[i].Name == name {
			return &s.Lifecycles[i]
		}
	}
	return nil
}

// LifecycleOf returns the lifecycle of a node type: the one it names, else the
// one of its nearest ancestor that names one. Nil when the nodes have no state.
func (s Schema) LifecycleOf(name string) *domain.Lifecycle {
	byName := map[string]NodeType{}
	for _, n := range s.NodeTypes {
		byName[n.Name] = n
	}
	for seen := map[string]bool{}; name != "" && !seen[name]; name = byName[name].Extends {
		seen[name] = true
		if ref := byName[name].Lifecycle; ref != "" {
			return s.Lifecycle(ref)
		}
	}
	return nil
}

// checkLifecycles validates the lifecycles, the references to them and the
// document declarations.
func (s Schema) checkLifecycles(prefix string, nodeTypes map[string]bool, add func(path, format string, args ...any)) {
	names := map[string]bool{}
	for i, l := range s.Lifecycles {
		path := fmt.Sprintf(prefix+"lifecycles[%d]", i)
		switch {
		case l.Name == "":
			add(path+".name", "name required")
		case !lifecycleNameRE.MatchString(l.Name):
			add(path+".name", "name must be lowercase letters, digits, '-' or '_' and start with a letter")
		case names[l.Name]:
			add(path+".name", "duplicate lifecycle %s", l.Name)
		}
		names[l.Name] = true
		for _, msg := range l.Issues() {
			add(path, "%s", msg)
		}
		for j, t := range l.Transitions {
			if _, err := guard.Compile(t.Guard); err != nil {
				add(fmt.Sprintf(path+".transitions[%d].guard", j), "%v", err)
			}
		}
	}
	for i, n := range s.NodeTypes {
		path := fmt.Sprintf(prefix+"nodeTypes[%d]", i)
		if n.Lifecycle != "" {
			if !names[n.Lifecycle] {
				add(path+".lifecycle", "unknown lifecycle %s", n.Lifecycle)
			}
			if !n.IsChangeControlled() {
				add(path+".changeControlled", "a type with a lifecycle must be change controlled")
			}
		}
		if d := n.Document; d != nil {
			if len(d.Contains) == 0 {
				add(path+".document.contains", "a document contains at least one node type")
			}
			for _, c := range d.Contains {
				if !nodeTypes[c] {
					add(path+".document.contains", "unknown node type %s", c)
				}
			}
		}
	}
	// child states of a document transition must exist on a contained type
	for i, n := range s.NodeTypes {
		l := s.LifecycleOf(n.Name)
		if l == nil {
			continue
		}
		for _, t := range l.Transitions {
			if t.Children == nil {
				continue
			}
			path := fmt.Sprintf(prefix+"nodeTypes[%d].lifecycle", i)
			doc := s.documentOf(n.Name)
			if doc == nil {
				add(path, "transition %s of lifecycle %s constrains children but %s is not a document", t.Name, l.Name, n.Name)
				continue
			}
			for _, st := range t.Children.States {
				found := false
				for _, c := range doc.Contains {
					if cl := s.LifecycleOf(c); cl != nil {
						if _, ok := cl.State(st); ok {
							found = true
						}
					}
				}
				if !found {
					add(path, "child state %q of transition %s exists on none of the contained types", st, t.Name)
				}
			}
		}
	}
}

// documentOf returns the document spec of a type (inherited through extends).
func (s Schema) documentOf(name string) *domain.DocumentSpec {
	byName := map[string]NodeType{}
	for _, n := range s.NodeTypes {
		byName[n.Name] = n
	}
	for seen := map[string]bool{}; name != "" && !seen[name]; name = byName[name].Extends {
		seen[name] = true
		if d := byName[name].Document; d != nil {
			return d
		}
	}
	return nil
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
	if len(m.Goals) == 0 {
		add("goals", "at least one goal required")
	}
	nodeTypes, linkTypes := m.Domain.check("domain.", add)
	if m.DomainRef != "" {
		if _, _, err := SplitRef(m.DomainRef); err != nil {
			add("domainRef", "%v", err)
		}
		if len(m.Domain.NodeTypes) == 0 {
			add("domainRef", "domain reference not resolved (see Resolve)")
		} else {
			m.lintDomainRefs(nodeTypes, linkTypes, add)
		}
	}

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
		case a.Kind == KindBuiltin && a.Builtin == "":
			add(path+".builtin", "builtin action requires a builtin")
		}
		if a.Permission != "" && !strings.Contains(a.Permission, ":") {
			add(path+".permission", "permission must be <resource>:<action>")
		}
		if e := a.Expects; e != nil {
			if e.Produce.NodeType != "" && len(nodeTypes) > 0 && !nodeTypes[e.Produce.NodeType] {
				add(path+".expects.produce.nodeType", "unknown node type %s", e.Produce.NodeType)
			}
			if e.Link != nil && len(linkTypes) > 0 && !linkTypes[e.Link.Type] {
				add(path+".expects.link.type", "unknown link type %s", e.Link.Type)
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
		default:
			add(path+".planner", "planner must be goap, utility or hybrid")
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
	if len(m.Agents) == 0 {
		agents[DefaultAgent] = Agent{Name: DefaultAgent, Description: m.Description, Planner: PlannerGOAP}
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
	return &Compiled{Methodology: m, Conditions: set, actions: actions, utilities: uset, agents: agents, whens: wset}, nil
}

// Action returns an action by name.
func (c *Compiled) Action(name string) (Action, bool) {
	a, ok := c.actions[name]
	return a, ok
}

// Goal returns a goal by name.
func (c *Compiled) Goal(name string) (Goal, bool) {
	for _, g := range c.Goals {
		if g.Name == name {
			return g, true
		}
	}
	return Goal{}, false
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

// Supertypes maps each node type to its ancestors, nearest first (ADR 0009 §6).
func (m *Methodology) Supertypes() map[string][]string {
	parents := map[string]string{}
	for _, n := range m.Domain.NodeTypes {
		if n.Extends != "" {
			parents[n.Name] = n.Extends
		}
	}
	out := map[string][]string{}
	for _, n := range m.Domain.NodeTypes {
		seen := map[string]bool{n.Name: true}
		for t := parents[n.Name]; t != "" && !seen[t]; t = parents[t] {
			seen[t] = true
			out[n.Name] = append(out[n.Name], t)
		}
	}
	return out
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

// AgentList returns the agents in declaration order.
func (c *Compiled) AgentList() []Agent {
	if len(c.Methodology.Agents) == 0 {
		return []Agent{c.agents[DefaultAgent]}
	}
	out := make([]Agent, 0, len(c.Methodology.Agents))
	for _, a := range c.Methodology.Agents {
		out = append(out, c.agents[a.Name])
	}
	return out
}

// AgentActions returns the planner operators admissible for an agent.
func (c *Compiled) AgentActions(ag Agent) []goap.Action {
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
	for _, g := range c.Goals {
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
}

// MetaJSON serializes the lifecycle, document and change-control declarations.
func (n NodeType) MetaJSON() []byte {
	b, _ := json.Marshal(nodeTypeMeta{Lifecycle: n.Lifecycle, Document: n.Document, ChangeControlled: n.ChangeControlled})
	return b
}

// SetMeta restores what MetaJSON stored.
func (n *NodeType) SetMeta(raw []byte) {
	var m nodeTypeMeta
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return
	}
	n.Lifecycle, n.Document, n.ChangeControlled = m.Lifecycle, m.Document, m.ChangeControlled
}
