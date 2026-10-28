package registrysvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/lib/llmcfg"
)

// ensureAliasStubs finds every LLM alias an agent or action of m references and, for any
// that names no platform@LlmAlias node yet, opens a pending ChangeImpact on the platform
// namespace proposing it as a stub (no target yet), so an administrator sees it
// in the change-review queue and sets the target of the draft before accepting it: the domain refuses an alias with none (ADR 0018, ADR 0021). Best-effort: an error
// here is never returned - it must never block a publish.
// graphBacked is satisfied by *GraphStore and by anything embedding it (Go promotes the method) - it exposes the
// underlying graph so ensureAliasStubs can propose alias stubs on the platform namespace, separate from wherever
// the methodology itself is stored.
type graphBacked interface {
	aliasStubGraph() StoreGraph
}

func (s *Service) ensureAliasStubs(ctx context.Context, m *methodology.Methodology) {
	// check-then-create must not interleave: methodologies are seeded and published concurrently
	s.aliasMu.Lock()
	defer s.aliasMu.Unlock()
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
		if err := createAliasStubs(ctx, g, stubs, m.Name); err != nil {
			log := s.Log
			if log == nil {
				log = slog.Default()
			}
			log.Warn("registry: could not propose the alias stubs", "methodology", m.Name, "aliases", stubs, "err", err)
		}
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
	head, err := g.BranchHeadHeader(ctx, llmcfg.NamespacePlatform, domain.MainBranch)
	if errors.Is(err, graphapi.ErrNotFound) {
		return aliases, nil // no platform graph yet: every alias is "missing"
	}
	if err != nil {
		return nil, err
	}
	nodes, _, err := g.CommitGraph(ctx, head.ID)
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
// already holds the LlmAlias node of alias, whatever the state of its review - avoids creating a
// duplicate stub when a methodology referencing it is republished before an administrator acts.
// (Change.Nodes are the impacts of its view, filled by Graph.Changes.)
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
			if cn.Key == key && cn.Type == typ {
				return true, nil
			}
		}
	}
	return false, nil
}

// createAliasStubs opens one platform-namespace change proposing a stub platform@LlmAlias node (name only, no
// target) per alias a methodology needs, as pending change impacts: the administrator reviews them together. It never
// calls Graph.CommitEdits (which auto-accepts) nor ImpactNodeReview/Apply: the change stays open and visible in the review queue
// until an administrator reviews it.
func createAliasStubs(ctx context.Context, g StoreGraph, aliases []string, methodologyName string) error {
	head, err := g.BranchHeadHeader(ctx, llmcfg.NamespacePlatform, domain.MainBranch)
	if err != nil {
		return err
	}
	root, err := rootProject(ctx, g)
	if err != nil {
		return err
	}
	c, err := g.CreateChange(ctx, graphapi.NewChange{
		Namespace:  llmcfg.NamespacePlatform,
		ProjectID:  root,
		Title:      fmt.Sprintf("Model aliases needed by %s (%s)", methodologyName, strings.Join(aliases, ", ")),
		Intent:     fmt.Sprintf("methodology %s references the model aliases %s: each needs a target (provider/model) before this change can be accepted", methodologyName, strings.Join(aliases, ", ")),
		ForkCommit: head.ID,
		OwnBranch:  true,
	})
	if err != nil {
		return err
	}
	for _, alias := range aliases {
		// a draft, its review proposed (ADR 0076, 0079): the change stays open
		if _, err := g.ImpactNodeCreate(ctx, c.ID, graphapi.NodeCreate{Key: llmcfg.AliasKey(alias), Type: llmcfg.NodeTypeAlias, Properties: map[string]any{"alias": alias},
			Rationale: fmt.Sprintf("alias %s needs a target provider/model before this change can be accepted (referenced by methodology %s)", alias, methodologyName), ProducedBy: "methodology-load"}); err != nil {
			return err
		}
	}
	return nil
}
