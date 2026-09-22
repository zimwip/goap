// Package methodology defines the declarative format of an enterprise
// methodology (domain schema, conditions, actions, goals) and compiles it
// into planner inputs.
package methodology

import (
	"bytes"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/goap"
)

// Methodology is the declarative definition deployed in the registry.
type Methodology struct {
	Name        string      `yaml:"name" json:"name"`
	Version     string      `yaml:"version" json:"version"`
	Description string      `yaml:"description,omitempty" json:"description,omitempty"`
	Domain      Schema      `yaml:"domain" json:"domain"`
	Conditions  []Condition `yaml:"conditions" json:"conditions"`
	Actions     []Action    `yaml:"actions" json:"actions"`
	Goals       []Goal      `yaml:"goals" json:"goals"`
}

// Schema is the domain model of the methodology.
type Schema struct {
	NodeTypes []NodeType `yaml:"nodeTypes" json:"nodeTypes"`
	LinkTypes []LinkType `yaml:"linkTypes" json:"linkTypes"`
}

// NodeType is a domain node type.
type NodeType struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Properties  []string `yaml:"properties,omitempty" json:"properties,omitempty"`
}

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
}

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
	nodeTypes := map[string]bool{}
	for i, n := range m.Domain.NodeTypes {
		path := fmt.Sprintf("domain.nodeTypes[%d]", i)
		if n.Name == "" {
			add(path+".name", "name required")
		} else if nodeTypes[n.Name] {
			add(path+".name", "duplicate node type %s", n.Name)
		}
		nodeTypes[n.Name] = true
	}
	linkTypes := map[string]bool{}
	for i, l := range m.Domain.LinkTypes {
		path := fmt.Sprintf("domain.linkTypes[%d]", i)
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
		if !slices.Contains([]string{KindLLM, KindTool, KindHuman, KindBuiltin}, a.Kind) {
			add(path+".kind", "unknown kind %q", a.Kind)
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
		if len(a.Effects) == 0 {
			add(path+".effects", "no effect: the action can never be planned")
		}
		actions[a.Name] = a
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
	if len(issues) > 0 {
		sort.SliceStable(issues, func(i, j int) bool { return issues[i].Path < issues[j].Path })
		return nil, issues
	}
	set, err := condition.Compile(defs)
	if err != nil {
		return nil, Issues{{Message: err.Error()}}
	}
	return &Compiled{Methodology: m, Conditions: set, actions: actions}, nil
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

// PlanningActions returns the planner operators.
func (c *Compiled) PlanningActions() []goap.Action {
	out := make([]goap.Action, 0, len(c.Actions))
	for _, src := range c.Actions {
		a := c.actions[src.Name]
		out = append(out, goap.Action{Name: a.Name, Pre: a.Pre, Effects: a.Effects, Cost: a.Cost})
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
