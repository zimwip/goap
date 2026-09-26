package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// OpenFlowRequest relaunches a step of the action flow on a new flow branch.
type OpenFlowRequest struct {
	// Parent is the flow the relaunched step ran on ("" = the main flow).
	Parent string
	// ForkAfter is the last item of the parent's view before the step ("" = none).
	ForkAfter domain.ItemID
	// Seeds are the items produced by the relaunched step and by what followed
	// it; the items derived from them are invalidated too.
	Seeds []domain.ItemID
	// StaleExecutions are the action runs of the relaunched step and of what followed it: the change
	// nodes, versions and reviews they produced are stale until the branch is adopted (ADR 0025).
	StaleExecutions []string
	FromStep        int
	Execution       string
	Process         string
	Reason          string
	// Guidance is a comment for the agent: recorded on the branch, it is part of the
	// blackboard the relaunched steps read. By is who wrote it.
	Guidance string
	By       string
}

// openFlowEvent appends a flow event to the log of a change.
func (g *Graph) flowEvent(ctx context.Context, tx Tx, id domain.ChangeID, e domain.FlowEvent) error {
	return tx.PutItem(ctx, id, domain.ChangeItem{ID: domain.ItemID(g.newID()), Kind: domain.KindFlow, Type: "flow." + e.Op,
		Status: domain.ItemAccepted, ProducedBy: "graph.flow", FlowEvent: &e, CreatedAt: g.now()})
}

func flowChange(ctx context.Context, tx Tx, id domain.ChangeID) (domain.Change, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return c, err
	}
	if c.Status != domain.ChangeDraft && c.Status != domain.ChangeActive {
		return c, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	return c, nil
}

// OpenFlow opens a flow branch: the step restarts from ForkAfter, the items
// it invalidated (the closure of Seeds over the provenance of the parent's
// view) become stale until the branch is adopted. A change may have several
// open flows: parallel alternatives, decided one by one.
func (g *Graph) OpenFlow(ctx context.Context, id domain.ChangeID, in OpenFlowRequest) (f domain.Flow, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := flowChange(ctx, tx, id)
		if err != nil {
			return err
		}
		if in.Parent != "" && c.FlowStatusOf(in.Parent) == domain.FlowDiscarded {
			return fmt.Errorf("flow %s is discarded: %w", in.Parent, ErrInvalid)
		}
		base := c.View(in.Parent)
		inView := map[domain.ItemID]bool{}
		for _, it := range base.Items {
			inView[it.ID] = true
		}
		if in.ForkAfter != "" && !inView[in.ForkAfter] {
			return fmt.Errorf("fork point %s is not on the flow %q: %w", in.ForkAfter, in.Parent, ErrInvalid)
		}
		for _, s := range in.Seeds {
			if !inView[s] {
				return fmt.Errorf("item %s is not on the flow %q: %w", s, in.Parent, ErrInvalid)
			}
		}
		flow := g.newID()
		stale := domain.StaleClosure(base.Items, in.Seeds)
		if err := g.flowEvent(ctx, tx, id, domain.FlowEvent{Op: domain.FlowOpenOp, Flow: flow, Parent: in.Parent, ForkAfter: in.ForkAfter,
			FromStep: in.FromStep, Execution: in.Execution, Process: in.Process, Reason: in.Reason, Stale: stale, StaleExecutions: in.StaleExecutions}); err != nil {
			return err
		}
		if in.Guidance != "" {
			if err := tx.PutItem(ctx, id, domain.ChangeItem{ID: domain.ItemID(g.newID()), Kind: domain.KindArtifact, Type: "guidance", Status: domain.ItemAccepted,
				Flow: flow, ProducedBy: firstNonEmpty(in.By, "human"), Data: map[string]any{"text": in.Guidance, "step": in.FromStep}, CreatedAt: g.now()}); err != nil {
				return err
			}
		}
		if c.Status == domain.ChangeDraft {
			c.Status = domain.ChangeActive
			if err := tx.PutChange(ctx, c); err != nil {
				return err
			}
		}
		full, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		f, _ = full.Flow(flow)
		return nil
	})
	return
}

func (g *Graph) decideFlow(ctx context.Context, id domain.ChangeID, flow, op, by string) (f domain.Flow, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := flowChange(ctx, tx, id)
		if err != nil {
			return err
		}
		cur, ok := c.Flow(flow)
		if !ok {
			return fmt.Errorf("flow %s: %w", flow, ErrNotFound)
		}
		if cur.Status != domain.FlowOpen {
			return fmt.Errorf("flow %s is %s: %w", flow, cur.Status, ErrConflict)
		}
		ev := domain.FlowEvent{Op: op, Flow: flow, By: by}
		switch op {
		case domain.FlowAdoptOp:
			if cur.Parent != "" && c.FlowStatusOf(cur.Parent) != domain.FlowAdopted {
				return fmt.Errorf("flow %s: its parent %s is not adopted: %w", flow, cur.Parent, ErrConflict)
			}
			if len(cur.CompetesWith) > 0 {
				return fmt.Errorf("flow %s competes with the adopted flow %s (it replaces the same items): relaunch or discard it: %w", flow, cur.CompetesWith[0], ErrConflict)
			}
			if err := g.adoptNodes(ctx, tx, c, cur, by); err != nil {
				return err
			}
		case domain.FlowDiscardOp:
			if err := g.discardNodes(ctx, tx, c, cur, by); err != nil {
				return err
			}
		}
		if err := g.flowEvent(ctx, tx, id, ev); err != nil {
			return err
		}
		full, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		f, _ = full.Flow(flow)
		return nil
	})
	return
}

// AdoptFlow adopts an open flow branch: its items count, the items it
// invalidated become superseded.
func (g *Graph) AdoptFlow(ctx context.Context, id domain.ChangeID, flow, by string) (domain.Flow, error) {
	return g.decideFlow(ctx, id, flow, domain.FlowAdoptOp, by)
}

// DiscardFlow discards an open flow branch: its items are rejected, the items
// it marked stale count again.
func (g *Graph) DiscardFlow(ctx context.Context, id domain.ChangeID, flow, by string) (domain.Flow, error) {
	return g.decideFlow(ctx, id, flow, domain.FlowDiscardOp, by)
}

// Flows lists the flow branches of a change.
func (g *Graph) Flows(ctx context.Context, id domain.ChangeID) (fs []domain.Flow, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		fs = c.Flows()
		return nil
	})
	return
}

// openFlows are the flow branches still waiting for a decision.
func openFlows(c domain.Change) []domain.Flow {
	return slices.DeleteFunc(c.Flows(), func(f domain.Flow) bool { return f.Status != domain.FlowOpen })
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
