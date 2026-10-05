// Package layering guards the dependency rules between the concepts of the platform (CLAUDE.md, design
// rule 1; ADR 0061): an MCP knows no connector and no adapter, the algorithm library knows no MCP, a domain
// knows no adapter, and the adapter is the one place where MCP and organisation meet.
package layering

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const mod = "github.com/zimwip/goap/"

// forbidden lists, by package, the packages it must not depend on, directly or not.
var forbidden = map[string][]string{
	"pkg/mcp":        {"pkg/adapter", "pkg/mcpbuiltin", "pkg/access", "pkg/algo", "pkg/domain", "pkg/graph"},
	"pkg/algo":       {"pkg/mcp", "pkg/adapter", "pkg/mcpbuiltin", "pkg/access"},
	"pkg/dsl":        {"pkg/mcp", "pkg/adapter", "pkg/mcpbuiltin", "pkg/access"},
	"pkg/domain/def": {"pkg/mcp", "pkg/adapter", "pkg/mcpbuiltin", "pkg/access"},
	"pkg/access":     {"pkg/mcp", "pkg/adapter", "pkg/mcpbuiltin"},
	"pkg/llmcfg":     {"pkg/mcp", "pkg/adapter", "pkg/mcpbuiltin"},
	"pkg/engine":     {"pkg/adapter", "pkg/mcpbuiltin", "pkg/observe", "pkg/selfimprove"},
	"pkg/builtins":   {"pkg/methodology", "pkg/engine", "pkg/domain"},
	"pkg/adapter":    {"pkg/mcpbuiltin", "pkg/access"},
}

func TestLayering(t *testing.T) {
	for pkg, banned := range forbidden {
		out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", mod+pkg).Output()
		if err != nil {
			t.Fatalf("go list %s: %v", pkg, err)
		}
		deps := strings.Fields(string(out))
		for _, b := range banned {
			if slices.Contains(deps, mod+b) {
				t.Errorf("%s depends on %s", pkg, b)
			}
		}
	}
}
