package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/events"
)

// This file implements the lifecycle of a change (ADR 0058). The domain defines the lifecycle, the methodology of
// the change names it, the change holds its state; a transition is checked by the CEL guard of the lifecycle in the
// environment of the conditions, journaled as a KindTransition item, and moves the state. Leaving a state freezes
// the change impacts written in it; going back to a state it already occupied unfreezes it and asks for a new
// review of what was written after.

// ChangeLifecycles is what the graph needs of the registry to run the lifecycle of a change: the methodologies are
// not known to the graph.
type ChangeLifecycles interface {
	// The methodology every method takes is a name or a reference at a version (domain.MethodologyRef, ADR 0113): a
	// change reads the version it follows (domain.Change.MethodologyRef), a bare name the latest published one.

	// MethodologyVersion resolves the version a new change of a methodology follows (ADR 0113): the version a reference
	// names, when the registry holds it published (an archived version still serves the changes that follow it); the
	// latest published version for a bare name. "" when the registry does not know the methodology.
	MethodologyVersion(ctx context.Context, methodology string) (string, error)
	// Lifecycle returns the lifecycle the changes of a methodology follow; nil when it names none.
	Lifecycle(ctx context.Context, methodology string) (*domain.Lifecycle, error)
	// DefaultGoal returns the main goal the changes of a methodology start with (ADR 0096); "" when the methodology is
	// unknown or has none.
	DefaultGoal(ctx context.Context, methodology string) (string, error)
	// Guard evaluates the guard of a transition for the change on bb (condition.CheckGuard, the world state being the
	// one of the conditions of the methodology of the change).
	Guard(ctx context.Context, bb domain.Blackboard, expr, transition, decision string) (bool, error)
	// Gate evaluates the vetos and the objectives of a transition in the same environment (condition.CheckGate, ADR
	// 0075 §3); a transition that declares none is not asked.
	Gate(ctx context.Context, bb domain.Blackboard, t domain.Transition, decision string) (domain.GateResult, error)
	// Commit is the say of the methodology on each commit of a change (ADR 0066, 0146: the goal of the Activity it is
	// scoped to), on the blackboard of what it committed: decided false, it has none and the committable states of the
	// domain decide; decided true, ok says whether the commit stands, in place of the committable states.
	Commit(ctx context.Context, c domain.Change, bb domain.Blackboard) (decided, ok bool, err error)
	// Guardians is the guardian chain of a new root change of a methodology in a project (ADR 0106): the methodologies
	// integrating it in force in the project chain, followed upwards, outermost first, then the methodology itself.
	Guardians(ctx context.Context, methodology, project string) ([]domain.GuardianRef, error)
	// Integrates reports whether the changes of the methodology integrator integrate the changes of methodology, the
	// condition of an attachment (ADR 0106).
	Integrates(ctx context.Context, integrator, methodology string) (bool, error)
	// CheckGuardians asks the outer guardians of the chain of a change (domain.Change.OuterGuardians, innermost first)
	// about a transition, or about its commit when transition is "": refusal names the guardian that refused and why,
	// "" when they all agree. ancestors are the parent changes of the change, nearest first: the integrator a guardian
	// sees is the nearest one of its methodology.
	CheckGuardians(ctx context.Context, bb domain.Blackboard, transition string, ancestors []domain.Change) (refusal string, err error)
}

// ChangeTransitionAuthorizer decides whether the caller may take a transition of the lifecycle of a change. Nil allows
// every transition.
type ChangeTransitionAuthorizer func(ctx context.Context, c domain.Change, t domain.Transition) error

// changeLifecycle resolves the lifecycle a change follows.
func (g *Graph) changeLifecycle(ctx context.Context, c domain.Change) (*domain.Lifecycle, error) {
	if c.Lifecycle == "" {
		return nil, nil
	}
	if g.Lifecycles == nil {
		return nil, fmt.Errorf("change %s follows lifecycle %s, which nothing resolves: %w", c.ID, c.Lifecycle, ErrInvalid)
	}
	lc, err := g.Lifecycles.Lifecycle(ctx, c.MethodologyRef())
	if err != nil {
		return nil, err
	}
	if lc == nil || lc.Name != c.Lifecycle {
		return nil, fmt.Errorf("lifecycle %s of change %s is not the one of its methodology %q: %w", c.Lifecycle, c.ID, c.Methodology, ErrInvalid)
	}
	return lc, nil
}

