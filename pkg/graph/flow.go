package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/changeapi"
	"github.com/zimwip/goap/pkg/domain"
)

// OpenFlowRequest is changeapi.OpenFlowRequest (ADR 0098: the contract of the change, shared with the engine).
type OpenFlowRequest = changeapi.OpenFlowRequest

// openFlowEvent appends a flow event to the log of a change.
func (g *Graph) flowEvent(ctx context.Context, tx Tx, id domain.ChangeID, e domain.FlowEvent) error {
	return putItem(ctx, tx, id, domain.ChangeItem{ID: domain.ItemID(g.newID()), Kind: domain.KindFlow, Type: "flow." + e.Op,
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
			Stale: stale, StaleRuns: in.StaleRuns, Origin: in.Origin}); err != nil {
			return err
		}
		for _, it := range in.Items {
			if it.Kind == domain.KindFlow || it.Kind == domain.KindTransition || it.Kind == domain.KindDecisionPoint {
				return fmt.Errorf("a %s item is recorded by its own operation, not at the opening of a flow: %w", it.Kind, ErrInvalid)
			}
			it.ID, it.Flow, it.CreatedAt = domain.ItemID(g.newID()), flow, g.now()
			if it.Status == "" {
				it.Status = domain.ItemAccepted
			}
			if err := it.Validate(); err != nil {
				return fmt.Errorf("item %s: %v: %w", it.ID, err, ErrInvalid)
			}
			if err := putItem(ctx, tx, id, it); err != nil {
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
		if err := g.decideFlowTx(ctx, tx, c, flow, op, by); err != nil {
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

// decideFlowTx adopts or discards an open flow of c.
func (g *Graph) decideFlowTx(ctx context.Context, tx Tx, c domain.Change, flow, op, by string) error {
	cur, ok := c.Flow(flow)
	if !ok {
		return fmt.Errorf("flow %s: %w", flow, ErrNotFound)
	}
	if cur.Status != domain.FlowOpen {
		return fmt.Errorf("flow %s is %s: %w", flow, cur.Status, ErrConflict)
	}
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
	return g.flowEvent(ctx, tx, c.ID, domain.FlowEvent{Op: op, Flow: flow, By: by})
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
