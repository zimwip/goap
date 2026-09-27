package registrysvc

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/methodology"
)

// scopeIssues reports the MCPs a methodology declares where their scope forbids it (ADR 0028): an MCP of
// scope agent (orchestration: goap-scheduler) belongs to agents[].mcps, one of scope action to the actions.
// Without MCPScopes, or when they cannot be read, nothing is reported (unknown MCPs are allowed: the
// organisation of a change decides what is bound).
func (s *Service) scopeIssues(ctx context.Context, m *methodology.Methodology) methodology.Issues {
	if s.MCPScopes == nil {
		return nil
	}
	scopes, err := s.MCPScopes(ctx)
	if err != nil {
		return nil
	}
	var out methodology.Issues
	for i, a := range m.Actions {
		for _, name := range a.RequiredMCPs() {
			if sc, ok := scopes[name]; ok && !mcp.ForActions(sc) {
				field := ".mcps"
				if a.Kind == methodology.KindTool {
					field = ".tool"
				}
				out = append(out, methodology.Issue{Path: fmt.Sprintf("actions[%d]%s", i, field),
					Message: fmt.Sprintf("MCP %s has scope agent: declare it on the agent (agents[].mcps), its llm actions reach it", name)})
			}
		}
	}
	for i, ag := range m.Agents {
		for _, name := range ag.MCPs {
			if sc, ok := scopes[name]; ok && !mcp.ForAgents(sc) {
				out = append(out, methodology.Issue{Path: fmt.Sprintf("agents[%d].mcps", i),
					Message: fmt.Sprintf("MCP %s has scope action: declare it on the actions that use it", name)})
			}
		}
	}
	return out
}
