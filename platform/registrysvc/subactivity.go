package registrysvc

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
)

// SubChangeValidator resolves pkg/graph.Graph.SubChangeValidator (architecture plan "Activity concept" cascade): the
// Activity a sub-change is scoped to must be its parent's or a descendant of it, following the sub_activity links of
// the methodology namespace. Unlike the unit and the project, an unset activity is not inherited: a sub-change is
// typically scoped to a more specific sub-activity than its parent, not the same one. Wire it from main:
// g.SubChangeValidator = reg.SubChangeValidator, or a guardian client (ADR 0168). A registry with no graph behind its store (MemoryStore) validates
// nothing.
func (s *Service) SubChangeValidator(ctx context.Context, parent, child domain.Change) error {
	ref, ancestor := ActivityOf(child), ActivityOf(parent)
	if ref == "" || ancestor == "" || ref == ancestor {
		return nil
	}
	gs, ok := s.Store.(interface {
		read(ctx context.Context) (*defs, error)
	})
	if !ok {
		return nil
	}
	d, err := gs.read(ctx)
	if err != nil {
		return err
	}
	if !activityWithin(d.parentOf, ref, ancestor) {
		return fmt.Errorf("activity %s is not part of %s, the activity of the parent change: %w", ref, ancestor, graphapi.ErrInvalid)
	}
	return nil
}

// activityWithin reports whether the Activity with key ref is ancestor or a descendant of it, walking up the
// sub_activity links (parent -> child) of the head of the methodology namespace's main branch.
func activityWithin(parentOf map[string]string, ref, ancestor string) bool {
	seen := map[string]bool{}
	for cur := ref; cur != "" && !seen[cur]; cur = parentOf[cur] {
		if cur == ancestor {
			return true
		}
		seen[cur] = true
	}
	return false
}

// parentsOf is the activity each activity is a part of, by key: the source of the sub_activity link reaching it, from
// any version of the source (suspect links included).
func parentsOf(nodes []domain.Node, links []domain.Link) map[string]string {
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
	return parentOf
}
