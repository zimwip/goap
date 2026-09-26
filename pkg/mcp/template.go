package mcp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zimwip/goap/pkg/algo"
)

// Operation is an operation a connector exposes.
type Operation struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema of its arguments.
	InputSchema map[string]any
}

func propertyNames(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// closestOperation picks the operation a tool most likely maps to: the same name, then a name
// that contains the tool name or that the tool name contains ("read" ~ "read_file").
func closestOperation(tool string, ops []Operation) (Operation, bool) {
	for _, o := range ops {
		if o.Name == tool {
			return o, true
		}
	}
	for _, o := range ops {
		if strings.HasPrefix(o.Name, tool+"_") || strings.HasSuffix(o.Name, "_"+tool) {
			return o, true
		}
	}
	for _, o := range ops {
		if strings.Contains(o.Name, tool) {
			return o, true
		}
	}
	return Operation{}, false
}

func jsString(s string) string { return fmt.Sprintf("%q", s) }

// AdapterTemplate generates the skeleton of the JavaScript of an adapter from the definition of
// the MCP (one case per tool it expects) and the operations of the connector (the ones the code
// can call): each tool is mapped onto the operation that looks closest, with the arguments the
// tool receives, or left as a TODO. The author completes the mapping (renaming, reshaping the
// arguments and the result, several calls).
func AdapterTemplate(def Def, connector string, ops []Operation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// Adapter of the MCP %s on the connector %s.\n", def.Name, connector)
	b.WriteString("// ctx.tool() is the tool called and ctx.args() its arguments; ctx.param(name) reads a parameter of the\n")
	b.WriteString("// organisation's instance; ctx.call(operation, args) calls the connector and returns its result;\n")
	b.WriteString("// return the result of the tool, or reject with ctx.fail(message).\n")
	if len(ops) > 0 {
		b.WriteString("// Operations of the connector:\n")
		for _, o := range ops {
			fmt.Fprintf(&b, "//   %s(%s)", o.Name, strings.Join(propertyNames(o.InputSchema), ", "))
			if o.Description != "" {
				fmt.Fprintf(&b, " - %s", o.Description)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("switch (ctx.tool()) {\n")
	for _, t := range def.Tools {
		fmt.Fprintf(&b, "  case %s:\n", jsString(t.Name))
		op, ok := closestOperation(t.Name, ops)
		if !ok {
			fmt.Fprintf(&b, "    // TODO: no operation of the connector matches; call the ones needed with ctx.call(...)\n")
			fmt.Fprintf(&b, "    ctx.fail(%s);\n    return;\n", jsString("not implemented: "+t.Name))
			continue
		}
		var args []string
		for _, n := range propertyNames(t.InputSchema) {
			args = append(args, fmt.Sprintf("%s: ctx.args().%s", n, n))
		}
		fmt.Fprintf(&b, "    return ctx.call(%s, { %s });\n", jsString(op.Name), strings.Join(args, ", "))
	}
	b.WriteString("}\nctx.fail(\"unknown tool \" + ctx.tool());\n")
	return b.String()
}

// AdapterParams derives the parameters of an adapter from what the connector announces: one
// per property of its configuration schema (the connector receives them as its configuration,
// under the same name) and one secret per secret name.
func AdapterParams(configSchema map[string]any, secretNames []string) []algo.Param {
	required := map[string]bool{}
	if req, ok := configSchema["required"].([]any); ok {
		for _, r := range req {
			if n, _ := r.(string); n != "" {
				required[n] = true
			}
		}
	}
	props, _ := configSchema["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []algo.Param
	for _, n := range names {
		spec, _ := props[n].(map[string]any)
		p := algo.Param{Name: n, Type: algo.ParamString, Required: required[n]}
		p.Description, _ = spec["description"].(string)
		switch spec["type"] {
		case "number", "integer":
			p.Type = algo.ParamNumber
		case "boolean":
			p.Type = algo.ParamBoolean
		case "array":
			p.Type = algo.ParamStrings
		case "object":
			p.Type = algo.ParamJSON
		}
		out = append(out, p)
	}
	for _, s := range secretNames {
		out = append(out, algo.Param{Name: s, Type: algo.ParamSecret, Required: true, Description: "reference of the secret " + s + " (<vault path>#<field> or env:<VAR>)"})
	}
	return out
}
