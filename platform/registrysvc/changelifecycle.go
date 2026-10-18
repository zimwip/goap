package registrysvc

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/lib/def"
)

// Lifecycle implements pkg/graph.ChangeLifecycles (ADR 0058): the lifecycle the changes of a methodology follow, the
// one it names in the domain of its namespace (else in any domain, a built-in one for instance). A methodology the
// registry does not know, or one naming none, gives nil: its changes have no state.
func (s *Service) Lifecycle(ctx context.Context, methodology string) (*domain.Lifecycle, error) {
	r, err := s.record(ctx, methodology)
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
	// the lifecycle of the domain versions the methodology version is written against (ADR 0113)
	ds, err := s.domainsFor(ctx, &m, r.PublishedAt)
	if err != nil {
		return nil, err
	}
	recs := builtinRecords()
	for _, d := range ds {
		recs = append(recs, def.Record{Domain: *d, Status: StatusPublished})
	}
	found := lifecycleIn(recs, &m)
	if found == nil {
		return nil, fmt.Errorf("methodology %s names lifecycle %q, which no domain defines: %w", m.Name, m.Lifecycle, ErrInvalid)
	}
	if !hasGates(&m) {
		return found, nil
	}
	c, err := s.Methodology(ctx, methodology)
	if err != nil {
		return nil, fmt.Errorf("gates of methodology %s: %w", m.Name, err)
	}
	return WithGates(found, c.Gates())
}

// hasGates says whether a process of m has a gate step (ADR 0132).
func hasGates(m *methodology.Methodology) bool {
	var in func([]methodology.Step) bool
	in = func(steps []methodology.Step) bool {
		for _, st := range steps {
			if st.Gate != nil || in(st.Steps) {
				return true
			}
		}
		return false
	}
	for _, p := range m.Processes {
		if in(p.Steps) {
			return true
		}
	}
	return false
}

// WithGates is the lifecycle with the criteria and the freezes of the gates of the methodology merged into the
// transitions they take (ADR 0132 §2, §4): the graph enforces them as it does the ones the domain declares.
func WithGates(lc *domain.Lifecycle, gates []methodology.GateInfo) (*domain.Lifecycle, error) {
	out := *lc
	out.Transitions = slices.Clone(lc.Transitions)
	for _, g := range gates {
		found := false
		for i := range out.Transitions {
			t := &out.Transitions[i]
			if t.Name != g.Transition {
				continue
			}
			found = true
			vetos, objectives := g.Criteria()
			t.Vetos = append(slices.Clone(t.Vetos), vetos...)
			t.Objectives = append(slices.Clone(t.Objectives), objectives...)
			if g.Freezes != nil && !g.Freezes.Empty() {
				f := *g.Freezes
				if t.Freezes != nil {
					f = mergeFreezes(*t.Freezes, f)
				}
				t.Freezes = &f
			}
		}
		if !found {
			return nil, fmt.Errorf("gate %s takes transition %s, which lifecycle %s has not: %w", g.Path, g.Transition, lc.Name, ErrInvalid)
		}
	}
	return &out, nil
}

func mergeFreezes(a, b domain.Freezes) domain.Freezes {
	add := func(x, y []string) []string {
		out := slices.Clone(x)
		for _, v := range y {
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		return out
	}
	return domain.Freezes{Fields: add(a.Fields, b.Fields), Types: add(a.Types, b.Types),
		Transitions: a.Transitions || b.Transitions, KeepPhase: a.KeepPhase || b.KeepPhase}
}

// lifecycleIn is the lifecycle m names among the domains: the one of its namespace, else of any domain (nil: none).
func lifecycleIn(recs []def.Record, m *methodology.Methodology) *domain.Lifecycle {
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
	r, err := s.record(ctx, name)
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
	c, err := s.Methodology(ctx, bb.Change.MethodologyRef())
	if err != nil {
		return false, err
	}
	return condition.CheckGuard(expr, bb, c.Conditions.Evaluate(bb).State, transition, decision)
}

// Gate implements pkg/graph.ChangeLifecycles: the vetos and objectives of a transition (ADR 0075 §3), in the same
// environment as its guard.
func (s *Service) Gate(ctx context.Context, bb domain.Blackboard, t domain.Transition, decision string) (domain.GateResult, error) {
	c, err := s.Methodology(ctx, bb.Change.MethodologyRef())
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
	r, err := s.record(ctx, methodology)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	roles, restricted = r.Methodology.OperationRoles(op)
	return roles, restricted, nil
}
