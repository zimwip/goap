package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/changeapi"
	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine/blackboard"
)

// This file is the lifecycle of a change (ADR 0058) as the engine runs it (ADR 0098): the methodology names the
// lifecycle (a lifecycle of a domain), the engine moves the state of the change along it and records each move as
// change objects of the built-in domain execution (execution@Transition, execution@State); the change knows only
// whether it is open or closed. Leaving a state freezes the impacts whose draft was started in it (the guardian of the
// change refuses their edits, Guardian.MayEdit); going back to a state already occupied asks for a new review of what
// was accepted after it was left.

// enginePrincipal is the identity the engine writes the state of a change with (execution@State, execution@Transition).
var enginePrincipal = authz.System("engine")

// LifecyclePort resolves the lifecycle the changes of a methodology follow (the registry: the methodology names it, a
// domain defines it); nil when the methodology names none.
type LifecyclePort interface {
	Lifecycle(ctx context.Context, methodology string) (*domain.Lifecycle, error)
}

// TransitionRequest asks for a transition of the lifecycle of a change.
type TransitionRequest struct {
	// Transition is the name of the transition out of the current state.
	Transition string
	// Decision is the decision point that gates the transition, when its guard asks for one: it is seen by the guard
	// as change.decision, and is consumed by the move (a point gates one transition only).
	Decision string
	// By is the caller, recorded on the move.
	By string
}

// TransitionResult is the state a transition leaves the change in.
type TransitionResult struct {
	State blackboard.StateRef
	// Move is the key of the execution@Transition change object recording the move.
	Move string
}

// lifecycleOf resolves the lifecycle the change follows: nil for a change whose methodology names none.
func (e *Engine) lifecycleOf(ctx context.Context, c domain.Change) (*domain.Lifecycle, error) {
	if c.Methodology == "" || e.Lifecycles == nil {
		return nil, nil
	}
	return e.Lifecycles.Lifecycle(ctx, c.Methodology)
}

// stateOf is the state of the change in its lifecycle: the recorded one, else the initial state of the lifecycle.
func stateOf(v blackboard.View, lc *domain.Lifecycle) blackboard.StateRef {
	if st, ok := v.State(); ok {
		return st
	}
	return blackboard.StateRef{Lifecycle: lc.Name, State: lc.Initial}
}

// TransitionChange moves the state of a change along the transition the request names (ADR 0058): the transition must
// leave the current state, the caller may take it (TransitionAuthorizer), the decision point that gates it is decided
// and unused, its guard holds and its vetos and objectives pass (an objective not met is covered by a derogation:
// the move goes with reserve). The move is written with the new state in one write of the change, over the state read
// (a concurrent move is a conflict).
func (e *Engine) TransitionChange(ctx context.Context, id domain.ChangeID, in TransitionRequest) (TransitionResult, error) {
	var out TransitionResult
	bb, err := e.Graph.Blackboard(ctx, id)
	if err != nil {
		return out, err
	}
	c := bb.Change
	switch c.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned, domain.ChangeCommitted:
		return out, fmt.Errorf("change %s is %s: %w", id, c.Status, changeapi.ErrConflict)
	}
	lc, err := e.lifecycleOf(ctx, c)
	if err != nil {
		return out, err
	}
	if lc == nil {
		return out, fmt.Errorf("change %s follows no lifecycle: %w", id, changeapi.ErrInvalid)
	}
	view := blackboard.Of(bb)
	cur := stateOf(view, lc)
	t, ok := lc.Transition(cur.State, in.Transition)
	if !ok {
		return out, fmt.Errorf("change %s in state %s has no transition %q: %w", id, cur.State, in.Transition, changeapi.ErrInvalid)
	}
	if e.TransitionAuthorizer != nil {
		if err := e.TransitionAuthorizer(ctx, c, t); err != nil {
			return out, err
		}
	}
	moves := view.Moves()
	consumed := map[string]bool{}
	for _, m := range moves {
		if m.Decision != "" {
			consumed[m.Decision] = true
		}
	}
	if in.Decision != "" {
		if consumed[in.Decision] {
			return out, fmt.Errorf("decision point %s already gated a transition: %w", in.Decision, changeapi.ErrConflict)
		}
		found := false
		for _, d := range domain.DecisionPointsOf(bb) {
			found = found || d.ID == in.Decision
		}
		if !found {
			return out, fmt.Errorf("decision point %s of change %s: %w", in.Decision, id, changeapi.ErrNotFound)
		}
	}
	gate, err := e.checkGate(ctx, bb, t, in.Decision, consumed)
	if err != nil {
		return out, err
	}
	move := blackboard.Move{Transition: t.Name, From: t.From, To: t.To, Decision: in.Decision, By: in.By}
	var unmet, reserve any
	if gate.WithReserve() {
		unmet, reserve = plain(gate.Unmet), plain(gate.Reserve)
	}
	// the engine writes the move as itself (the change refuses these types to anyone else), the caller is on the move
	written, err := e.Graph.PutObjects(authz.With(ctx, enginePrincipal), id, []domain.ObjectWrite{blackboard.RecordMove(move, unmet, reserve), blackboard.SetState(lc.Name, t.To, cur.Version)})
	if err != nil {
		return out, err
	}
	out.Move = written[0].Key
	out.State = blackboard.StateRef{Lifecycle: lc.Name, State: t.To, Version: written[1].Version}
	// going back to a state the change already occupied: what was accepted since it was left is reviewed again
	back := t.To == lc.Initial && len(moves) > 0
	for _, m := range moves {
		back = back || m.To == t.To
	}
	if back {
		if err := e.reopenSince(ctx, c, moves, t.To); err != nil {
			e.log().Warn("reviews not reopened after a move back", "change", id, "state", t.To, "err", err)
		}
	}
	return out, nil
}

