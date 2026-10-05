package graph

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
)

// This file implements the lifecycle of a change (ADR 0058). The domain defines the lifecycle, the methodology of
// the change names it, the change holds its state; a transition is checked by the CEL guard of the lifecycle in the
// environment of the conditions, journaled as a KindTransition item, and moves the state. Leaving a state freezes
// the change impacts written in it; going back to a state it already occupied unfreezes it and asks for a new
// review of what was written after.

// ChangeLifecycles is what the graph needs of the registry to run the lifecycle of a change: the methodologies are
// not known to the graph.
type ChangeLifecycles interface {
	// Lifecycle returns the lifecycle the changes of a methodology follow; nil when it names none.
	Lifecycle(ctx context.Context, methodology string) (*domain.Lifecycle, error)
	// Guard evaluates the guard of a transition for the change on bb (condition.CheckGuard, the world state being the
	// one of the conditions of the methodology of the change).
	Guard(ctx context.Context, bb domain.Blackboard, expr, transition, decision string) (bool, error)
}

// ChangeTransitionAuthorizer decides whether the caller may take a transition of the lifecycle of a change. Nil allows
// every transition.
type ChangeTransitionAuthorizer func(ctx context.Context, c domain.Change, t domain.Transition) error

// TransitionRequest asks for a transition of the lifecycle of a change.
type TransitionRequest struct {
	// Transition is the name of the transition out of the current state.
	Transition string
	// Decision is the decision point that gates the transition, when its guard asks for one: it is seen by the guard
	// as change.decision, and is consumed by the move (a point gates one transition only).
	Decision string
	// By is the caller, recorded in the journal.
	By string
}

// changeLifecycle resolves the lifecycle a change follows.
func (g *Graph) changeLifecycle(ctx context.Context, c domain.Change) (*domain.Lifecycle, error) {
	if c.Lifecycle == "" {
		return nil, nil
	}
	if g.Lifecycles == nil {
		return nil, fmt.Errorf("change %s follows lifecycle %s, which nothing resolves: %w", c.ID, c.Lifecycle, ErrInvalid)
	}
	lc, err := g.Lifecycles.Lifecycle(ctx, c.Methodology)
	if err != nil {
		return nil, err
	}
	if lc == nil || lc.Name != c.Lifecycle {
		return nil, fmt.Errorf("lifecycle %s of change %s is not the one of its methodology %q: %w", c.Lifecycle, c.ID, c.Methodology, ErrInvalid)
	}
	return lc, nil
}

