package registrysvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
)

// Lifecycle implements pkg/graph.ChangeLifecycles (ADR 0058): the lifecycle the changes of a methodology follow, the
// one it names in the domain of its namespace (else in any domain, a built-in one for instance). A methodology the
// registry does not know, or one naming none, gives nil: its changes have no state.
func (s *Service) Lifecycle(ctx context.Context, methodology string) (*domain.Lifecycle, error) {
	r, err := s.Store.Get(ctx, methodology, "")
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	m := r.Methodology
	if m.Lifecycle == "" {
		return nil, nil
	}
	recs, err := s.DomainVersions(ctx, false)
	if err != nil {
		return nil, err
	}
	var found *domain.Lifecycle
	for _, d := range recs {
		if lc := d.Domain.Lifecycle(m.Lifecycle); lc != nil && (found == nil || d.Domain.Name == m.Namespace) {
			found = lc
		}
	}
	if found == nil {
		return nil, fmt.Errorf("methodology %s names lifecycle %q, which no domain defines: %w", m.Name, m.Lifecycle, ErrInvalid)
	}
	return found, nil
}

// Guard implements pkg/graph.ChangeLifecycles: the guard of a transition of the lifecycle of a change, in the
// environment of the conditions of its methodology, whose values are the world state the guard reads.
func (s *Service) Guard(ctx context.Context, bb domain.Blackboard, expr, transition, decision string) (bool, error) {
	c, err := s.Methodology(ctx, bb.Change.Methodology)
	if err != nil {
		return false, err
	}
	return condition.CheckGuard(expr, bb, c.Conditions.Evaluate(bb).State, transition, decision)
}
