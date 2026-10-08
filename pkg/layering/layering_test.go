// Package layering guards the dependency rules between the concepts of the platform (CLAUDE.md, design
// rule 1; ADR 0061): an MCP knows no connector and no adapter, the algorithm library knows no MCP, a domain
// knows no adapter, the graph and the domain model know no use case (the risk register) nor the policy of the
// engine and of the agents (the relaunch of a step, the guidance, the confidence an agent ruling needs: ADR 0067),
// and the adapter is the one place where MCP and organisation meet.
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
	"pkg/mcp":         {"pkg/adapter", "pkg/mcpbuiltin", "pkg/access", "pkg/algo", "pkg/domain", "pkg/graph"},
	"pkg/algo":        {"pkg/mcp", "pkg/adapter", "pkg/mcpbuiltin", "pkg/access"},
	"pkg/dsl":         {"pkg/mcp", "pkg/adapter", "pkg/mcpbuiltin", "pkg/access"},
	"pkg/domain/def":  {"pkg/mcp", "pkg/adapter", "pkg/mcpbuiltin", "pkg/access"},
	"pkg/access":      {"pkg/mcp", "pkg/adapter", "pkg/mcpbuiltin"},
	"pkg/llmcfg":      {"pkg/mcp", "pkg/adapter", "pkg/mcpbuiltin"},
	"pkg/engine":      {"pkg/adapter", "pkg/mcpbuiltin", "pkg/observe", "pkg/selfimprove", "internal/assistantsvc"},
	"pkg/builtins":    {"pkg/methodology", "pkg/engine", "pkg/domain"},
	"pkg/adapter":     {"pkg/mcpbuiltin", "pkg/access"},
	"pkg/domain":      {"pkg/risk", "pkg/methodology", "pkg/journal", "pkg/decision", "pkg/verify", "pkg/criticality", "pkg/review"},
	"pkg/criticality": {"pkg/graph", "pkg/engine", "pkg/methodology", "pkg/journal", "pkg/decision", "pkg/condition", "pkg/access"},
	"pkg/verify":      {"pkg/graph", "pkg/engine", "pkg/methodology", "pkg/risk", "pkg/journal", "pkg/decision", "pkg/condition"},
	"pkg/decision":    {"pkg/graph", "pkg/engine", "pkg/methodology", "pkg/risk", "pkg/journal"},
	"pkg/journal":     {"pkg/graph", "pkg/engine", "pkg/methodology"},
	"pkg/graph":       {"pkg/risk", "pkg/methodology", "pkg/journal", "pkg/decision", "pkg/verify", "pkg/criticality", "pkg/review", "internal/registrysvc", "internal/assistantsvc"},
	"pkg/review":      {"pkg/graph", "pkg/engine", "pkg/methodology", "pkg/risk", "pkg/journal", "pkg/decision", "pkg/condition", "pkg/access"},
}

// The graph core names no concept of the methodology namespace (ADR 0066: the Activity a change is scoped to lives in
// Change.Data, read by the registry as the guardian of the change, graph.Guardian, ADR 0098).
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

// The graph core keeps the mechanism of flows and decision points; what the engine and the agents make of them is
// theirs (ADR 0067): a flow stores an opaque origin and opaque stale run ids and opens with items it is given, a decision
// point stores opaque policy values and asks a DecisionPolicy. The core names none of their vocabulary.
func TestCoreNamesNoEnginePolicy(t *testing.T) {
	for _, dir := range []string{"../graph", "../domain"} {
		files, err := filepath.Glob(dir + "/*.go")
		if err != nil || len(files) == 0 {
			t.Fatalf("glob %s: %v, %d files", dir, err, len(files))
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, word := range []string{`"guidance"`, "FromStep", "StaleExecutions", "Threshold", "MaxRounds", "DeciderAgent", "DeciderHuman"} {
				if strings.Contains(string(b), word) {
					t.Errorf("%s names %q", f, word)
				}
			}
		}
	}
}

// The graph core and the domain model name no structure of the organisation (ADR 0054, ADR 0069): the node type that
// builds each structure, its parent link, its root and its default flag come from the type catalogue (a domain tags
// them, domains/builtin/organisation.yaml for the built-in one), and an untyped graph falls back to the catalogue of
// the built-in domains, never to names written in Go.
func TestCoreNamesNoStructure(t *testing.T) {
	for _, dir := range []string{"../graph", "../domain"} {
		files, err := filepath.Glob(dir + "/*.go")
		if err != nil || len(files) == 0 {
			t.Fatalf("glob %s: %v, %d files", dir, err, len(files))
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, word := range []string{"ORG-DEFAULT", "PROJ-ROOT", "OrgUnit", "ProjectUnit", "part_of", "project_part_of"} {
				if strings.Contains(string(b), word) {
					t.Errorf("%s names %q", f, word)
				}
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
