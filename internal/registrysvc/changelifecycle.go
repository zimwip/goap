package registrysvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
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
	found := lifecycleIn(recs, &m)
	if found == nil {
		return nil, fmt.Errorf("methodology %s names lifecycle %q, which no domain defines: %w", m.Name, m.Lifecycle, ErrInvalid)
	}
	return found, nil
}

// lifecycleIn is the lifecycle m names among the domains: the one of its namespace, else of any domain (nil: none).
func lifecycleIn(recs []DomainRecord, m *methodology.Methodology) *domain.Lifecycle {
	if m.Lifecycle == "" {
		return nil
	}
	var found *domain.Lifecycle
	for _, d := range recs {
		if lc := d.Domain.Lifecycle(m.Lifecycle); lc != nil && (found == nil || d.Domain.Name == m.Namespace) {
			found = lc
		}
	}
	return found
}

// DefaultGoal implements pkg/graph.ChangeLifecycles (ADR 0096): the main goal of a methodology, the goal its changes
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

// Guard implements pkg/graph.ChangeLifecycles: the guard of a transition of the lifecycle of a change, in the
// environment of the conditions of its methodology, whose values are the world state the guard reads.
func (s *Service) Guard(ctx context.Context, bb domain.Blackboard, expr, transition, decision string) (bool, error) {
	c, err := s.Methodology(ctx, bb.Change.Methodology)
	if err != nil {
		return false, err
	}
	return condition.CheckGuard(expr, bb, c.Conditions.Evaluate(bb).State, transition, decision)
}

// Gate implements pkg/graph.ChangeLifecycles: the vetos and objectives of a transition (ADR 0075 §3), in the same
// environment as its guard.
func (s *Service) Gate(ctx context.Context, bb domain.Blackboard, t domain.Transition, decision string) (domain.GateResult, error) {
	c, err := s.Methodology(ctx, bb.Change.Methodology)
	if err != nil {
		return domain.GateResult{}, err
	}
	return condition.CheckGate(t, bb, c.Conditions.Evaluate(bb).State, decision)
}

// OperationRoles returns the roles of a methodology that allow an operation of the catalogue (pkg/operations, ADR
// 0103), from its stored definition. restricted is false when the methodology lists the operations of none of its
// roles, or the registry does not know it: the operation is then allowed to anyone the other rules let in.
func (s *Service) OperationRoles(ctx context.Context, methodology, op string) (roles []string, restricted bool, err error) {
	if methodology == "" {
		return nil, false, nil
	}
	r, err := s.Store.Get(ctx, methodology, "")
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	roles, restricted = r.Methodology.OperationRoles(op)
	return roles, restricted, nil
}