// plain is a value as JSON reads it back (a change object holds JSON values).
func plain(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// checkGate evaluates the guard of a transition: the CEL guard of the lifecycle over the change, its decision points
// (the ones that already gated a transition left out) and the world state of its methodology, then its vetos and
// objectives (ADR 0075 §3).
func (e *Engine) checkGate(ctx context.Context, bb domain.Blackboard, t domain.Transition, decision string, consumed map[string]bool) (domain.GateResult, error) {
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
	m, err := e.Methodologies.Methodology(ctx, bb.Change.Methodology)
	if err != nil {
		return res, err
	}
	world := m.Conditions.Evaluate(bb).State
	if t.Guard != "" {
		ok, err := condition.CheckGuard(t.Guard, bb, world, t.Name, decision)
		if err != nil {
			return res, fmt.Errorf("guard of %s on change %s: %v: %w", t.Name, bb.Change.ID, err, changeapi.ErrInvalid)
		}
		if !ok {
			return res, fmt.Errorf("change %s cannot take %s: gate not satisfied (%s): %w", bb.Change.ID, t.Name, t.Guard, changeapi.ErrConflict)
		}
	}
	if len(t.Vetos) == 0 && len(t.Objectives) == 0 {
		return res, nil
	}
	res, err = condition.CheckGate(t, bb, world, decision)
	if err != nil {
		return res, fmt.Errorf("criteria of %s on change %s: %v: %w", t.Name, bb.Change.ID, err, changeapi.ErrInvalid)
	}
	if len(res.Vetoed) > 0 {
		return res, fmt.Errorf("change %s cannot take %s: vetoed by %s: %w", bb.Change.ID, t.Name, strings.Join(res.Vetoed, ", "), changeapi.ErrConflict)
	}
	if u := res.Uncovered(); len(u) > 0 {
		return res, fmt.Errorf("change %s cannot take %s: objectives not met and not covered by a derogation: %s: %w", bb.Change.ID, t.Name, strings.Join(u, ", "), changeapi.ErrConflict)
	}
	return res, nil
}

// phase is the state of the lifecycle an impact's draft was started in, and where in the log of the change.
type phase struct {
	State string
	Seq   int64
}

// phases is the state each impact of the main workspace was last started in (created, checked out, installed by an
// adoption: ADR 0079), by replaying the moves against the impact log: the state before the first move is the one it
// leaves. An edit or a transition of the draft does not set it.
func (e *Engine) phases(ctx context.Context, id domain.ChangeID, moves []blackboard.Move) (map[domain.ChangeImpactID]phase, error) {
	if len(moves) == 0 {
		return nil, nil
	}
	entries, _, err := e.Graph.ChangeLog(ctx, domain.LogFilter{Change: id, Types: []string{domain.LogImpact + "."}})
	if err != nil {
		return nil, err
	}
	out := map[domain.ChangeImpactID]phase{}
	for _, l := range entries {
		var ev domain.ImpactEvent
		if err := json.Unmarshal(l.Payload, &ev); err != nil {
			return nil, err
		}
		if !ev.StartsDraft() || ev.Flow != "" {
			continue
		}
		state := moves[0].From
		for _, m := range moves {
			if m.Seq < l.Seq {
				state = m.To
			}
		}
		out[ev.Impact] = phase{State: state, Seq: l.Seq}
	}
	return out, nil
}

// reopenSince sends back to proposed the accepted impacts started in another state since the change last left to
// (moves are the ones before the move back): they were accepted in a later phase, which the change has undone.
func (e *Engine) reopenSince(ctx context.Context, c domain.Change, moves []blackboard.Move, to string) error {
	ph, err := e.phases(ctx, c.ID, moves)
	if err != nil {
		return err
	}
	var since int64
	for _, m := range moves {
		if m.To == to {
			since = m.Seq
		}
	}
	cur, err := e.Graph.Change(ctx, c.ID)
	if err != nil {
		return err
	}
	var reopen []domain.ChangeImpactID
	for _, cn := range cur.Nodes {
		p, ok := ph[cn.ID]
		if !ok || p.State == to || p.Seq < since || cn.Review != domain.ReviewAccepted {
			continue
		}
		reopen = append(reopen, cn.ID)
	}
	if len(reopen) == 0 {
		return nil
	}
	_, err = e.Graph.ReopenImpacts(ctx, c.ID, reopen, fmt.Sprintf("written in a later state, to be reviewed again: the change went back to %s", to))
	return err
}

// ensureState records the initial state of the lifecycle of the change when nothing recorded one, so the conditions
// of a run read it (change.state, "state:<name>"). Best effort: a concurrent writer wins.
func (e *Engine) ensureState(ctx context.Context, id domain.ChangeID, methodology string) {
	if methodology == "" || e.Lifecycles == nil {
		return
	}
	lc, err := e.Lifecycles.Lifecycle(ctx, methodology)
	if err != nil || lc == nil {
		return
	}
	objs, err := e.Graph.Objects(ctx, id, domain.ObjectFilter{Types: []string{blackboard.TypeState}})
	if err != nil || len(objs) > 0 {
		return
	}
	if _, err := e.Graph.PutObjects(authz.With(ctx, enginePrincipal), id, []domain.ObjectWrite{blackboard.SetState(lc.Name, lc.Initial, 0)}); err != nil && !errors.Is(err, changeapi.ErrConflict) {
		e.log().Debug("initial state not recorded", "change", id, "err", err)
	}
}
