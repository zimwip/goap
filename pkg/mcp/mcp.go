// Package mcp holds the definition of an MCP, the generic usage of a tool by an LLM: a name and tool
// signatures (document-repository: list, read, write), with no infrastructure dependency (ADR 0019).
// It knows no connector, no adapter and no organisation: the adapter, where they meet, is pkg/adapter
// (ADR 0061); the schemas of the built-in MCPs are in pkg/mcpbuiltin.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// NodeTypeMCP is the type of the node of an MCP (namespace platform).
const NodeTypeMCP = "platform@MCP"

// MCPKey is the key of the node of an MCP.
func MCPKey(name string) string { return "MCP:" + name }

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

func viaJSON(from, to any) error {
	b, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, to)
}
