package registrysvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
)

// The guardian chain of a change (ADR 0106): the methodologies that frame it, outermost first, its own last, each
// declaring in its definition what it requires of the changes it integrates (integrates, guardian). The graph saves
// the chain on the change and asks the registry to evaluate it.

// maxChain bounds the walk up the integrations (a cycle is cut by the methodologies already seen anyway).
const maxChain = 8

// Guardians implements pkg/graph.ChangeLifecycles: the guardian chain of a new root change of a methodology in a
// project. From the methodology, the methodologies applicable to the project chain (nearest project first, Applicable)
// are searched for one integrating it; the first found frames it, and the search goes on from that one. Without
// Applicable, or for a methodology the registry does not know, the chain is the methodology alone.
func (s *Service) Guardians(ctx context.Context, name, project string) ([]domain.GuardianRef, error) {
	own, err := s.Store.Get(ctx, name, "")
	if errors.Is(err, ErrNotFound) {
		return []domain.GuardianRef{{Methodology: name}}, nil
	} else if err != nil {
		return nil, err
	}
	chain := []domain.GuardianRef{{Methodology: name, Version: own.Methodology.Version}}
	if s.Applicable == nil || project == "" {
		return chain, nil
	}
	apps, err := s.Applicable(ctx, project)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{name: true}
	for cur := name; len(chain) < maxChain; {
		var found *Record
		for _, a := range apps {
			if seen[a] {
				continue
			}
			r, err := s.Store.Get(ctx, a, "")
			if errors.Is(err, ErrNotFound) {
				continue
			} else if err != nil {
				return nil, err
			}
			if r.Methodology.IntegratesChangesOf(cur) {
				found = &r
				break
			}
		}
		if found == nil {
			break
		}
		m := found.Methodology
		chain = append([]domain.GuardianRef{{Methodology: m.Name, Version: m.Version}}, chain...)
		seen[m.Name], cur = true, m.Name
	}
	return chain, nil
}

// Integrates implements pkg/graph.ChangeLifecycles: whether the latest version of integrator integrates the changes
// of methodology; false for a methodology the registry does not know.
func (s *Service) Integrates(ctx context.Context, integrator, methodology string) (bool, error) {
	r, err := s.Store.Get(ctx, integrator, "")
	if errors.Is(err, ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return r.Methodology.IntegratesChangesOf(methodology), nil
}

// CheckGuardians implements pkg/graph.ChangeLifecycles: the outer guardians of the chain of the change, innermost
// first, each with its guard for the transition (guardian.transitions) or the merge (MergeGuard: guardian.merge, and
// an attached integrator when its integration is exclusive), in the environment of the conditions of the change's
// own methodology plus `integrator`, the nearest ancestor change of that guardian's methodology.
func (s *Service) CheckGuardians(ctx context.Context, bb domain.Blackboard, transition string, ancestors []domain.Change) (string, error) {
	c := bb.Change
	var world map[string]bool
	worldOf := func() map[string]bool {
		if world == nil {
			world = map[string]bool{}
			if cm, err := s.Methodology(ctx, c.Methodology); err == nil && cm != nil {
				world = cm.Conditions.Evaluate(bb).State
			}
		}
		return world
	}
	for i := len(c.Guardians) - 1; i >= 0; i-- {
		g := c.Guardians[i]
		if g.Methodology == c.Methodology {
			continue // its own: the lifecycle and the merge of the change
		}
		m, err := s.guardianMethodology(ctx, g)
		if err != nil {
			return "", err
		}
		if m == nil {
			continue // a guardian the registry no longer knows says nothing
		}
		inner := c.Methodology
		if i+1 < len(c.Guardians) {
			inner = c.Guardians[i+1].Methodology
		}
		expr := m.TransitionGuard(transition)
		if transition == "" {
			expr = m.MergeGuard(inner)
		}
		if expr == "" {
			continue
		}
		ok, err := condition.CheckGuardian(expr, bb, worldOf(), transition, integratorOf(ancestors, g.Methodology))
		if err != nil {
			return "", fmt.Errorf("guardian %s: %w", g.Methodology, err)
		}
		if !ok {
			return fmt.Sprintf("%s requires %s", g.Methodology, expr), nil
		}
	}
	return "", nil
}

// guardianMethodology is the definition of a guardian at the version saved in the chain, else the latest one; nil
// when the registry knows neither.
func (s *Service) guardianMethodology(ctx context.Context, g domain.GuardianRef) (*methodology.Methodology, error) {
	if g.Version != "" {
		if r, err := s.Store.Get(ctx, g.Methodology, g.Version); err == nil {
			return &r.Methodology, nil
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	r, err := s.Store.Get(ctx, g.Methodology, "")
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r.Methodology, nil
}

// integratorOf is what a guardian sees as `integrator`: the nearest ancestor change of its methodology (nil: none,
// guard.NoIntegrator).
func integratorOf(ancestors []domain.Change, methodology string) map[string]any {
	for _, a := range ancestors {
		if a.Methodology == methodology {
			return map[string]any{"attached": true, "id": string(a.ID), "methodology": a.Methodology, "state": a.State, "status": string(a.Status)}
		}
	}
	return nil
}
