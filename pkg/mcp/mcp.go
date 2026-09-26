// Package mcp holds the model of the tool layer, with no infrastructure
// dependency: generic MCP definitions, the adapters implementing them on a
// connector, and the bindings of an organization (see ADR 0019).
//
//	MCP        generic: name + tool signatures (document-repository: list, read, write)
//	Connector  a separately deployed service wrapping a real API (localfs, gdrive)
//	Adapter    declarative mapping MCP tool -> connector operation
//	Binding    organization: MCP -> (connector, configuration, secret references)
package mcp

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

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

// ToolMapping maps a tool of an MCP onto an operation of a connector.
type ToolMapping struct {
	Tool      string `json:"tool"`
	Operation string `json:"operation"`
	// Arguments are the operation arguments: literals, or "$.a.b" references to the
	// arguments of the tool call (see MapArguments). Nil or empty: the tool arguments as they are.
	Arguments map[string]any `json:"arguments,omitempty"`
	// ResultPath is the dotted path picked in the operation result ("" = all of it).
	ResultPath string `json:"resultPath,omitempty"`
}

// Adapter is the implementation of an MCP by a connector.
type Adapter struct {
	MCP       string        `json:"mcp"`
	Connector string        `json:"connector"`
	Tools     []ToolMapping `json:"tools"`
}

// Binding attaches an MCP to a connector for an organization.
type Binding struct {
	OrgID     string `json:"orgId"`
	MCP       string `json:"mcp"`
	Connector string `json:"connector"`
	// Config is the connector configuration of the organization.
	Config map[string]any `json:"config,omitempty"`
	// Secrets maps a secret name to its reference ("<vault path>#<field>" or "env:<VAR>").
	Secrets map[string]string `json:"secrets,omitempty"`
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

// Validate checks an adapter against the MCP it implements: every mapped tool
// must exist and be mapped once. Tools left unmapped are unavailable through
// this adapter.
func (a Adapter) Validate(def Def) error {
	if !ValidName(a.Connector) {
		return fmt.Errorf("connector name %q must match %s: %w", a.Connector, nameRE, ErrInvalid)
	}
	seen := map[string]bool{}
	for _, m := range a.Tools {
		if _, ok := def.Tool(m.Tool); !ok {
			return fmt.Errorf("adapter %s/%s: mcp %s has no tool %q: %w", a.MCP, a.Connector, a.MCP, m.Tool, ErrInvalid)
		}
		if seen[m.Tool] {
			return fmt.Errorf("adapter %s/%s: tool %s mapped twice: %w", a.MCP, a.Connector, m.Tool, ErrInvalid)
		}
		if m.Operation == "" {
			return fmt.Errorf("adapter %s/%s: tool %s has no operation: %w", a.MCP, a.Connector, m.Tool, ErrInvalid)
		}
		seen[m.Tool] = true
	}
	return nil
}

// Mapping returns the mapping of a tool.
func (a Adapter) Mapping(tool string) (ToolMapping, bool) {
	for _, m := range a.Tools {
		if m.Tool == tool {
			return m, true
		}
	}
	return ToolMapping{}, false
}

// MapArguments builds the operation arguments from the mapping template and the
// tool arguments. In the template a string "$" is the whole set of arguments and
// "$.a.b" the value at that path; a reference to a missing value is left out (an
// optional argument), other strings, numbers and booleans are literals; objects
// and lists are mapped recursively. A nil template passes the arguments through.
func MapArguments(tmpl, args map[string]any) map[string]any {
	if tmpl == nil {
		return args
	}
	out, _ := mapValue(tmpl, args).(map[string]any)
	return out
}

func mapValue(v any, args map[string]any) any {
	switch x := v.(type) {
	case string:
		if x == "$" {
			return args
		}
		if p, ok := strings.CutPrefix(x, "$."); ok {
			if got, ok := lookup(args, p); ok {
				return got
			}
			return missing{}
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if m := mapValue(e, args); m != (missing{}) {
				out[k] = m
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			if m := mapValue(e, args); m != (missing{}) {
				out = append(out, m)
			}
		}
		return out
	}
	return v
}

type missing struct{}

func lookup(v any, path string) (any, bool) {
	for _, seg := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[seg]; !ok {
			return nil, false
		}
	}
	return v, true
}

// Pick returns the value at a dotted path of the result ("" = all of it). A
// non-object value found by the path is wrapped as {"value": v}, so that a
// result is always an object.
func Pick(result map[string]any, path string) (map[string]any, error) {
	if path == "" {
		return result, nil
	}
	v, ok := lookup(result, path)
	if !ok {
		return nil, fmt.Errorf("result has no %q", path)
	}
	if m, ok := v.(map[string]any); ok {
		return m, nil
	}
	return map[string]any{"value": v}, nil
}
