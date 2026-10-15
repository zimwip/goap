package layering

import (
	"encoding/json"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"testing"
)

// components places every package of the repository in the component it will be a module of (ADR 0125 §1), by the
// longest path prefix (a prefix covers the packages under it). A new package must be placed here.
var components = map[string]string{
	"gen":                          "contracts",
	"pkg/domain":                   "contracts",
	"pkg/journal":                  "contracts",
	"pkg/algo":                     "contracts",
	"pkg/authz":                    "contracts",
	"pkg/events":                   "contracts",
	"pkg/operations":               "contracts",
	"pkg/builtins":                 "contracts",
	"pkg/form":                     "contracts",
	"pkg/mcp":                      "contracts",
	"pkg/llm":                      "contracts",
	"pkg/graphapi":                 "contracts",
	"pkg/risk":                     "contracts",
	"pkg/assess":                   "contracts",
	"pkg/verify":                   "contracts",
	"pkg/review":                   "contracts",
	"pkg/criticality":              "contracts",
	"pkg/decision":                 "contracts",
	"pkg/domain/def":               "lib",
	"domains/builtin":              "lib",
	"pkg/adapter":                  "lib",
	"pkg/mcpbuiltin":               "lib",
	"pkg/graphsnap":                "lib",
	"pkg/brief":                    "lib",
	"pkg/typecat":                  "lib",
	"pkg/guard":                    "lib",
	"pkg/dsl":                      "lib",
	"pkg/access":                   "lib",
	"pkg/llmcfg":                   "lib",
	"internal/identity":            "lib",
	"internal/platform":            "lib",
	"internal/telemetry":           "lib",
	"internal/rpcerr":              "lib",
	"internal/pbconv":              "lib",
	"internal/sqlschematest":       "lib",
	"internal/pgtest":              "lib",
	"internal/graphclient":         "lib",
	"internal/domainclient":        "lib",
	"internal/domainpb":            "lib",
	"pkg/graph":                    "graph",
	"pkg/okf":                      "graph",
	"pkg/prov":                     "graph",
	"pkg/index":                    "graph",
	"internal/graphsvc":            "graph",
	"internal/domainsvc":           "graph",
	"internal/indexersvc":          "graph",
	"pkg/engine":                   "engine",
	"pkg/goap":                     "engine",
	"pkg/methodology":              "engine",
	"pkg/condition":                "engine",
	"pkg/intent":                   "engine",
	"pkg/observe":                  "engine",
	"pkg/selfimprove":              "engine",
	"pkg/eval":                     "engine",
	"internal/enginesvc":           "engine",
	"internal/sandbox":             "engine",
	"internal/telemetry/enginetel": "engine",
	"internal/mcpsvc":              "mcp",
	"internal/connectorkit":        "mcp",
	"internal/connectors":          "mcp",
	"internal/gateway":             "gateway",
	"internal/credsvc":             "gateway",
	// the platform: the compositions, the services that are none of the above (the methodology registry until it
	// moves to the engine, ADR 0125 §3) and the tests that need the graph and the engine together
	"cmd":                   "platform",
	"internal/e2e":          "platform",
	"internal/registrysvc":  "platform",
	"internal/modelgw":      "platform",
	"internal/prefssvc":     "platform",
	"internal/convsvc":      "platform",
	"internal/assistantsvc": "platform",
	"internal/eventsvc":     "platform",
	"internal/devseed":      "platform",
	"internal/opgen":        "platform",
	"pkg/layering":          "platform",
}

// dependsOn lists, by component, the components it may depend on besides itself.
var dependsOn = map[string][]string{
	"contracts": {},
	"lib":       {"contracts"},
	"graph":     {"contracts", "lib"},
	"engine":    {"contracts", "lib"},
	"mcp":       {"contracts", "lib"},
	"gateway":   {"contracts", "lib"},
	"platform":  {"contracts", "lib", "graph", "engine", "mcp", "gateway"},
}

func componentOf(pkg string) string {
	best, comp := -1, ""
	for prefix, c := range components {
		if (pkg == prefix || strings.HasPrefix(pkg, prefix+"/")) && len(prefix) > best {
			best, comp = len(prefix), c
		}
	}
	return comp
}

// Every package, its tests included, depends only on the components its own may depend on, so that each component can
// become a module of a go.work (ADR 0125 §1).
func TestComponents(t *testing.T) {
	cmd := exec.Command("go", "list", "-json=ImportPath,Imports,TestImports,XTestImports", "./...")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var bad []string
	for dec.More() {
		var p struct {
			ImportPath                         string
			Imports, TestImports, XTestImports []string
		}
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		src := strings.TrimPrefix(p.ImportPath, mod)
		from := componentOf(src)
		if from == "" {
			t.Errorf("%s is in no component: place it in components", src)
			continue
		}
		check := func(deps []string, test string) {
			for _, d := range deps {
				if !strings.HasPrefix(d, mod) {
					continue
				}
				dep := strings.TrimPrefix(d, mod)
				to := componentOf(dep)
				if to != from && !slices.Contains(dependsOn[from], to) {
					bad = append(bad, from+" -> "+to+": "+test+src+" imports "+dep)
				}
			}
		}
		check(p.Imports, "")
		check(append(p.TestImports, p.XTestImports...), "the tests of ")
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Error(b)
	}
}