// TransitionChange moves the state of a change along the transition named by the request (ADR 0058). Its guard and
// guardians are asked in the transaction of the move (ADR 0140).
func (g *Graph) TransitionChange(ctx context.Context, id domain.ChangeID, in TransitionRequest) (c domain.Change, err error) {
	err = g.inChange(ctx, id, func(ctx context.Context) error {
		c, err = g.transitionChange(ctx, id, in)
		return err
	})
	return c, err
}

func (g *Graph) transitionChange(ctx context.Context, id domain.ChangeID, in TransitionRequest) (domain.Change, error) {
	bb, err := g.Blackboard(ctx, id)
	if err != nil {
		return domain.Change{}, err
	}
	c := bb.Change
	switch c.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned, domain.ChangeCommitted:
		return c, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	if c.Lifecycle == "" {
		return c, fmt.Errorf("change %s follows no lifecycle: %w", id, ErrInvalid)
	}
	lc, err := g.changeLifecycle(ctx, c)
	if err != nil {
		return c, err
	}
	t, ok := lc.Transition(c.State, in.Transition)
	if !ok {
		return c, fmt.Errorf("change %s in state %s has no transition %q: %w", id, c.State, in.Transition, ErrInvalid)
	}
	if g.ChangeAuthorizer != nil {
		if err := g.ChangeAuthorizer(ctx, c, t); err != nil {
			return c, err
		}
	}
	consumed := map[string]bool{}
	for _, m := range c.StateMoves() {
		if m.Decision != "" {
			consumed[m.Decision] = true
		}
	}
	if in.Decision != "" {
		if consumed[in.Decision] {
			return c, fmt.Errorf("decision point %s already gated a transition: %w", in.Decision, ErrConflict)
		}
		found := false
		for _, d := range c.DecisionPointsAt(g.now(), g.DecisionPolicy) {
			found = found || d.ID == in.Decision
		}
		if !found {
			return c, fmt.Errorf("decision point %s of change %s: %w", in.Decision, id, ErrNotFound)
		}
	}
	gate, err := g.checkGate(ctx, bb, t, in.Decision, consumed)
	if err != nil {
		return c, err
	}
	if err := g.checkGuardians(ctx, bb, t.Name); err != nil {
		return c, err
	}
	err = g.changeTx(ctx, id, func(tx Tx) error {
		cur, err := changeOpen(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.State != c.State {
			return fmt.Errorf("change %s moved to %s meanwhile: %w", id, cur.State, ErrConflict)
		}
		reopened := slicesContainsMove(cur.StateMoves(), t.To) || t.To == lc.Initial && len(cur.StateMoves()) > 0
		data := map[string]any{"transition": t.Name, "from": t.From, "to": t.To}
		if in.Decision != "" {
			data["decision"] = in.Decision
		}
		if !t.Freezes.Empty() {
			// what the move freezes beyond its phase (ADR 0132 §4), read back from the move by the guards of the edits
			data["freezes"] = *t.Freezes
		}
		if gate.WithReserve() {
			// go with reserve (ADR 0075 §3): which objectives were not met and the derogation that covered each
			data["unmet"], data["reserve"] = gate.Unmet, gate.Reserve
		}
		it := domain.ChangeItem{ID: domain.ItemID(g.newID()), Type: domain.TypeTransition, Target: "transition." + t.Name, Status: domain.ItemAccepted,
			ProducedBy: firstNonEmpty(in.By, g.caller(ctx), "graph.transition"), Data: data, CreatedAt: g.now()}
		if err := g.checkItem(it); err != nil {
			return fmt.Errorf("%w: %w", err, ErrInvalid)
		}
		if err := putItem(ctx, tx, id, it); err != nil {
			return err
		}
		cur.State = t.To
		if cur.Status == domain.ChangeDraft {
			cur.Status = domain.ChangeActive
		}
		if err := tx.PutChange(ctx, cur); err != nil {
			return err
		}
		g.raise(ctx, tx, events.ChangeTransitioned, cur, nil)
		if reopened {
			return g.reopenPhase(ctx, tx, cur, t.To)
		}
		return nil
	})
	if err != nil {
		return c, err
	}
	return g.Change(ctx, id)
}

func slicesContainsMove(moves []domain.StateMove, to string) bool {
	for _, m := range moves {
		if m.To == to {
			return true
		}
	}
	return false
}

// checkGate evaluates the guard of a transition: the CEL guard of the lifecycle over the change, its decision points
// (the ones that already gated a transition left out) and its world state, then its vetos and objectives (ADR 0075 §3).
// The result says which objectives the transition goes with reserve of.
func (g *Graph) checkGate(ctx context.Context, bb domain.Blackboard, t domain.Transition, decision string, consumed map[string]bool) (domain.GateResult, error) {
	var res domain.GateResult
	if t.Guard == "" && len(t.Vetos) == 0 && len(t.Objectives) == 0 {
		return res, nil
	}
	var kept []domain.DecisionPoint
	for _, d := range domain.DecisionPointsOf(bb) {
		if !consumed[d.ID] {
			kept = append(kept, d)
		}
	}
	bb = bb.WithFacet(domain.FacetDecisionPoints, kept)
	if t.Guard != "" {
		ok, err := g.Lifecycles.Guard(ctx, bb, t.Guard, t.Name, decision)
		if err != nil {
			return res, invalidf("guard of %s on change %s: %v", t.Name, bb.Change.ID, err)
		}
		if !ok {
			return res, fmt.Errorf("change %s cannot take %s: gate not satisfied (%s): %w", bb.Change.ID, t.Name, t.Guard, ErrConflict)
		}
	}
	if len(t.Vetos) == 0 && len(t.Objectives) == 0 {
		return res, nil
	}
	res, err := g.Lifecycles.Gate(ctx, bb, t, decision)
	if err != nil {
		return res, invalidf("criteria of %s on change %s: %v", t.Name, bb.Change.ID, err)
	}
	if len(res.Vetoed) > 0 {
		return res, fmt.Errorf("change %s cannot take %s: vetoed by %s: %w", bb.Change.ID, t.Name, strings.Join(res.Vetoed, ", "), ErrConflict)
	}
	if u := res.Uncovered(); len(u) > 0 {
		return res, fmt.Errorf("change %s cannot take %s: objectives not met and not covered by a derogation: %s: %w", bb.Change.ID, t.Name, strings.Join(u, ", "), ErrConflict)
	}
	return res, nil
}

// phase is the state a change impact started its draft in, and where in the log (draftState.phaseOf).
type phase struct {
	State string
	Seq   int64
	// Before: started before the first move of the change, in the state that move left
	Before bool
}

// frozen reports whether the impact was written in a state other than the current one: leaving a state freezes
// its impacts (ADR 0058 §4), going back to it unfreezes them.
func (g *Graph) frozen(ctx context.Context, tx Tx, c domain.Change, impact domain.ChangeImpactID) (bool, string, error) {
	if c.State == "" {
		return false, "", nil
	}
	st, err := g.draftState(ctx, tx, c.ID)
	if err != nil {
		return false, "", err
	}
	p, ok := st.phaseOf(impact)
	if !ok || p.State == c.State {
		return false, p.State, nil
	}
	// the move that left the state of the impact may keep its phase editable (ADR 0132 §4)
	moves := c.StateMoves()
	for i := len(moves) - 1; i >= 0; i-- {
		if moves[i].From == p.State {
			return moves[i].Freezes == nil || !moves[i].Freezes.KeepPhase, p.State, nil
		}
	}
	return true, p.State, nil
}

// reopenPhase is made when a change goes back to a state it already occupied: what was written after it entered
// the state, in a later phase, must be reviewed again.
func (g *Graph) reopenPhase(ctx context.Context, tx Tx, c domain.Change, to string) error {
	moves := c.StateMoves()
	if len(moves) == 0 {
		return nil
	}
	st, err := g.draftState(ctx, tx, c.ID)
	if err != nil {
		return err
	}
	// the last entry into the state, before the move just journaled (c does not hold it yet)
	var entered domain.ItemID
	for _, m := range moves {
		if m.To == to {
			entered = m.Item
		}
	}
	since := st.moveSeq[entered]
	for _, cn := range c.Nodes {
		p, ok := st.phaseOf(cn.ID)
		if !ok || p.State == to || p.Seq < since || cn.Review != domain.ReviewAccepted {
			continue
		}
		r := domain.Review{Status: domain.ReviewProposed, By: g.caller(ctx), At: g.now(),
			Comment: fmt.Sprintf("written in %s, to be reviewed again: the change went back to %s", p.State, to)}
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactReviewed, Review: &r}); err != nil {
			return err
		}
	}
	return nil
}

