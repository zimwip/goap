// Package mcp holds the model of the tool layer, with no infrastructure dependency (ADR 0019).
//
//	MCP        generic usage of a tool by an LLM: name + tool signatures (document-repository: list, read, write)
//	Connector  a separately deployed service wrapping a real API, exposing its own operations (localfs: list_dir, read_file, ...)
//	Adapter    code that implements the tools an MCP expects with the operations a connector exposes: an
//	           algorithm of the domain library (usage `adapter`), instantiated by an organisational unit with
//	           its parameter values (the Adapter type of this package is that instance)
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Types and namespaces of the graph objects.
const (
	NamespacePlatform     = "platform"
	NamespaceOrganisation = "organisation"
	NodeTypeMCP           = "MCP"
	NodeTypeAdapter       = "Adapter"
	NodeTypeOrgUnit       = "OrgUnit"
	LinkOwner             = "owner"
	LinkPartOf            = "part_of"
)

// MCPKey is the key of the node of an MCP.
func MCPKey(name string) string { return "MCP:" + name }

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
}

// Def is a generic MCP definition.
type Def struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Tools       []Tool `json:"tools"`
}

// Adapter is the instance of an adapter algorithm of the library, with the parameter values of one
// organisational unit: the one place where unit, MCP and connector meet. The connector and the
// code come from the algorithm; the unit gives the values (root directory, account, secret
// references). It is an Adapter node of the "organisation" namespace linked to its unit by `owner`.
type Adapter struct {
	// Unit is the key of the OrgUnit the adapter belongs to (from its `owner` link).
	Unit string `json:"unit,omitempty"`
	// MCP is the name of the MCP of the platform namespace (the algorithm implements the same).
	MCP string `json:"mcp"`
	// Domain and Version locate the algorithm in the library; an empty version is the latest published.
	Domain  string `json:"domain"`
	Version string `json:"version,omitempty"`
	// Algorithm is the name of the adapter algorithm in the domain.
	Algorithm string `json:"algorithm"`
	// Params are the parameter values (secrets as references).
	Params map[string]any `json:"params,omitempty"`
}

// ToolInfo is a tool available to an organization, under its qualified name.
type ToolInfo struct {
	Name        string         `json:"name"` // "<mcp>/<tool>"
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
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

// Validate checks the instance against the MCP it says it implements.
func (a Adapter) Validate(def Def) error {
	if a.MCP != def.Name {
		return fmt.Errorf("adapter of %s validated against %s: %w", a.MCP, def.Name, ErrInvalid)
	}
	if !ValidName(a.Domain) || !ValidName(a.Algorithm) {
		return fmt.Errorf("adapter %s: domain and algorithm must be lowercase names: %w", a.MCP, ErrInvalid)
	}
	return nil
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
