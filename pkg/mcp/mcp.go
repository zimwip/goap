// Package mcp holds the model of the tool layer, with no infrastructure dependency (ADR 0019).
//
//	MCP        generic usage of a tool by an LLM: name + tool signatures (document-repository: list, read, write)
//	Connector  a separately deployed service wrapping a real API, exposing its own operations (localfs: list_dir, read_file, ...)
//	AdapterDef code that implements the tools an MCP expects with the operations a connector exposes: an
//	           AdapterDef node of the platform namespace (changed through a change), usage `adapter` of pkg/algo
//	Adapter    the instance of an AdapterDef by an organisational unit, with its parameter values (Adapter node
//	           of the organisation namespace)
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/algo"
)

// Types and namespaces of the graph objects.
const (
	NamespacePlatform     = "platform"
	NamespaceOrganisation = "organisation"
	NodeTypeMCP           = "platform@MCP"
	NodeTypeAdapter       = "organisation@Adapter"
	NodeTypeAdapterDef    = "platform@AdapterDef"
	NodeTypeOrgUnit       = "organisation@OrgUnit"
	LinkOwner             = "organisation@owner"
	LinkPartOf            = "organisation@part_of"
)

// MCPKey is the key of the node of an MCP.
func MCPKey(name string) string { return "MCP:" + name }

// AdapterDefKey is the key of the node of an adapter definition.
func AdapterDefKey(name string) string { return "ADD:" + name }

// AdapterKey is the key of the Adapter node of an MCP in a unit.
func AdapterKey(unit, mcp string) string { return "ADP:" + unit + "/" + mcp }

// ErrInvalid marks a malformed definition.
var ErrInvalid = errors.New("invalid")

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// Tool is one tool of an MCP.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	// ReadOnly marks a tool that changes nothing (the MCP readOnlyHint): a unit can keep only these.
	ReadOnly bool `json:"readOnly,omitempty"`
}

// Scopes of an MCP: where a methodology may use it.
const (
	// ScopeAction: declared by actions (`actions[].mcps`, a tool action's `<mcp>/<tool>`).
	ScopeAction = "action"
	// ScopeAgent: declared by agents only (`agents[].mcps`), for the llm actions that work at the level
	// of the agent: orchestration tools (start other agents) are not a step's business.
	ScopeAgent = "agent"
	// ScopeBoth: either (the default, empty).
	ScopeBoth = "both"
)

// Def is a generic MCP definition.
type Def struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Scope says where the MCP may be used: action, agent or both (empty: both).
	Scope string `json:"scope,omitempty"`
	Tools []Tool `json:"tools"`
}

// ScopeOf normalizes a scope (empty: both).
func ScopeOf(scope string) string {
	if scope == "" {
		return ScopeBoth
	}
	return scope
}

// ForActions reports whether an MCP of this scope may be declared by actions.
func ForActions(scope string) bool { return ScopeOf(scope) != ScopeAgent }

// ForAgents reports whether an MCP of this scope may be declared by agents.
func ForAgents(scope string) bool { return ScopeOf(scope) != ScopeAction }

// AdapterDef defines an adapter: code that implements the tools of one MCP with the operations of one
// connector, and the parameters an instance sets. It is an AdapterDef node of the "platform" namespace.
type AdapterDef struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// MCP is the name of the MCP whose tools the code implements; Connector the id of the connector it calls.
	MCP       string       `json:"mcp"`
	Connector string       `json:"connector"`
	Language  string       `json:"language"`
	Code      string       `json:"code"`
	Params    []algo.Param `json:"params,omitempty"`
}

