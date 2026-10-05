// Package adapter holds the model of the adapter, the one place where organisation, MCP and connector
// meet (ADR 0019, 0061).
//
//	Def       code that implements the tools an MCP expects with the operations a connector exposes: an
//	          AdapterDef node of the platform namespace (domain.TypeAdapterDef, changed through a change),
//	          its parameters typed like those of pkg/algo, plus the secret references only an adapter has
//	Instance  the instance of a Def by an organisational unit, with its parameter values and the
//	          restrictions it puts on the MCP (Adapter node of the organisation namespace, domain.TypeAdapter)
//
// It imports the definition of the MCP (pkg/mcp) and the generic algorithm model (pkg/algo); neither
// knows it. The scripts run through pkg/dsl.
package adapter

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/mcp"
)

// ParamSecret is the parameter type of a reference to a secret ("<vault path>#<field>" or
// "env:<VAR>"). Only an adapter has them: the script never reads one, the hub hands the resolved
// secret to the connector under the parameter name.
const ParamSecret = "secret"

// DefKey is the key of the node of an adapter definition.
func DefKey(name string) string { return "ADD:" + name }

// Key is the key of the Adapter node of an MCP in a unit.
func Key(unit, mcp string) string { return "ADP:" + unit + "/" + mcp }

// Def defines an adapter: code that implements the tools of one MCP with the operations of one
// connector, and the parameters an instance sets. It is an AdapterDef node of the "platform" namespace.
type Def struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// MCP is the name of the MCP whose tools the code implements; Connector the id of the connector it calls.
	MCP       string       `json:"mcp"`
	Connector string       `json:"connector"`
	Language  string       `json:"language"`
	Code      string       `json:"code"`
	Params    []algo.Param `json:"params,omitempty"`
}

// Instance is the instance of a Def with the parameter values of one organisational unit: the one
// place where unit, MCP and connector meet. The connector and the code come from the definition; the
// unit gives the values (root directory, account, secret references). It is an Adapter node of the
// "organisation" namespace owned by its unit.
type Instance struct {
	// Unit is the key of the OrgUnit the adapter belongs to (the owner of its version).
	Unit string `json:"unit,omitempty"`
	// MCP is the name of the MCP of the platform namespace (the definition implements the same).
	MCP string `json:"mcp"`
	// Adapter is the name of the Def.
	Adapter string `json:"adapter"`
	// Params are the parameter values (secrets as references).
	Params map[string]any `json:"params,omitempty"`
	// Restrictions of the MCP for the unit and its sub-units (ADR 0028). They add up along the unit
	// chain: a unit can narrow what its ancestors allow, never widen it. An instance may only restrict
	// (no Adapter): the implementation is then the one of the nearest ancestor.
	//
	// Disabled removes the MCP; Tools, when not empty, lists the only tools allowed; Deny lists
	// tools refused; ReadOnly keeps only the tools marked read-only.
	Disabled bool     `json:"disabled,omitempty"`
	Tools    []string `json:"tools,omitempty"`
	Deny     []string `json:"deny,omitempty"`
	ReadOnly bool     `json:"readOnly,omitempty"`
}

// Validate checks the instance against the MCP it says it implements: an instance names an adapter
// definition unless it only restricts the MCP, and restricts only tools the MCP has.
func (a Instance) Validate(def mcp.Def) error {
	if a.MCP != def.Name {
		return fmt.Errorf("adapter of %s validated against %s: %w", a.MCP, def.Name, mcp.ErrInvalid)
	}
	if a.Adapter == "" && !a.Restricts() {
		return fmt.Errorf("adapter %s: name an adapter definition or restrict the MCP: %w", a.MCP, mcp.ErrInvalid)
	}
	if a.Adapter != "" && !mcp.ValidName(a.Adapter) {
		return fmt.Errorf("adapter %s: adapter must be the lowercase name of an adapter definition: %w", a.MCP, mcp.ErrInvalid)
	}
	for _, t := range append(slices.Clone(a.Tools), a.Deny...) {
		if _, ok := def.Tool(t); !ok {
			return fmt.Errorf("adapter %s: the MCP has no tool %q: %w", a.MCP, t, mcp.ErrInvalid)
		}
	}
	return nil
}

