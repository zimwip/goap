package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// The blackboard of a change is an append-only log (domain/flow.go). Relaunching
// step K of a run opens a flow branch: what step K and the steps after it
// produced (with everything derived from it) is marked stale, a new process
// replans from the state before step K and appends its items to the branch as
// candidates, and once it reaches the goal a human adopts the branch (the old
// outputs are superseded) or discards it (the old outputs count again).

func lastItem(c domain.ChangeSet) domain.ItemID {
	for i := len(c.Items) - 1; i >= 0; i-- {
		if c.Items[i].Kind != domain.KindFlow {
			return c.Items[i].ID
		}
	}
	return ""
}

// Relaunch restarts a run from step `step`: it opens a flow branch on the change and returns the
// new process (status running, not scheduled yet: the caller runs it).
func (e *Engine) Relaunch(ctx context.Context, id string, step int, reason string) (*Process, error) {
	defer e.lock(id)()
	return e.relaunchLocked(ctx, id, step, reason)
}

// relaunchLocked is Relaunch with the lock of the process already held.
func (e *Engine) relaunchLocked(ctx context.Context, id string, step int, reason string) (*Process, error) {
	old, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	switch {
	case old.ChangeID == "":
		return nil, fmt.Errorf("process %s has no change: %w", id, ErrInvalidState)
	case old.ParentID != "":
		return nil, fmt.Errorf("process %s is a sub-agent run: relaunch the step of its parent: %w", id, ErrInvalidState)
	case old.Status == StatusRunning || old.Status == StatusClarifying || old.Status == StatusSuperseded:
		return nil, fmt.Errorf("process %s is %s: %w", id, old.Status, ErrInvalidState)
	case step < 0 || step >= len(old.Steps):
		return nil, fmt.Errorf("process %s has no step %d: %w", id, step, ErrInvalidState)
	}
	seeds, err := e.relaunchedItems(ctx, old, step)
	if err != nil {
		return nil, err
	}
	who := authz.From(ctx)
	if who.Anonymous() {
		who = old.Initiator
	}
	flow, err := e.Graph.OpenFlow(ctx, old.ChangeID, graph.OpenFlowRequest{Parent: old.Flow, ForkAfter: old.Steps[step].LastItem, Seeds: seeds,
		FromStep: step, Execution: old.Steps[step].Execution, Process: old.ID, Reason: reason})
	if err != nil {
		return nil, err
	}
	p := &Process{ID: uuid.NewString(), Methodology: old.Methodology, MethodologyVersion: old.MethodologyVersion, Agent: old.Agent, Planner: old.Planner,
		Goal: old.Goal, ChangeID: old.ChangeID, BaselineID: old.BaselineID, Namespace: old.Namespace, Title: fmt.Sprintf("%s (relaunch from step %d)", old.Title, step),
		Trigger: old.Trigger, Initiator: who, Vars: maps.Clone(old.Vars), Disabled: map[string]bool{}, Status: StatusRunning,
		Flow: flow.ID, RelaunchOf: old.ID, FromStep: step, CreatedAt: e.clock(), UpdatedAt: e.clock()}
	p.Intent = old.Intent
	if err := e.Store.Put(ctx, p); err != nil {
		return nil, err
	}
	e.log().Info("step relaunched", "process", old.ID, "step", step, "flow", flow.ID, "stale", len(flow.Stale), "new", p.ID)
	return p, nil
}

// relaunchedItems are the items the step and what followed it produced on the
// flow of the process: those of its own later steps and of the sub-agent runs
// started from step onwards (found in the journal).
func (e *Engine) relaunchedItems(ctx context.Context, old *Process, step int) ([]domain.ItemID, error) {
	execs := map[string]bool{}
	for _, s := range old.Steps[step:] {
		if s.Execution != "" {
			execs[s.Execution] = true
		}
	}
	recs, err := e.Graph.Journal(ctx, domain.ExecutionFilter{ChangeID: old.ChangeID})
	if err != nil {
		return nil, err
	}
	since := old.Steps[step].StartedAt
	desc := map[string]bool{old.ID: true}
	for changed := true; changed; {
		changed = false
		for _, r := range recs {
			if r.ParentProcessID != "" && desc[r.ParentProcessID] && !desc[r.ProcessID] && !r.StartedAt.Before(since) {
				desc[r.ProcessID], changed = true, true
			}
		}
	}
	for _, r := range recs {
		if r.ProcessID != old.ID && desc[r.ProcessID] && r.Kind == domain.ExecAction {
			execs[r.ID] = true
		}
	}
	bb, err := e.Graph.BlackboardIn(ctx, old.ChangeID, old.Flow)
	if err != nil {
		return nil, err
	}
	var seeds []domain.ItemID
	for _, it := range bb.Change.Items {
		if it.Execution != "" && execs[it.Execution] {
			seeds = append(seeds, it.ID)
		}
	}
	return seeds, nil
}

// DecideFlow adopts or discards the flow branch of a relaunched process. Adopting requires the
// process to have reached its goal (it waits for this decision): the run it replaces becomes
// superseded. Discarding is possible at any time while the branch is open.
func (e *Engine) DecideFlow(ctx context.Context, id string, adopt bool, comment string) (*Process, error) {
	defer e.lock(id)()
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Flow == "" {
		return nil, fmt.Errorf("process %s does not run on a flow branch: %w", id, ErrInvalidState)
	}
	by := authz.From(ctx).Subject
	decision := "discarded"
	if adopt {
		if p.Status != StatusWaiting || p.Pending == nil || p.Pending.Kind != TaskFlow {
			return nil, fmt.Errorf("process %s has not reached its goal on flow %s yet: %w", id, p.Flow, ErrInvalidState)
		}
		if _, err := e.Graph.AdoptFlow(ctx, p.ChangeID, p.Flow, by); err != nil {
			return nil, err
		}
		decision = "adopted"
		if p.RelaunchOf != "" {
			if err := e.supersede(ctx, p.RelaunchOf); err != nil {
				return nil, err
			}
		}
		p.Status = StatusCompleted
	} else {
		if _, err := e.Graph.DiscardFlow(ctx, p.ChangeID, p.Flow, by); err != nil {
			return nil, err
		}
		p.Status = StatusSuperseded
	}
	p.Pending = nil
	e.resumeWaiting(ctx, p, !adopt)
	e.journal(ctx, p, domain.ExecutionRecord{Kind: domain.ExecApproval, Step: len(p.Steps), Action: "flow", Actor: by,
		Data: map[string]any{"flow": p.Flow, "decision": decision, "comment": comment, "relaunchOf": p.RelaunchOf, "fromStep": p.FromStep}})
	return p, e.save(ctx, p, eventOf(p))
}

// supersede marks the run replaced by an adopted flow.
func (e *Engine) supersede(ctx context.Context, id string) error {
	defer e.lock(id)()
	old, err := e.Store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if old.Status == StatusSuperseded {
		return nil
	}
	old.Pending, old.Status = nil, StatusSuperseded
	return e.save(ctx, old, eventOf(old))
}

// FlowOf returns the flow branches of the change of a process (for display).
func (e *Engine) FlowsOf(ctx context.Context, id string) ([]domain.Flow, error) {
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	bb, err := e.Graph.Blackboard(ctx, p.ChangeID)
	if err != nil {
		return nil, err
	}
	return slices.Clone(bb.Change.Flows()), nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
