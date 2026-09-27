package registrysvc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/methodology"
)

// An agent-scoped MCP (goap-scheduler) is declared by agents only, an action-scoped one by actions only (ADR 0028).
func TestMCPScopesAreValidated(t *testing.T) {
	s := &Service{Store: NewMemoryStore(), MCPScopes: func(context.Context) (map[string]string, error) {
		return map[string]string{mcp.BuiltinScheduler: mcp.ScopeAgent, "files": mcp.ScopeAction, "notes": mcp.ScopeBoth}, nil
	}}
	withALM(t, s)
	m := example(t)
	if issues := s.validate(context.Background(), &m); len(issues) > 0 {
		t.Fatalf("example: %v", issues)
	}

	// the agent level may orchestrate; a step may not
	m.Agents = []methodology.Agent{{Name: "lead", Planner: methodology.PlannerGOAP, MCPs: []string{mcp.BuiltinScheduler, "notes"}}}
	if issues := s.validate(context.Background(), &m); len(issues) > 0 {
		t.Fatalf("agent-level scheduler refused: %v", issues)
	}
	m.Actions = append(m.Actions,
		methodology.Action{Name: "delegate", Kind: methodology.KindTool, Tool: mcp.BuiltinScheduler + "/start", Cost: 1},
		methodology.Action{Name: "think", Kind: methodology.KindLLM, Prompt: "p", MCPs: []string{mcp.BuiltinScheduler, "files"}, Cost: 1})
	m.Agents[0].MCPs = append(m.Agents[0].MCPs, "files")
	var got []string
	for _, i := range s.scopeIssues(context.Background(), &m) {
		got = append(got, i.Path)
	}
	n := len(m.Actions)
	want := []string{fmt.Sprintf("actions[%d].tool", n-2), fmt.Sprintf("actions[%d].mcps", n-1), "agents[0].mcps"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("issues at %v, want %v", got, want)
	}
}
