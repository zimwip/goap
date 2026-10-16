package graph

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
)

// The merge of a change is held by its guardian (ADR 0102), the same as its transitions (ADR 0058): the lifecycle of
// the change must be in a final state, then the methodology has its say (ChangeLifecycles.Merge: the goal of the
// Activity the change is scoped to), else the landable states of the domain decide (ADR 0078); then the outer
// guardians of its chain, innermost first (ADR 0106). Before them, the changes it depends on must have reached the
// branch it merges into (ADR 0108). Asked outside any transaction: the guardian may read the graph.

// checkMerge asks the guardian of a change whether it may merge.
func (g *Graph) checkMerge(ctx context.Context, id domain.ChangeID) error {
	var c domain.Change
	var bb domain.Blackboard
	var landed []domain.NodeView
	var deps []domain.Dependency
	var logs map[string][]domain.LogEntry
	err := g.repo.InTx(ctx, func(tx Tx) (err error) {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		if logs, err = g.logEntries(ctx, tx, id); err != nil {
			return err
		}
		if deps, err = dependenciesOf(ctx, tx, c); err != nil {
			return err
		}
		bb, landed, err = g.mergeBlackboard(ctx, tx, c)
		bb = bb.WithFacet(domain.FacetDependencies, deps)
		return err
	})
	if err != nil {
		return err
	}
	// the facets folded from the log (the history of the change, ADR 0109), outside the transaction
	if bb, err = g.withLogFacets(ctx, bb, logs); err != nil {
		return err
	}
	// first the changes it depends on (ADR 0108): their work has reached the branch it merges into
	if err := checkDependencies(c, deps); err != nil {
		return err
	}
	if c.Lifecycle != "" {
		lc, err := g.changeLifecycle(ctx, c)
		if err != nil {
			return err
		}
		if s, ok := lc.State(c.State); !ok || !s.Final {
			return fmt.Errorf("change %s is in state %s of lifecycle %s: it is merged once it is in a final state: %w", c.ID, c.State, c.Lifecycle, ErrConflict)
		}
	}
	if err := g.ownGuardianMerge(ctx, c, bb, landed); err != nil {
		return err
	}
	// then the outer guardians of its chain (ADR 0106), innermost first
	return g.checkGuardians(ctx, bb, "")
}

// ownGuardianMerge is the say of the change's own methodology on its merge: its Activity goal, else the landable states.
func (g *Graph) ownGuardianMerge(ctx context.Context, c domain.Change, bb domain.Blackboard, landed []domain.NodeView) error {
	if g.Lifecycles != nil {
		decided, ok, err := g.Lifecycles.Merge(ctx, c, bb)
		switch {
		case err != nil:
			return err
		case decided && !ok:
			return invalidf("the change does not satisfy the goal of its methodology")
		case decided:
			return nil
		}
	}
	var stuck []string
	for _, n := range landed {
		if n.NotLandable {
			stuck = append(stuck, fmt.Sprintf("%s (%s) in %s", n.Key, n.Type, n.State))
		}
	}
	if len(stuck) > 0 {
		return invalidf("the change leaves nodes in a state that cannot land, move them to a landable state first: %s", joinSorted(stuck))
	}
	return nil
}

// mergeBlackboard is what the guardian judges: the change with, for each impact, its pre version and what it lands (the
// version on the change's branch), those landed nodes returned apart.
func (g *Graph) mergeBlackboard(ctx context.Context, tx Tx, c domain.Change) (domain.Blackboard, []domain.NodeView, error) {
	bb := domain.Blackboard{Change: c, Nodes: map[domain.NodeRef]domain.NodeView{}}
	ix, err := g.typesAt(ctx, tx, c.BaselineID)
	if err != nil {
		return bb, nil, err
	}
	impacts := mainImpactsOf(c)
	r, err := g.newDraftReader(ctx, tx, c, "", impacts)
	if err != nil {
		return bb, nil, err
	}
	var head map[domain.NodeID]domain.Version
	if own, ok, err := ownBranch(ctx, tx, c); err != nil {
		return bb, nil, err
	} else if ok {
		h, err := branchHead(ctx, tx, c.Namespace, own.Name)
		if err != nil {
			return bb, nil, err
		}
		head = h.Nodes
	}
	view := func(ref domain.NodeRef) (domain.NodeView, error) {
		v, err := viewIn(ctx, tx, r, ref)
		if lc := ix.lifecycleOf(v.Type); err == nil && lc != nil && v.State != "" {
			v.NotLandable = !lc.Landable(v.State)
		}
		return v, err
	}
	var landed []domain.NodeView
	for _, cn := range impacts {
		if cn.Pre != nil {
			v, err := view(*cn.Pre)
			if err != nil {
				return bb, nil, err
			}
			bb.Nodes[*cn.Pre] = v
		}
		at, ok := head[nodeOf(cn)]
		if !ok || cn.Landed == nil {
			continue
		}
		to := &domain.NodeRef{ID: nodeOf(cn), Version: at}
		v, err := view(*to)
		if err != nil {
			return bb, nil, err
		}
		bb.Nodes[*to] = v
		bb.Nodes[*cn.Post] = v // the impact's post reads what it lands
		landed = append(landed, v)
	}
	return bb, landed, nil
}

// mainImpactsOf are the impacts of the main flow of a change: the ones its commit and its merge look at.
func mainImpactsOf(c domain.Change) []domain.ChangeImpact {
	var out []domain.ChangeImpact
	for _, cn := range c.Nodes {
		if cn.Flow == "" && !cn.Superseded {
			out = append(out, cn)
		}
	}
	return out
}
