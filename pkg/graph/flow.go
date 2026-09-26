package graph

import (
	"context"
	"fmt"
	"maps"
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
	Seeds     []domain.ItemID
	FromStep  int
	Execution string
	Process   string
	Reason    string
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

func flowChange(ctx context.Context, tx Tx, id domain.ChangeID) (domain.ChangeSet, error) {
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
			FromStep: in.FromStep, Execution: in.Execution, Process: in.Process, Reason: in.Reason, Stale: stale}); err != nil {
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
			ev.Merged, ev.Items, ev.Nodes = g.mergeFlowBranch(ctx, tx, c, cur), cur.Materialized, cur.Nodes
			if !ev.Merged {
				ev.Items, ev.Nodes = nil, nil
			}
		case domain.FlowDiscardOp:
			for _, name := range cur.Branches {
				if b, err := tx.Branch(ctx, name); err == nil && b.Status == domain.BranchOpen {
					b.Status = domain.BranchAbandoned
					if err := tx.PutBranch(ctx, b); err != nil {
						return err
					}
				}
			}
		}
		if ev.Merged {
			if err := g.mergeFlowNow(ctx, tx, c, cur); err != nil {
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
func openFlows(c domain.ChangeSet) []domain.Flow {
	return slices.DeleteFunc(c.Flows(), func(f domain.Flow) bool { return f.Status != domain.FlowOpen })
}

// mergedSet lists the proposals already applied on the change branch by adopted, merged flows.
func mergedSet(c domain.ChangeSet) map[domain.ItemID]bool {
	out := map[domain.ItemID]bool{}
	for _, f := range c.Flows() {
		if len(f.Merged) > 0 && c.FlowStatusOf(f.ID) == domain.FlowAdopted {
			for _, id := range f.Merged {
				out[id] = true
			}
		}
	}
	return out
}

// flowProposals are the proposals of a flow view that still have to be applied (in effect, not yet merged).
func flowProposals(c domain.ChangeSet, flow string) []domain.ItemID {
	view := c.View(flow)
	var ids []domain.ItemID
	for _, it := range view.Items {
		if it.Kind == domain.KindProposal && view.InEffect(it.ID) && !c.MergedOnBranch(it.ID) {
			ids = append(ids, it.ID)
		}
	}
	return ids
}

// mergeFlowBranch reports whether the domain branch of a flow can be merged into the change
// branch when the flow is adopted: the change has a branch of its own, the flow was materialized
// on a branch that reflects its current proposals, and the merge has no conflict. Nothing is
// written.
func (g *Graph) mergeFlowBranch(ctx context.Context, tx Tx, c domain.ChangeSet, f domain.Flow) bool {
	if f.Branch == "" || len(f.Materialized) == 0 {
		return false
	}
	own, ok, err := ownBranch(ctx, tx, c)
	if err != nil || !ok {
		return false
	}
	want := flowProposals(c, f.ID)
	if len(want) != len(f.Materialized) || !slices.Equal(slices.Sorted(slices.Values(want)), slices.Sorted(slices.Values(f.Materialized))) {
		return false
	}
	plan, err := planMerge(ctx, tx, f.Branch, own.Name)
	return err == nil && len(plan.Conflicting()) == 0
}

// mergeFlowNow merges the domain branch of an adopted flow into the change branch.
func (g *Graph) mergeFlowNow(ctx context.Context, tx Tx, c domain.ChangeSet, f domain.Flow) error {
	own, _, err := ownBranch(ctx, tx, c)
	if err != nil {
		return err
	}
	_, err = g.mergeBranchTx(ctx, tx, MergeRequest{From: f.Branch, Into: own.Name, Title: "merge flow " + f.ID, Namespace: c.Namespace})
	return err
}

// MaterializeFlow applies the proposals of a flow branch on a graph branch of their own, forked
// from the branch the change acts on, so that the resulting graph can be looked at (PlanMerge of
// that branch against the change branch) before the flow is adopted. Adopting merges the branch
// into the change branch when the change has a branch of its own; discarding abandons it. Calling
// it again after the flow changed opens a new branch. Nothing is written on failure.
func (g *Graph) MaterializeFlow(ctx context.Context, id domain.ChangeID, flow string) (f domain.Flow, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := flowChange(ctx, tx, id)
		if err != nil {
			return err
		}
		cur, ok := c.Flow(flow)
		if !ok || cur.Status != domain.FlowOpen {
			return fmt.Errorf("flow %s is not open: %w", flow, ErrConflict)
		}
		want := flowProposals(c, flow)
		if len(want) == 0 || (cur.Branch != "" && slices.Equal(slices.Sorted(slices.Values(want)), slices.Sorted(slices.Values(cur.Materialized)))) {
			f = cur
			return nil
		}
		base, err := tx.Baseline(ctx, c.BaselineID)
		if err != nil {
			return err
		}
		target, parent, into := maps.Clone(base.Nodes), base.ID, domain.BranchOf(c.Branch)
		if own, isOwn, err := ownBranch(ctx, tx, c); err != nil {
			return err
		} else if isOwn {
			head, err := branchHead(ctx, tx, own.Name)
			if err != nil {
				return err
			}
			target, parent, into = maps.Clone(head.Nodes), head.ID, own.Name
		}
		name := "flow-" + shortID(flow)
		if n := len(cur.Branches); n > 0 {
			name = fmt.Sprintf("%s-%d", name, n+1)
		}
		view := c.View(flow)
		view.Branch = name
		w, err := g.walk(ctx, tx, view, false)
		if err != nil {
			return err
		}
		a := &applier{g: g, tx: tx, ctx: ctx, change: view, walk: w, branch: name, target: target, bumped: map[domain.NodeID]*bump{},
			created: map[domain.ItemID]domain.NodeRef{}, removed: map[domain.LinkID]bool{}, merged: mergedSet(c), mnodes: c.MergedNodes()}
		if err := a.run(); err != nil {
			return err
		}
		result := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: "flow " + shortID(flow), Branch: name, ParentID: parent, ChangeID: c.ID, Nodes: a.target, CreatedAt: g.now()}
		if err := tx.PutBaseline(ctx, result); err != nil {
			return err
		}
		if err := tx.PutBranch(ctx, domain.Branch{Name: name, Parent: into, ForkBaseline: parent, Head: result.ID,
			Origin: "flow:" + string(c.ID) + ":" + flow, Status: domain.BranchOpen, CreatedAt: g.now()}); err != nil {
			return err
		}
		nodes := map[domain.ItemID]domain.NodeID{}
		for item, ref := range a.created {
			nodes[item] = ref.ID
		}
		if err := g.flowEvent(ctx, tx, id, domain.FlowEvent{Op: domain.FlowMaterializeOp, Flow: flow, Branch: name, Items: want, Nodes: nodes}); err != nil {
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

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
