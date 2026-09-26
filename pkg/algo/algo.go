// Package algo defines the algorithms of a domain (ADR 0018): scripts written in
// JavaScript or Go that plug into the model at fixed extension points. An
// algorithm has a Type (one of the fixed usages, which decides the DSL context
// its script runs in and the places it can be plugged), a script and a list of
// typed parameters; an Instance sets the parameter values and is what gets
// plugged. The scripts run through pkg/dsl.
package algo

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
)

// Usage is the fixed list of algorithm types: the extension points of the platform.
type Usage string

// Usages.
const (
	// UsageAction: the script of a `kind: script` action. The code stays bound
	// to the agent declaration: there are no algorithms of this type.
	UsageAction Usage = "action"
	// UsagePropertyValidator checks the value of a property of a node.
	UsagePropertyValidator Usage = "property_validator"
	// UsageTransitionGuard decides whether a lifecycle transition is allowed.
	UsageTransitionGuard Usage = "transition_guard"
	// UsageTransitionAction runs when a lifecycle transition is taken and may
	// change properties of the node.
	UsageTransitionAction Usage = "transition_action"
	// UsageAdapter implements the tools of an MCP with the operations of a connector (ADR
	// 0019): the code maps the expected functions onto the exposed ones. It is declared in the
	// domain library and instantiated, with parameter values, by the organisational units.
	// It is never plugged into a node type or a lifecycle.
	UsageAdapter Usage = "adapter"
)

// Usages lists every usage.
func Usages() []Usage {
	return []Usage{UsageAction, UsagePropertyValidator, UsageTransitionGuard, UsageTransitionAction, UsageAdapter}
}

// Pluggable tells whether algorithms of this type can be declared and plugged.
func (u Usage) Pluggable() bool { return u != UsageAction && u.Known() }

// Known tells whether u is one of the usages.
func (u Usage) Known() bool { return slices.Contains(Usages(), u) }

// Languages of the scripts.
const (
	JavaScript = "javascript"
	Go         = "go"
)

// Param types.
const (
	ParamString  = "string"
	ParamNumber  = "number"
	ParamBoolean = "boolean"
	// ParamRegex is a regular expression (Go / RE2 syntax), checked when the instance is saved.
	ParamRegex = "regex"
	// ParamEnum is one of Param.Values.
	ParamEnum = "enum"
	// ParamStrings is a list of strings.
	ParamStrings = "strings"
	// ParamJSON is any JSON value.
	ParamJSON = "json"
	// ParamSecret is a reference to a secret ("<vault path>#<field>" or "env:<VAR>"). The script
	// never reads it: adapters hand the resolved secret to the connector under the parameter name.
	ParamSecret = "secret"
)

// ParamTypes lists the parameter types.
func ParamTypes() []string {
	return []string{ParamString, ParamNumber, ParamBoolean, ParamRegex, ParamEnum, ParamStrings, ParamJSON, ParamSecret}
}

