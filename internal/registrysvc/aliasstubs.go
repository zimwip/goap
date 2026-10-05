package registrysvc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llmcfg"
	"github.com/zimwip/goap/pkg/methodology"
)

// ensureAliasStubs finds every LLM alias an agent or action of m references and, for any
// that names no platform@LlmAlias node yet, opens a pending ChangeImpact on the platform
// namespace proposing it as a stub (no target: "needs fixing"), so an administrator sees it
// in the change-review queue and fills in the real target (ADR 0021). Best-effort: an error
// here is never returned - it must never block a publish.
// graphBacked is satisfied by *GraphStore and by anything embedding it (Go promotes the method) - it exposes the
// underlying graph so ensureAliasStubs can propose alias stubs on the platform namespace, separate from wherever
// the methodology itself is stored.
type graphBacked interface {
	aliasStubGraph() StoreGraph
}

func (s *Service) ensureAliasStubs(ctx context.Context, m *methodology.Methodology) {
	gb, ok := s.Store.(graphBacked)
	if !ok {
		return // no graph behind this registry (e.g. MemoryStore in dev/tests): nothing to stub
	}
	g := gb.aliasStubGraph()
	aliases := referencedAliases(m)
	if len(aliases) == 0 {
		return
	}
	missing, err := missingAliases(ctx, g, aliases)
	if err != nil || len(missing) == 0 {
		return
	}
	var stubs []string
	for _, alias := range missing {
		pending, err := hasPendingStub(ctx, g, alias)
		if err != nil || pending {
			continue
		}
		stubs = append(stubs, alias)
	}
	if len(stubs) > 0 {
		_ = createAliasStubs(ctx, g, stubs, m.Name)
	}
}

// referencedAliases collects every non-empty Agent.Model / Action.Model of m, deduplicated.
func referencedAliases(m *methodology.Methodology) []string {
	seen := map[string]bool{}
	var out []string
	add := func(alias string) {
		if alias != "" && !seen[alias] {
			seen[alias] = true
			out = append(out, alias)
		}
	}
	for _, a := range m.Agents {
		add(a.Model)
	}
	for _, a := range m.Actions {
		add(a.Model)
	}
	return out
}

// missingAliases returns which of aliases has no platform@LlmAlias node at the head of
// platform/main (an existing-but-broken alias, i.e. present with an empty target, still
// counts as "exists" - don't re-stub it).
func missingAliases(ctx context.Context, g StoreGraph, aliases []string) ([]string, error) {
	head, err := g.BranchHead(ctx, llmcfg.NamespacePlatform, domain.MainBranch)
	if errors.Is(err, graph.ErrNotFound) {
		return aliases, nil // no platform graph yet: every alias is "missing"
	}
	if err != nil {
		return nil, err
	}
	nodes, _, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, n := range nodes {
		if n.Namespace == llmcfg.NamespacePlatform && n.Type == llmcfg.NodeTypeAlias {
			have[n.Key] = true
		}
	}
	var missing []string
	for _, a := range aliases {
		if !have[llmcfg.AliasKey(a)] {
			missing = append(missing, a)
		}
	}
	return missing, nil
}

// hasPendingStub reports whether an open (not applied/abandoned) platform-namespace change
// already proposes alias as a pending (not yet reviewed) LlmAlias node - avoids creating a
// duplicate stub when a methodology referencing it is republished before an administrator acts.
func hasPendingStub(ctx context.Context, g StoreGraph, alias string) (bool, error) {
	changes, err := g.Changes(ctx)
	if err != nil {
		return false, err
	}
	key, typ := llmcfg.AliasKey(alias), llmcfg.NodeTypeAlias
	for _, c := range changes {
		if c.Namespace != llmcfg.NamespacePlatform {
			continue
		}
		switch c.Status {
		case domain.ChangeApplied, domain.ChangeAbandoned:
			continue
		}
		for _, cn := range c.Nodes {
			if cn.Key == key && cn.Type == typ && cn.Review == domain.ReviewProposed {
				return true, nil
			}
		}
	}
	return false, nil
}

// createAliasStubs opens one platform-namespace change proposing a stub platform@LlmAlias node (name only, no
// target) per alias a methodology needs, as pending change impacts: the administrator reviews them together. It never
// calls Graph.Commit (which auto-accepts) nor ReviewNode/Apply: the change stays open and visible in the review queue
// until an administrator reviews it.
func createAliasStubs(ctx context.Context, g StoreGraph, aliases []string, methodologyName string) error {
	head, err := g.BranchHead(ctx, llmcfg.NamespacePlatform, domain.MainBranch)
	if err != nil {
		return err
	}
	c, err := g.CreateChange(ctx, graph.NewChange{
		Namespace:  llmcfg.NamespacePlatform,
		Title:      fmt.Sprintf("Aliases needed by %s (%s)", methodologyName, strings.Join(aliases, ", ")),
		Intent:     "configure the model aliases referenced by a methodology",
		BaselineID: head.ID,
		OwnBranch:  true,
	})
	if err != nil {
		return err
	}
	for _, alias := range aliases {
		// a working version, checked out, its review proposed (ADR 0076): the change stays open
		if _, err := g.CreateNode(ctx, c.ID, graph.NodeCreate{Key: llmcfg.AliasKey(alias), Type: llmcfg.NodeTypeAlias, Properties: map[string]any{"alias": alias},
			Rationale: fmt.Sprintf("referenced by methodology %s but not configured yet", methodologyName), ProducedBy: "methodology-load"}); err != nil {
			return err
		}
	}
	return nil
}