// TransitionChange moves the state of a change along the transition named by the request (ADR 0058).
func (g *Graph) TransitionChange(ctx context.Context, id domain.ChangeID, in TransitionRequest) (domain.Change, error) {
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
	if err := g.checkGate(ctx, bb, t, in.Decision, consumed); err != nil {
		return c, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
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
		it := domain.ChangeItem{ID: domain.ItemID(g.newID()), Kind: domain.KindTransition, Type: "transition." + t.Name, Status: domain.ItemAccepted,
			ProducedBy: firstNonEmpty(in.By, g.caller(ctx), "graph.transition"), Data: data, CreatedAt: g.now()}
		if err := it.Validate(); err != nil {
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
// (the ones that already gated a transition left out) and its world state.
func (g *Graph) checkGate(ctx context.Context, bb domain.Blackboard, t domain.Transition, decision string, consumed map[string]bool) error {
	if t.Guard == "" {
		return nil
	}
	var kept []domain.DecisionPoint
	for _, d := range domain.DecisionPointsOf(bb) {
		if !consumed[d.ID] {
			kept = append(kept, d)
		}
	}
	bb = bb.WithFacet(domain.FacetDecisionPoints, kept)
	ok, err := g.Lifecycles.Guard(ctx, bb, t.Guard, t.Name, decision)
	if err != nil {
		return invalidf("guard of %s on change %s: %v", t.Name, bb.Change.ID, err)
	}
	if !ok {
		return fmt.Errorf("change %s cannot take %s: gate not satisfied (%s): %w", bb.Change.ID, t.Name, t.Guard, ErrConflict)
	}
	return nil
}

// phases is the state each change impact was last written in, by replaying the journal of the transitions against
// the impact log: the state before the first move is the one the first move leaves.
func (g *Graph) phases(ctx context.Context, tx Tx, c domain.Change) (map[domain.ChangeImpactID]phase, error) {
	moves := c.StateMoves()
	if len(moves) == 0 {
		return nil, nil
	}
	entries, err := tx.Log(ctx, domain.LogFilter{Change: c.ID, Types: []string{domain.LogFact + ".", domain.LogImpact + "."}})
	if err != nil {
		return nil, err
	}
	at := map[domain.ItemID]domain.StateMove{}
	for _, m := range moves {
		at[m.Item] = m
	}
	state := moves[0].From
	out := map[domain.ChangeImpactID]phase{}
	for _, l := range entries {
		switch {
		case l.Type == domain.LogFact+"."+string(domain.KindTransition):
			var it domain.ChangeItem
			if err := json.Unmarshal(l.Payload, &it); err != nil {
				return nil, err
			}
			if m, ok := at[it.ID]; ok {
				state = m.To
			}
		case len(l.Type) > len(domain.LogImpact) && l.Type[:len(domain.LogImpact)] == domain.LogImpact:
			var ev domain.ImpactEvent
			if err := json.Unmarshal(l.Payload, &ev); err != nil {
				return nil, err
			}
			if ev.Op == domain.ImpactWritten && ev.Flow == "" {
				out[ev.Impact] = phase{State: state, Seq: l.Seq}
			}
		}
	}
	return out, nil
}

type phase struct {
	State string
	Seq   int64
}

// frozen reports whether the impact was written in a state other than the current one: leaving a state freezes
// its impacts (ADR 0058 §4), going back to it unfreezes them.
func (g *Graph) frozen(ctx context.Context, tx Tx, c domain.Change, impact domain.ChangeImpactID) (bool, string, error) {
	if c.State == "" {
		return false, "", nil
	}
	ph, err := g.phases(ctx, tx, c)
	if err != nil {
		return false, "", err
	}
	p, ok := ph[impact]
	return ok && p.State != c.State, p.State, nil
}

// reopenPhase is made when a change goes back to a state it already occupied: what was written after it entered
// the state, in a later phase, must be reviewed again.
func (g *Graph) reopenPhase(ctx context.Context, tx Tx, c domain.Change, to string) error {
	ph, err := g.phases(ctx, tx, c)
	if err != nil {
		return err
	}
	// the last entry into the state, before the move just journaled (c does not hold it yet)
	var entered string
	for _, m := range c.StateMoves() {
		if m.To == to {
			entered = string(m.Item)
		}
	}
	var since int64
	entries, err := tx.Log(ctx, domain.LogFilter{Change: c.ID, Types: []string{domain.LogFact + "." + string(domain.KindTransition)}})
	if err != nil {
		return err
	}
	for _, l := range entries {
		var it domain.ChangeItem
		if err := json.Unmarshal(l.Payload, &it); err != nil {
			return err
		}
		if string(it.ID) == entered {
			since = l.Seq
		}
	}
	for _, cn := range c.Nodes {
		p, ok := ph[cn.ID]
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

// checkFinalState refuses to commit a change whose lifecycle has not reached a final state (ADR 0058 §4). It is made
// before the transaction of the commit, the registry that resolves the lifecycle reading the graph.
func (g *Graph) checkFinalState(ctx context.Context, id domain.ChangeID) error {
	c, err := g.Change(ctx, id)
	if err != nil || c.Lifecycle == "" {
		return nil // a change that does not exist is refused by the commit
	}
	lc, err := g.changeLifecycle(ctx, c)
	if err != nil {
		return err
	}
	if s, ok := lc.State(c.State); !ok || !s.Final {
		return fmt.Errorf("change %s is in state %s of lifecycle %s: it can be applied once it is in a final state: %w", c.ID, c.State, c.Lifecycle, ErrConflict)
	}
	return nil
}
