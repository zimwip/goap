package mcpbuiltin

import (
	"testing"

	"github.com/zimwip/goap/pkg/mcp"
)

func TestDefinitions(t *testing.T) {
	defs := Defs()
	adapters := AdapterDefs()
	if len(defs) != len(Names) || len(adapters) != len(defs) {
		t.Fatalf("%d definitions, %d adapters for %d built-ins", len(defs), len(adapters), len(Names))
	}
	for i, d := range defs {
		if err := d.Validate(); err != nil {
			t.Fatal(err)
		}
		if !Is(d.Name) || adapters[i].MCP != d.Name || adapters[i].Connector != d.Name {
			t.Fatalf("adapter %+v does not implement %s", adapters[i], d.Name)
		}
		if err := adapters[i].Validate(); err != nil {
			t.Fatal(err)
		}
		if err := Adapter("ORG-DEFAULT", d.Name).Validate(d); err != nil {
			t.Fatal(err)
		}
	}
	if Is("document-repository") {
		t.Fatal("document-repository is not built in")
	}
}

// Starting other agents is the agent level's business only.
func TestOnlyTheSchedulerIsForAgents(t *testing.T) {
	for _, d := range Defs() {
		if want := d.Name == Scheduler; (d.Scope == mcp.ScopeAgent) != want {
			t.Fatalf("%s has scope %q", d.Name, d.Scope)
		}
	}
}