// checkGuardians asks the outer guardians of the chain of a change (ADR 0106) about a transition, or about its commit
// when transition is "" (ADR 0146). A change whose chain is its own methodology only is not asked.
func (g *Graph) checkGuardians(ctx context.Context, bb domain.Blackboard, transition string) error {
	c := bb.Change
	if g.Lifecycles == nil || len(c.OuterGuardians()) == 0 {
		return nil
	}
	var ancestors []domain.Change
	for p, seen := c.ParentID, map[domain.ChangeID]bool{}; p != "" && !seen[p]; {
		seen[p] = true
		pc, err := g.Change(ctx, p)
		if err != nil {
			return err
		}
		ancestors = append(ancestors, pc)
		p = pc.ParentID
	}
	refusal, err := g.Lifecycles.CheckGuardians(ctx, bb, transition, ancestors)
	if err != nil {
		return invalidf("guardians of change %s: %v", c.ID, err)
	}
	if refusal == "" {
		return nil
	}
	what := "commit"
	if transition != "" {
		what = "take " + transition
	}
	return fmt.Errorf("change %s cannot %s: its guardian %s: %w", c.ID, what, refusal, ErrConflict)
}

// guardianChain is the guardian chain of a new change (ADR 0106): a sub-change takes its parent's chain and adds its
// own methodology; a root change gets the chain the registry computes for its methodology in its project.
func (g *Graph) guardianChain(ctx context.Context, in NewChange, version string) ([]domain.GuardianRef, error) {
	if g.Lifecycles == nil || in.Methodology == "" {
		return nil, nil
	}
	if in.ParentID == "" {
		return g.Lifecycles.Guardians(ctx, domain.MethodologyRef(in.Methodology, version), in.ProjectID)
	}
	parent, err := g.Change(ctx, in.ParentID)
	if err != nil {
		return nil, err
	}
	return chainUnder(parent, in.Methodology, &domain.GuardianRef{Methodology: in.Methodology, Version: version}), nil
}

// chainUnder is the chain of a change of methodology under a parent: the parent's chain (its own methodology when it
// has none) then the methodology, unless it is already the last one. own, when set, is the version-pinned reference
// of the methodology.
func chainUnder(parent domain.Change, methodology string, own *domain.GuardianRef) []domain.GuardianRef {
	chain := slices.Clone(parent.Guardians)
	if len(chain) == 0 && parent.Methodology != "" {
		chain = []domain.GuardianRef{{Methodology: parent.Methodology}}
	}
	if n := len(chain); methodology != "" && (n == 0 || chain[n-1].Methodology != methodology) {
		ref := domain.GuardianRef{Methodology: methodology}
		if own != nil {
			ref = *own
		}
		chain = append(chain, ref)
	}
	return chain
}
