package registrysvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
)

// Lifecycle implements engine.LifecyclePort (ADR 0058, 0098): the lifecycle the changes of a methodology follow, the
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

// DefaultGoal implements pkg/graph.ChangeDefaults (ADR 0096): the main goal of a methodology, the goal its changes
// start with; "" for a methodology the registry does not know. It reads the definition, a methodology that does not
// compile still gives its goal.
func (s *Service) DefaultGoal(ctx context.Context, name string) (string, error) {
	r, err := s.Store.Get(ctx, name, "")
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return r.Methodology.MainGoal(), nil
}
