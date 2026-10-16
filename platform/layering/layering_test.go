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
	"contracts/mcp":  {"lib/adapter", "lib/mcpbuiltin", "lib/access", "contracts/algo", "contracts/domain", "graph/graph"},
	"contracts/algo": {"contracts/mcp", "lib/adapter", "lib/mcpbuiltin", "lib/access"},
	"lib/dsl":        {"contracts/mcp", "lib/adapter", "lib/mcpbuiltin", "lib/access"},
	"lib/def":        {"contracts/mcp", "lib/adapter", "lib/mcpbuiltin", "lib/access"},
	"lib/access":     {"contracts/mcp", "lib/adapter", "lib/mcpbuiltin", "graph/graph", "engine/engine"},
	"lib/llmcfg":     {"contracts/mcp", "lib/adapter", "lib/mcpbuiltin", "graph/graph", "engine/engine"},
	// The methodology describes and compiles; the engine plans and runs (ADR 0126): neither the compiler nor the
	// conditions know the planner.
	"engine/methodology":    {"engine/goap", "engine/engine"},
	"engine/condition":      {"engine/goap", "engine/engine"},
	"engine/engine":         {"lib/adapter", "lib/mcpbuiltin", "engine/observe", "engine/selfimprove", "platform/assistantsvc", "graph/graph"},
	"contracts/builtins":    {"engine/methodology", "engine/engine", "contracts/domain"},
	"lib/adapter":           {"lib/mcpbuiltin", "lib/access"},
	"contracts/domain":      {"contracts/risk", "engine/methodology", "contracts/journal", "contracts/decision", "contracts/verify", "contracts/criticality", "contracts/review"},
	"contracts/criticality": {"graph/graph", "engine/engine", "engine/methodology", "contracts/journal", "contracts/decision", "engine/condition", "lib/access"},
	"contracts/verify":      {"graph/graph", "engine/engine", "engine/methodology", "contracts/risk", "contracts/journal", "contracts/decision", "engine/condition"},
	"contracts/decision":    {"graph/graph", "engine/engine", "engine/methodology", "contracts/risk", "contracts/journal"},
	"contracts/journal":     {"graph/graph", "engine/engine", "engine/methodology"},
	"graph/graph":           {"contracts/risk", "engine/methodology", "contracts/journal", "contracts/decision", "contracts/verify", "contracts/criticality", "contracts/review", "platform/registrysvc", "platform/assistantsvc", "engine/engine"},
	// The engine and the graph meet only through the contract of the graph (engine.GraphPort over contracts/graphapi): the
	// engine names no graph implementation, the graph and its service no engine type (docs/architecture.md, "Components").
	"contracts/graphapi": {"graph/graph", "engine/engine", "engine/methodology"},
	"graph/graphsvc":     {"engine/engine", "engine/methodology"},
	// The gateway, the MCP hub and what every service shares reach the graph and the engine through their contracts
	// and clients only, never their implementation (ADR 0125).
	"platform/cmd/gateway":   {"graph/graph", "engine/engine", "graph/graphsvc"},
	"gateway/gateway":        {"graph/graph", "engine/engine", "graph/graphsvc"},
	"gateway/credsvc":        {"graph/graph", "engine/engine", "graph/graphsvc"},
	"mcp/mcpsvc":             {"graph/graph", "engine/engine", "graph/graphsvc"},
	"mcp/connectorkit":       {"graph/graph", "engine/engine", "graph/graphsvc"},
	"mcp/connectors/builtin": {"graph/graph", "engine/engine", "graph/graphsvc"},
	"lib/graphclient":        {"graph/graph", "engine/engine", "graph/graphsvc"},
	"lib/telemetry":          {"graph/graph", "engine/engine", "graph/graphsvc"},
	"lib/pbconv":             {"graph/graph", "engine/engine", "graph/graphsvc"},
	"lib/rpcerr":             {"graph/graph", "engine/engine", "graph/graphsvc"},
	"lib/graphsnap":          {"graph/graph", "engine/engine"},
	"platform/cmd/mcp":       {"graph/graph", "engine/engine", "engine/methodology", "graph/graphsvc", "platform/registrysvc"},
	// Two registries (ADR 0125 §3): the domain registry defines the graph and names no methodology; the methodology
	// registry reads it through a port and the contract, and stores its nodes through the graph's contract.
	"graph/domainsvc":      {"graph/graph", "engine/engine", "engine/methodology", "platform/registrysvc"},
	"lib/domainclient":     {"graph/graph", "engine/engine", "engine/methodology", "graph/domainsvc", "platform/registrysvc"},
	"lib/domainpb":         {"graph/graph", "engine/engine", "engine/methodology", "graph/domainsvc", "platform/registrysvc"},
	"platform/registrysvc": {"graph/graph", "graph/graphsvc", "graph/domainsvc"},
	"contracts/review":     {"graph/graph", "engine/engine", "engine/methodology", "contracts/risk", "contracts/journal", "contracts/decision", "engine/condition", "lib/access"},
}

