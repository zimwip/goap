package registrysvc

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// SubChangeValidator is the sub-change rule of the guardian of the registry (Guardian.MayCreateChild, ADR 0098;
// architecture plan "Activity concept" cascade): the Activity a sub-change is scoped to must be its parent's or a
// descendant of it, following the sub_activity links of the methodology namespace. Unlike the unit and the project, an
// unset activity is not inherited: a sub-change is typically scoped to a more specific sub-activity than its parent,
// not the same one. A registry with no graph behind its store (MemoryStore) validates nothing.
func (s *Service) SubChangeValidator(ctx context.Context, parent, child domain.Change) error {
	ref, ancestor := ActivityOf(child), ActivityOf(parent)
	if ref == "" || ancestor == "" || ref == ancestor {
		return nil
	}
	gb, ok := s.Store.(graphBacked)
	if !ok {
		return nil
	}
	within, err := activityWithin(ctx, gb.aliasStubGraph(), ref, ancestor)
	if err != nil {
		return err
	}
	if !within {
		return fmt.Errorf("activity %s is not part of %s, the activity of the parent change: %w", ref, ancestor, graph.ErrInvalid)
	}
	return nil
}

// activityWithin reports whether the Activity with key ref is ancestor or a descendant of it, walking up the
// sub_activity links (parent -> child) of the head of the methodology namespace's main branch.
func activityWithin(ctx context.Context, g StoreGraph, ref, ancestor string) (bool, error) {
	head, err := g.BranchHead(ctx, NamespaceMethodology, domain.MainBranch)
	if err != nil {
		return false, err
	}
	nodes, links, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		return false, err
	}
	keys := make(map[domain.NodeID]string, len(nodes))
	for _, n := range nodes {
		keys[n.ID] = n.Key
	}
	parentOf := map[string]string{}
	for _, l := range links {
		if l.Type == linkSubActivity {
			parentOf[keys[l.To.ID]] = keys[l.From.ID]
		}
	}
	seen := map[string]bool{}
	for cur := ref; cur != "" && !seen[cur]; cur = parentOf[cur] {
		if cur == ancestor {
			return true, nil
		}
		seen[cur] = true
	}
	return false, nil
}
