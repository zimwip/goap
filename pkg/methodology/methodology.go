// Package methodology defines the declarative format of an enterprise
// methodology (domain schema, conditions, actions, goals) and compiles it
// into planner inputs.
package methodology

import (
	"bytes"
	"fmt"
	"slices"

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

// Compile validates the methodology and compiles its conditions, including
// the conditions generated from action expectations.
func (m *Methodology) Compile() (*Compiled, error) {
	if m.Name == "" {
		return nil, fmt.Errorf("methodology name required")
	}
	if len(m.Goals) == 0 {
		return nil, fmt.Errorf("methodology %s: at least one goal required", m.Name)
	}
	nodeTypes := map[string]bool{}
	for _, n := range m.Domain.NodeTypes {
		nodeTypes[n.Name] = true
	}
	linkTypes := map[string]bool{}
	for _, l := range m.Domain.LinkTypes {
		linkTypes[l.Name] = true
		for _, end := range []string{l.From, l.To} {
			if end != "" && !nodeTypes[end] {
				return nil, fmt.Errorf("link type %s references unknown node type %s", l.Name, end)
			}
		}
	}

	defs := make([]condition.Definition, 0, len(m.Conditions)+len(m.Actions))
	for _, c := range m.Conditions {
		defs = append(defs, condition.Definition{Name: c.Name, Expr: c.Expr})
	}
	actions := map[string]Action{}
	for i := range m.Actions {
		a := &m.Actions[i]
		if a.Name == "" {
			return nil, fmt.Errorf("action #%d without name", i)
		}
		if _, dup := actions[a.Name]; dup {
			return nil, fmt.Errorf("duplicate action %s", a.Name)
		}
		if !slices.Contains([]string{KindLLM, KindTool, KindHuman, KindBuiltin}, a.Kind) {
			return nil, fmt.Errorf("action %s: unknown kind %q", a.Name, a.Kind)
		}
		switch {
		case a.Kind == KindLLM && a.Prompt == "":
			return nil, fmt.Errorf("action %s: llm action requires a prompt", a.Name)
		case a.Kind == KindTool && a.Tool == "":
			return nil, fmt.Errorf("action %s: tool action requires a tool", a.Name)
		case a.Kind == KindBuiltin && a.Builtin == "":
			return nil, fmt.Errorf("action %s: builtin action requires a builtin", a.Name)
		}
		if e := a.Expects; e != nil {
			if e.Produce.NodeType != "" && len(nodeTypes) > 0 && !nodeTypes[e.Produce.NodeType] {
				return nil, fmt.Errorf("action %s: expects unknown node type %s", a.Name, e.Produce.NodeType)
			}
			if e.Link != nil && len(linkTypes) > 0 && !linkTypes[e.Link.Type] {
				return nil, fmt.Errorf("action %s: expects unknown link type %s", a.Name, e.Link.Type)
			}
			expr, err := e.Expr()
			if err != nil {
				return nil, fmt.Errorf("action %s: %w", a.Name, err)
			}
			defs = append(defs, condition.Definition{Name: a.ExpectCondition(), Expr: expr})
			if a.Effects == nil {
				a.Effects = map[string]bool{}
			}
			a.Effects[a.ExpectCondition()] = true
		}
		if len(a.Effects) == 0 {
			return nil, fmt.Errorf("action %s: no effect, it can never be planned", a.Name)
		}
		actions[a.Name] = *a
	}
	set, err := condition.Compile(defs)
	if err != nil {
		return nil, fmt.Errorf("methodology %s: %w", m.Name, err)
	}
	check := func(owner string, conds map[string]bool) error {
		for k := range conds {
			if !set.Has(k) {
				return fmt.Errorf("%s references unknown condition %q", owner, k)
			}
		}
		return nil
	}
	for _, a := range m.Actions {
		if err := check("action "+a.Name, a.Pre); err != nil {
			return nil, err
		}
		if err := check("action "+a.Name, a.Effects); err != nil {
			return nil, err
		}
	}
	for _, g := range m.Goals {
		if len(g.Pre) == 0 {
			return nil, fmt.Errorf("goal %s has no precondition", g.Name)
		}
		if err := check("goal "+g.Name, g.Pre); err != nil {
			return nil, err
		}
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
	for _, a := range c.Actions {
		out = append(out, goap.Action{Name: a.Name, Pre: a.Pre, Effects: a.Effects, Cost: a.Cost})
	}
	return out
}

// PlanningGoal returns the planner goal.
func (g Goal) PlanningGoal() goap.Goal {
	return goap.Goal{Name: g.Name, Pre: g.Pre, Value: g.Value}
}
