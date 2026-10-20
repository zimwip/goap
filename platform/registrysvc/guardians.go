package registrysvc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/lib/def"
)

// The guardian chain of a change (ADR 0106): the methodologies that frame it, outermost first, its own last, each
// declaring in its definition what it requires of the changes it integrates (integrates, guardian). The graph saves
// the chain on the change and asks the registry to evaluate it.

// maxChain bounds the walk up the integrations (a cycle is cut by the methodologies already seen anyway).
const maxChain = 8

// Guardians implements graph.ChangeLifecycles: the guardian chain of a new root change of a methodology in a
// project. From the methodology, the methodologies applicable to the project chain (nearest project first, Applicable)
// are searched for one integrating it; the first found frames it, and the search goes on from that one. Without
// Applicable, or for a methodology the registry does not know, the chain is the methodology alone.
func (s *Service) Guardians(ctx context.Context, ref, project string) ([]domain.GuardianRef, error) {
	name, _ := domain.SplitMethodologyRef(ref)
	own, err := s.record(ctx, ref)
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

// Integrates implements graph.ChangeLifecycles: whether integrator (at the version its reference names, else the
// latest) integrates the changes of methodology; false for a methodology the registry does not know.
func (s *Service) Integrates(ctx context.Context, integrator, methodology string) (bool, error) {
	r, err := s.record(ctx, integrator)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return r.Methodology.IntegratesChangesOf(methodology), nil
}

// CheckGuardians implements graph.ChangeLifecycles: the outer guardians of the chain of the change, innermost
// first, each with its guard for the transition (guardian.transitions) or a commit (CommitGuard: guardian.commit, and
// an attached integrator when its integration is exclusive), in the environment of the conditions of the change's
// own methodology plus `integrator`, the nearest ancestor change of that guardian's methodology.
func (s *Service) CheckGuardians(ctx context.Context, bb domain.Blackboard, transition string, ancestors []domain.Change) (string, error) {
	c := bb.Change
	var world map[string]bool
	worldOf := func() map[string]bool {
		if world == nil {
			world = map[string]bool{}
			if cm, err := s.Methodology(ctx, c.MethodologyRef()); err == nil && cm != nil {
				world = cm.Conditions.Evaluate(bb).State
			}
		}
		return world
	}
	for i := len(c.Guardians) - 1; i >= 0; i-- {
		g := c.Guardians[i]
		if g.Methodology == c.Methodology {
			continue // its own: the lifecycle and the commits of the change
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
			expr = m.CommitGuard(inner)
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

// integrationIssues reports a cycle of integrations (ADR 0106): m integrating, directly or through the methodologies
// it integrates, a methodology that integrates m. Compared with the latest version of each stored methodology; a
// wildcard ("*") is not followed. Nothing is reported when the store cannot be read.
func (s *Service) integrationIssues(ctx context.Context, m *methodology.Methodology) def.Issues {
	if len(m.Integrates) == 0 {
		return nil
	}
	rs, err := s.Versions(ctx, false)
	if err != nil {
		return nil
	}
	integrates := map[string][]string{}
	for _, r := range rs {
		if r.Methodology.Name != m.Name {
			integrates[r.Methodology.Name] = integratedNames(&r.Methodology)
		}
	}
	integrates[m.Name] = integratedNames(m)
	var out def.Issues
	for i, in := range m.Integrates {
		for _, n := range in.Methodologies {
			if path := integrationPath(integrates, n, m.Name); path != nil {
				out = append(out, def.Issue{Path: fmt.Sprintf("integrates[%d].methodologies", i),
					Message: fmt.Sprintf("integration cycle: %s integrates %s", m.Name, strings.Join(path, " integrates "))})
			}
		}
	}
	return out
}

// integratedNames are the methodologies m names in its integrations, the wildcard left out.
func integratedNames(m *methodology.Methodology) []string {
	var out []string
	for _, in := range m.Integrates {
		for _, n := range in.Methodologies {
			if n != methodology.AnyMethodology {
				out = append(out, n)
			}
		}
	}
	return out
}

// integrationPath is a path of integrations from `from` to `to` (both included), nil when there is none.
func integrationPath(integrates map[string][]string, from, to string) []string {
	prev := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == to {
			var path []string
			for n := cur; n != ""; n = prev[n] {
				path = append([]string{n}, path...)
			}
			return path
		}
		for _, n := range integrates[cur] {
			if _, seen := prev[n]; !seen {
				prev[n] = cur
				queue = append(queue, n)
			}
		}
	}
	return nil
}

// compositionIssues checks m against the methodologies it is composed with (ADR 0106, methodology.CompositionIssues):
// the latest version of each other stored methodology and the lifecycles of the domains in force. Warnings only;
// nothing is reported when the store cannot be read.
func (s *Service) compositionIssues(ctx context.Context, m *methodology.Methodology) def.Issues {
	rs, err := s.Versions(ctx, false)
	if err != nil {
		return nil
	}
	others := map[string]*methodology.Methodology{}
	for _, r := range rs {
		if r.Methodology.Name != m.Name && r.Status != StatusArchived {
			mm := r.Methodology
			others[mm.Name] = &mm
		}
	}
	composed := len(m.Integrates) > 0 || m.Guardian != nil || len(m.On) > 0
	for _, o := range others {
		composed = composed || o.IntegratesChangesOf(m.Name) || m.IntegratesChangesOf(o.Name) || slices.Contains(o.AppliesTo, m.Name)
	}
	if !composed {
		return nil
	}
	domains, err := s.domainVersions(ctx, false)
	if err != nil {
		return nil
	}
	return m.CompositionIssues(methodology.Composition{Others: others,
		Lifecycle: func(x *methodology.Methodology) *domain.Lifecycle { return lifecycleIn(domains, x) }})
}
