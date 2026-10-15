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

// The workspace MCP is defined with its adapter, but is not one of the built-ins every unit holds (ADR 0118).
func TestWorkspace(t *testing.T) {
	d := WorkspaceDef()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if Is(Workspace) {
		t.Fatal("the workspace MCP is held by no unit by default")
	}
	for _, a := range WorkspaceAdapterDefs() {
		if err := a.Validate(); err != nil || a.MCP != Workspace {
			t.Fatalf("adapter %s: %v", a.Name, err)
		}
	}
	if err := WorkspaceDockerAdapter("ORG-DEFAULT", "/tmp/ws", "goap/builder:1").Validate(d); err != nil {
		t.Fatal(err)
	}
	if err := WorkspaceAdapter("ORG-DEFAULT", "/tmp/ws").Validate(d); err != nil {
		t.Fatal(err)
	}
}

// The Claude coding agent MCP is defined with its adapter, held by no unit by default (ADR 0123).
func TestClaudeAgent(t *testing.T) {
	d := ClaudeAgentDef()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if Is(ClaudeAgent) {
		t.Fatal("the Claude coding agent MCP is held by no unit by default")
	}
	a := ClaudeAgentAdapterDef()
	if err := a.Validate(); err != nil || a.MCP != ClaudeAgent || a.Connector != ClaudeAgent {
		t.Fatalf("adapter %s: %v", a.Name, err)
	}
	if err := ClaudeAgentAdapter("ORG-DEFAULT", "/tmp/ws").Validate(d); err != nil {
		t.Fatal(err)
	}
}