// The graph core names no concept of the methodology namespace (ADR 0066: the Activity a change is scoped to lives in
// Change.Data, read by the registry through ChangeLifecycles.Merge and Graph.SubChangeValidator).
func TestGraphNamesNoMethodology(t *testing.T) {
	files, err := filepath.Glob("../../graph/graph/*.go")
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
	for _, dir := range []string{"../../graph/graph", "../../contracts/domain"} {
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
	for _, dir := range []string{"../../graph/graph", "../../contracts/domain"} {
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

// TestEngineDecodesNoConvention keeps the engine reading the formal elements the compiler states (ADR 0126): the
// call of a step (methodology.Call), the owner of a step, the description of a generated condition; it decodes no name
// the compiler makes up (the prefixes of the generated conditions, the parameters of a former builtin), and it reads
// the activable form only, never the definition behind it (Compiled.Definition, ADR 0126 step 2).
func TestEngineDecodesNoConvention(t *testing.T) {
	files, err := filepath.Glob("../../engine/engine/*.go")
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
		for _, word := range []string{`"step:`, `"state:`, `"method:`, `"subject:`, `"pending:`, `"when:`, `"expect:`, "StatePrefix", "ReactionPrefix",
			`Params["step"]`, `Params["capability"]`, `Params["foreach"]`, `Params["groupBy"]`, `Params["goal"]`, "process.step", ".Definition()"} {
			if strings.Contains(string(b), word) {
				t.Errorf("%s decodes the convention %s: read the element the compiler states instead", f, word)
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

// forbiddenInTests lists, by package, the packages its tests must not depend on either: the engine is tested without
// the graph (a real graph runs it in platform/e2e/engine, ADR 0125 §5), so that it can become a module of its own.
var forbiddenInTests = map[string][]string{
	"engine/engine": {"graph/graph", "graph/graph/graphtest", "graph/graphsvc", "platform/devseed", "lib/mcpbuiltin"},
}

func TestTestLayering(t *testing.T) {
	for pkg, banned := range forbiddenInTests {
		out, err := exec.Command("go", "list", "-test", "-deps", "-f", "{{.ImportPath}}", mod+pkg).Output()
		if err != nil {
			t.Fatalf("go list -test %s: %v", pkg, err)
		}
		deps := strings.Fields(string(out))
		for _, b := range banned {
			if slices.Contains(deps, mod+b) {
				t.Errorf("the tests of %s depend on %s", pkg, b)
			}
		}
	}
}

// engine.Internals opens the engine to the integration tests of the platform; no production code calls it.
func TestEngineInternalsOnlyInTests(t *testing.T) {
	err := filepath.WalkDir("../..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "web", "gen":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Dir(path) == filepath.Join("..", "..", "engine", "engine") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "engine.Internals") {
			t.Errorf("%s uses engine.Internals", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