// Implements reports whether the instance names an adapter definition (else it only restricts).
func (a Instance) Implements() bool { return a.Adapter != "" }

// Restricts reports whether the instance restricts the MCP.
func (a Instance) Restricts() bool {
	return a.Disabled || len(a.Tools) > 0 || len(a.Deny) > 0 || a.ReadOnly
}

// Validate checks a definition (not its script: pkg/dsl compiles it).
func (d Def) Validate() error {
	if !mcp.ValidName(d.Name) {
		return fmt.Errorf("adapter definition name %q must be a lowercase name: %w", d.Name, mcp.ErrInvalid)
	}
	var issues []string
	if !mcp.ValidName(d.MCP) {
		issues = append(issues, "an adapter names the MCP it implements (mcp)")
	}
	if !mcp.ValidName(d.Connector) {
		issues = append(issues, "an adapter names the connector it calls (connector)")
	}
	issues = append(issues, d.script().Declaration(ParamSecret)...)
	if len(issues) > 0 {
		return fmt.Errorf("adapter definition %s: %s: %w", d.Name, issues[0], mcp.ErrInvalid)
	}
	return nil
}

// script is the definition as a generic algorithm: the declaration checks of pkg/algo apply to it.
func (d Def) script() algo.Algorithm {
	return algo.Algorithm{Name: d.Name, Description: d.Description, Language: d.Language, Code: d.Code, Params: d.Params}
}

// Resolve returns what an instance's values give the adapter: the configuration handed to the
// connector (every parameter but the secrets, checked against the declared params, defaults filled
// in) and the secret references by parameter name. Values for undeclared params are refused.
func (d Def) Resolve(values map[string]any) (config map[string]any, secrets map[string]string, issues []string) {
	plain := d.script()
	plain.Params = nil
	secret := map[string]algo.Param{}
	for _, p := range d.Params {
		if p.Type == ParamSecret {
			secret[p.Name] = p
		} else {
			plain.Params = append(plain.Params, p)
		}
	}
	rest := map[string]any{}
	for k, v := range values {
		if _, ok := secret[k]; !ok {
			rest[k] = v
		}
	}
	config, issues = plain.Resolve(rest)
	secrets = map[string]string{}
	for name, p := range secret {
		switch ref, set := values[name]; {
		case !set || ref == nil:
			if p.Required {
				issues = append(issues, fmt.Sprintf("param %s is required", name))
			}
		default:
			if s, ok := ref.(string); ok && s != "" {
				secrets[name] = s
			} else {
				issues = append(issues, fmt.Sprintf("param %s: expected a secret reference, got %T", name, ref))
			}
		}
	}
	slices.Sort(issues)
	return config, secrets, issues
}

// Props returns the properties of the AdapterDef node.
func (d Def) Props() map[string]any {
	var m map[string]any
	_ = viaJSON(d, &m)
	return m
}

// DefFromProps reads an adapter definition from the properties of its node.
func DefFromProps(props map[string]any) (Def, error) {
	var d Def
	if err := viaJSON(props, &d); err != nil {
		return d, fmt.Errorf("adapter definition node: %w: %w", err, mcp.ErrInvalid)
	}
	return d, nil
}

// Props returns the properties of the Adapter node (the unit is its owner).
func (a Instance) Props() map[string]any {
	a.Unit = ""
	var m map[string]any
	_ = viaJSON(a, &m)
	return m
}

// FromProps reads an adapter instance from the properties of its node and the unit that owns it.
func FromProps(unit string, props map[string]any) (Instance, error) {
	var a Instance
	if err := viaJSON(props, &a); err != nil {
		return a, fmt.Errorf("adapter node: %w: %w", err, mcp.ErrInvalid)
	}
	a.Unit = unit
	return a, nil
}

func viaJSON(from, to any) error {
	b, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, to)
}
