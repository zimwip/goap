// Package layering guards the dependency rules between the concepts of the platform (CLAUDE.md, design
// rule 1; ADR 0061): an MCP knows no connector and no adapter, the algorithm library knows no MCP, a domain
// knows no adapter, the graph and the domain model know no use case (the risk register), and the adapter is the
// one place where MCP and organisation meet.
package layering

import (
	"os"
	"os/exec"
	"path/filepath"
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
	"pkg/domain":     {"pkg/risk", "pkg/methodology", "pkg/journal"},
	"pkg/journal":    {"pkg/graph", "pkg/engine", "pkg/methodology"},
	"pkg/graph":      {"pkg/risk", "pkg/methodology", "pkg/journal", "internal/registrysvc"},
}

// The graph core names no concept of the methodology namespace (ADR 0066: the Activity a change is scoped to lives in
// Change.Data, read by the registry through Graph.LandingGate and Graph.SubChangeValidator).
func TestGraphNamesNoMethodology(t *testing.T) {
	files, err := filepath.Glob("../graph/*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v, %d files", err, len(files))
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, word := range []string{"methodology@", "NamespaceMethodology", "sub_activity", "ActivityRef"} {
			if strings.Contains(string(b), word) {
				t.Errorf("%s names %q", f, word)
			}
		}
	}
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