// Param declares a parameter of an algorithm: a typed value the instance sets
// and the script reads with ctx.param(name).
type Param struct {
	Name        string `yaml:"name" json:"name"`
	Type        string `yaml:"type" json:"type"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Required    bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Default     any    `yaml:"default,omitempty" json:"default,omitempty"`
	// Values are the allowed values of an enum.
	Values []string `yaml:"values,omitempty" json:"values,omitempty"`
}

// Algorithm is a reusable script of a fixed type with declared parameters.
type Algorithm struct {
	Name        string  `yaml:"name" json:"name"`
	Description string  `yaml:"description,omitempty" json:"description,omitempty"`
	Type        Usage   `yaml:"type" json:"type"`
	Language    string  `yaml:"language" json:"language"`
	Code        string  `yaml:"code" json:"code"`
	Params      []Param `yaml:"params,omitempty" json:"params,omitempty"`
	// MCP and Connector (adapters only): the name of the MCP whose tools the code implements and
	// the id of the connector whose operations it calls.
	MCP       string `yaml:"mcp,omitempty" json:"mcp,omitempty"`
	Connector string `yaml:"connector,omitempty" json:"connector,omitempty"`
}

// Instance sets the parameter values of an algorithm. Instances are what the
// domain plugs where the algorithm's type is allowed.
type Instance struct {
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	Algorithm   string         `yaml:"algorithm" json:"algorithm"`
	Values      map[string]any `yaml:"values,omitempty" json:"values,omitempty"`
}

// Bound is an instance resolved with its algorithm: everything needed to run
// it, so that the graph holds it inline and needs no other lookup (ADR 0014).
type Bound struct {
	Instance  string         `json:"instance"`
	Algorithm string         `json:"algorithm"`
	Type      Usage          `json:"type"`
	Language  string         `json:"language"`
	Code      string         `json:"code"`
	Params    map[string]any `json:"params,omitempty"`
	// Property is the property a validator applies to.
	Property string `json:"property,omitempty"`
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// ValidName tells whether s is a valid algorithm or instance name.
func ValidName(s string) bool { return nameRE.MatchString(s) }

var paramNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// Issues checks the declaration of an algorithm (not its script: pkg/dsl compiles it).
func (a Algorithm) Issues() []string {
	var out []string
	switch {
	case a.Name == "":
		out = append(out, "name required")
	case !ValidName(a.Name):
		out = append(out, "name must be lowercase letters, digits, '-' or '_' and start with a letter")
	}
	switch {
	case !a.Type.Known():
		out = append(out, fmt.Sprintf("unknown type %q", a.Type))
	case !a.Type.Pluggable():
		out = append(out, fmt.Sprintf("type %s has no algorithms: the code stays in the action declaration", a.Type))
	}
	if a.Language != JavaScript && a.Language != Go {
		out = append(out, "language must be javascript or go")
	}
	if a.Type == UsageAdapter {
		if !nameRE.MatchString(a.MCP) {
			out = append(out, "an adapter names the MCP it implements (mcp)")
		}
		if !nameRE.MatchString(a.Connector) {
			out = append(out, "an adapter names the connector it calls (connector)")
		}
	} else if a.MCP != "" || a.Connector != "" {
		out = append(out, "mcp and connector apply to adapters only")
	}
	if a.Code == "" {
		out = append(out, "code required")
	}
	seen := map[string]bool{}
	for i, p := range a.Params {
		switch {
		case p.Name == "":
			out = append(out, fmt.Sprintf("param %d has no name", i))
			continue
		case !paramNameRE.MatchString(p.Name):
			out = append(out, fmt.Sprintf("param %s: name must be letters, digits or '_' and start with a letter", p.Name))
		case seen[p.Name]:
			out = append(out, fmt.Sprintf("duplicate param %s", p.Name))
		}
		seen[p.Name] = true
		if !slices.Contains(ParamTypes(), p.Type) {
			out = append(out, fmt.Sprintf("param %s: unknown type %q", p.Name, p.Type))
			continue
		}
		if p.Type == ParamSecret && a.Type != UsageAdapter {
			out = append(out, fmt.Sprintf("param %s: secrets are for adapters only", p.Name))
		}
		if p.Type == ParamEnum && len(p.Values) == 0 {
			out = append(out, fmt.Sprintf("param %s: an enum needs values", p.Name))
		}
		if p.Default != nil {
			if _, err := p.Coerce(p.Default); err != nil {
				out = append(out, fmt.Sprintf("param %s: default: %v", p.Name, err))
			}
		}
	}
	return out
}

// Coerce checks v against the parameter type and returns it in its canonical
// form (numbers as float64, lists as []any of strings…).
func (p Param) Coerce(v any) (any, error) {
	switch p.Type {
	case ParamString:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected a string, got %T", v)
		}
		return s, nil
	case ParamSecret:
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("expected a secret reference, got %T", v)
		}
		return s, nil
	case ParamRegex:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected a regular expression, got %T", v)
		}
		if _, err := regexp.Compile(s); err != nil {
			return nil, fmt.Errorf("invalid regular expression: %v", err)
		}
		return s, nil
	case ParamNumber:
		switch n := v.(type) {
		case float64:
			return n, nil
		case int:
			return float64(n), nil
		case int32:
			return float64(n), nil
		case int64:
			return float64(n), nil
		case string:
			if f, err := strconv.ParseFloat(n, 64); err == nil {
				return f, nil
			}
		}
		return nil, fmt.Errorf("expected a number, got %v", v)
	case ParamBoolean:
		switch b := v.(type) {
		case bool:
			return b, nil
		case string:
			if x, err := strconv.ParseBool(b); err == nil {
				return x, nil
			}
		}
		return nil, fmt.Errorf("expected a boolean, got %v", v)
	case ParamEnum:
		s, ok := v.(string)
		if !ok || !slices.Contains(p.Values, s) {
			return nil, fmt.Errorf("expected one of %v, got %v", p.Values, v)
		}
		return s, nil
	case ParamStrings:
		var out []any
		switch l := v.(type) {
		case []string:
			for _, s := range l {
				out = append(out, s)
			}
		case []any:
			for _, x := range l {
				s, ok := x.(string)
				if !ok {
					return nil, fmt.Errorf("expected a list of strings, got element %v", x)
				}
				out = append(out, s)
			}
		default:
			return nil, fmt.Errorf("expected a list of strings, got %T", v)
		}
		if out == nil {
			out = []any{}
		}
		return out, nil
	case ParamJSON:
		return v, nil
	}
	return nil, fmt.Errorf("unknown param type %q", p.Type)
}

// Resolve returns the parameter values an instance gives the script: its
// values checked against the declared params, defaults filled in. Values for
// undeclared params are refused.
func (a Algorithm) Resolve(values map[string]any) (map[string]any, []string) {
	var issues []string
	out := map[string]any{}
	declared := map[string]bool{}
	for _, p := range a.Params {
		declared[p.Name] = true
		v, set := values[p.Name]
		if !set || v == nil {
			if p.Default != nil {
				v, set = p.Default, true
			}
		}
		if !set || v == nil {
			if p.Required {
				issues = append(issues, fmt.Sprintf("param %s is required", p.Name))
			}
			continue
		}
		c, err := p.Coerce(v)
		if err != nil {
			issues = append(issues, fmt.Sprintf("param %s: %v", p.Name, err))
			continue
		}
		out[p.Name] = c
	}
	for k := range values {
		if !declared[k] {
			issues = append(issues, fmt.Sprintf("unknown param %s", k))
		}
	}
	slices.Sort(issues)
	return out, issues
}

// Split separates the resolved values of an adapter into the configuration handed to the
// connector (every parameter but the secrets) and the secret references, by parameter name.
func (a Algorithm) Split(values map[string]any) (config map[string]any, secrets map[string]string) {
	config, secrets = map[string]any{}, map[string]string{}
	secret := map[string]bool{}
	for _, p := range a.Params {
		secret[p.Name] = p.Type == ParamSecret
	}
	for k, v := range values {
		if secret[k] {
			if s, ok := v.(string); ok {
				secrets[k] = s
			}
			continue
		}
		config[k] = v
	}
	return config, secrets
}

// Set is the algorithms and instances of a domain.
type Set struct {
	Algorithms []Algorithm
	Instances  []Instance
}

// Algorithm returns the named algorithm.
func (s Set) Algorithm(name string) (Algorithm, bool) {
	for _, a := range s.Algorithms {
		if a.Name == name {
			return a, true
		}
	}
	return Algorithm{}, false
}

// Instance returns the named instance.
func (s Set) Instance(name string) (Instance, bool) {
	for _, i := range s.Instances {
		if i.Name == name {
			return i, true
		}
	}
	return Instance{}, false
}

// Bind resolves an instance that must be of type want.
func (s Set) Bind(name string, want Usage) (Bound, error) {
	inst, ok := s.Instance(name)
	if !ok {
		return Bound{}, fmt.Errorf("unknown algorithm instance %s", name)
	}
	alg, ok := s.Algorithm(inst.Algorithm)
	if !ok {
		return Bound{}, fmt.Errorf("instance %s: unknown algorithm %s", name, inst.Algorithm)
	}
	if alg.Type != want {
		return Bound{}, fmt.Errorf("instance %s is of type %s, expected %s", name, alg.Type, want)
	}
	params, issues := alg.Resolve(inst.Values)
	if len(issues) > 0 {
		return Bound{}, fmt.Errorf("instance %s: %s", name, issues[0])
	}
	return Bound{Instance: name, Algorithm: alg.Name, Type: alg.Type, Language: alg.Language, Code: alg.Code, Params: params}, nil
}

// Clone returns a deep copy of the bound algorithm.
func (b Bound) Clone() Bound {
	c := b
	if b.Params != nil {
		c.Params = make(map[string]any, len(b.Params))
		for k, v := range b.Params {
			c.Params[k] = v
		}
	}
	return c
}