// Adapter is the instance of an AdapterDef with the parameter values of one organisational unit: the one
// place where unit, MCP and connector meet. The connector and the code come from the definition; the
// unit gives the values (root directory, account, secret references). It is an Adapter node of the
// "organisation" namespace linked to its unit by `owner`.
type Adapter struct {
	// Unit is the key of the OrgUnit the adapter belongs to (from its `owner` link).
	Unit string `json:"unit,omitempty"`
	// MCP is the name of the MCP of the platform namespace (the definition implements the same).
	MCP string `json:"mcp"`
	// Adapter is the name of the AdapterDef.
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

// ToolInfo is a tool available to an organization, under its qualified name.
type ToolInfo struct {
	Name        string         `json:"name"` // "<mcp>/<tool>"
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	ReadOnly    bool           `json:"readOnly,omitempty"`
	// Scope is the scope of the MCP of the tool (empty: both).
	Scope string `json:"scope,omitempty"`
}

// ToolName is the qualified name of a tool: "<mcp>/<tool>".
func ToolName(mcp, tool string) string { return mcp + "/" + tool }

// SplitTool splits "<mcp>/<tool>".
func SplitTool(name string) (mcp, tool string, err error) {
	mcp, tool, ok := strings.Cut(name, "/")
	if !ok || mcp == "" || tool == "" {
		return "", "", fmt.Errorf("tool %q: expected <mcp>/<tool>: %w", name, ErrInvalid)
	}
	return mcp, tool, nil
}

// ValidName reports whether s is a valid MCP, tool or connector name.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// Validate checks a definition.
func (d Def) Validate() error {
	if !ValidName(d.Name) {
		return fmt.Errorf("mcp name %q must match %s: %w", d.Name, nameRE, ErrInvalid)
	}
	switch d.Scope {
	case "", ScopeAction, ScopeAgent, ScopeBoth:
	default:
		return fmt.Errorf("mcp %s: scope %q must be action, agent or both: %w", d.Name, d.Scope, ErrInvalid)
	}
	seen := map[string]bool{}
	for _, t := range d.Tools {
		if !ValidName(t.Name) {
			return fmt.Errorf("mcp %s: tool name %q must match %s: %w", d.Name, t.Name, nameRE, ErrInvalid)
		}
		if seen[t.Name] {
			return fmt.Errorf("mcp %s: duplicate tool %s: %w", d.Name, t.Name, ErrInvalid)
		}
		seen[t.Name] = true
	}
	return nil
}

// Tool returns the named tool.
func (d Def) Tool(name string) (Tool, bool) {
	for _, t := range d.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Validate checks the instance against the MCP it says it implements: an instance names an adapter
// definition unless it only restricts the MCP, and restricts only tools the MCP has.
func (a Adapter) Validate(def Def) error {
	if a.MCP != def.Name {
		return fmt.Errorf("adapter of %s validated against %s: %w", a.MCP, def.Name, ErrInvalid)
	}
	if a.Adapter == "" && !a.Restricts() {
		return fmt.Errorf("adapter %s: name an adapter definition or restrict the MCP: %w", a.MCP, ErrInvalid)
	}
	if a.Adapter != "" && !ValidName(a.Adapter) {
		return fmt.Errorf("adapter %s: adapter must be the lowercase name of an adapter definition: %w", a.MCP, ErrInvalid)
	}
	for _, t := range append(slices.Clone(a.Tools), a.Deny...) {
		if _, ok := def.Tool(t); !ok {
			return fmt.Errorf("adapter %s: the MCP has no tool %q: %w", a.MCP, t, ErrInvalid)
		}
	}
	return nil
}

// Implements reports whether the instance names an adapter definition (else it only restricts).
func (a Adapter) Implements() bool { return a.Adapter != "" }

// Restricts reports whether the instance restricts the MCP.
func (a Adapter) Restricts() bool {
	return a.Disabled || len(a.Tools) > 0 || len(a.Deny) > 0 || a.ReadOnly
}

// Algorithm is the definition as an algorithm of usage adapter.
func (d AdapterDef) Algorithm() algo.Algorithm {
	return algo.Algorithm{Name: d.Name, Description: d.Description, Type: algo.UsageAdapter, Language: d.Language, Code: d.Code,
		Params: d.Params, MCP: d.MCP, Connector: d.Connector}
}

// Validate checks a definition (not its script: pkg/dsl compiles it).
func (d AdapterDef) Validate() error {
	if !ValidName(d.Name) {
		return fmt.Errorf("adapter definition name %q must match %s: %w", d.Name, nameRE, ErrInvalid)
	}
	if issues := d.Algorithm().Issues(); len(issues) > 0 {
		return fmt.Errorf("adapter definition %s: %s: %w", d.Name, issues[0], ErrInvalid)
	}
	return nil
}

// Props returns the properties of the AdapterDef node.
func (d AdapterDef) Props() map[string]any {
	var m map[string]any
	_ = viaJSON(d, &m)
	return m
}

// AdapterDefFromProps reads an adapter definition from the properties of its node.
func AdapterDefFromProps(props map[string]any) (AdapterDef, error) {
	var d AdapterDef
	if err := viaJSON(props, &d); err != nil {
		return d, fmt.Errorf("adapter definition node: %w: %w", err, ErrInvalid)
	}
	return d, nil
}

func viaJSON(from, to any) error {
	b, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, to)
}

// Props returns the properties of the MCP node.
func (d Def) Props() map[string]any {
	var m map[string]any
	_ = viaJSON(d, &m)
	return m
}

// DefFromProps reads an MCP definition from the properties of its node.
func DefFromProps(props map[string]any) (Def, error) {
	var d Def
	if err := viaJSON(props, &d); err != nil {
		return d, fmt.Errorf("mcp node: %w: %w", err, ErrInvalid)
	}
	return d, nil
}

// Props returns the properties of the Adapter node (the unit is carried by its owner link).
func (a Adapter) Props() map[string]any {
	a.Unit = ""
	var m map[string]any
	_ = viaJSON(a, &m)
	return m
}

// AdapterFromProps reads an adapter instance from the properties of its node and the unit that owns it.
func AdapterFromProps(unit string, props map[string]any) (Adapter, error) {
	var a Adapter
	if err := viaJSON(props, &a); err != nil {
		return a, fmt.Errorf("adapter node: %w: %w", err, ErrInvalid)
	}
	a.Unit = unit
	return a, nil
}

// CheckArgs checks the arguments of a call against the tool's input schema: the required
// properties must be present.
func (t Tool) CheckArgs(args map[string]any) error {
	req, _ := t.InputSchema["required"].([]any)
	for _, r := range req {
		if name, _ := r.(string); name != "" {
			if _, ok := args[name]; !ok {
				return fmt.Errorf("tool %s: missing argument %q: %w", t.Name, name, ErrInvalid)
			}
		}
	}
	return nil
}
