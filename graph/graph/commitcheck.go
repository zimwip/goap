package graph

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
)

// The guardian of a change judges its commits (ADR 0146), as it judges its transitions (ADR 0058): each commit puts the
// work of the change in the graph tree, so it is where the methodology has its say (ChangeLifecycles.Commit: the goal of
// the Activity the change is scoped to), else the committable states of the domain decide (ADR 0078); then the outer
// guardians of its chain, innermost first (ADR 0106). Asked in the transaction of the commit, once its versions are
// written (ADR 0140): a refusal undoes the commit; the guardian may read the graph, with the context it is given. The
// merge asks none of them (mergecheck.go).

// checkCommit asks the guardian of a change whether what it committed may stand.
func (g *Graph) checkCommit(ctx context.Context, id domain.ChangeID) error {
	var c domain.Change
	var bb domain.Blackboard
	var committed []domain.NodeView
	var logs map[string][]domain.LogEntry
	whole := true
	err := g.repo.InTx(ctx, func(tx Tx) (err error) {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		// with no guardian to ask, the committable states only: the nodes committed, not their links, history or
		// dependencies
		if whole = g.judgesCommit(c); !whole {
			bb, committed, err = g.commitBlackboard(ctx, tx, c, false)
			return err
		}
		if logs, err = g.logEntries(ctx, tx, id); err != nil {
			return err
		}
		deps, err := dependenciesOf(ctx, tx, c)
		if err != nil {
			return err
		}
		bb, committed, err = g.commitBlackboard(ctx, tx, c, true)
		bb = bb.WithFacet(domain.FacetDependencies, deps)
		return err
	})
	if err != nil {
		return err
	}
	if !whole {
		return g.ownGuardian(ctx, c, bb, committed)
	}
	// the facets folded from the log (the history of the change, ADR 0109)
	if bb, err = g.withLogFacets(ctx, bb, logs); err != nil {
		return err
	}
	if err := g.ownGuardian(ctx, c, bb, committed); err != nil {
		return err
	}
	// then the outer guardians of its chain (ADR 0106), innermost first
	return g.checkGuardians(ctx, bb, "")
}

// CommitJudge is a ChangeLifecycles that tells from the change alone whether its Commit may decide (a change scoped to
// an Activity): one that does not leaves the commit to the committable states, and the commit builds no blackboard
// for it.
type CommitJudge interface {
	JudgesCommit(c domain.Change) bool
}

// judgesCommit is whether a guardian may judge a commit of the change: its own methodology's, or an outer one of its
// chain.
func (g *Graph) judgesCommit(c domain.Change) bool {
	if g.Lifecycles == nil {
		return false
	}
	if len(c.OuterGuardians()) > 0 {
		return true
	}
	j, ok := g.Lifecycles.(CommitJudge)
	return !ok || j.JudgesCommit(c)
}

// ownGuardian is the say of the change's own methodology on its commits: its Activity goal, else the committable states.
func (g *Graph) ownGuardian(ctx context.Context, c domain.Change, bb domain.Blackboard, committed []domain.NodeView) error {
	if g.Lifecycles != nil {
		decided, ok, err := g.Lifecycles.Commit(ctx, c, bb)
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
	for _, n := range committed {
		if n.NotCommittable {
			stuck = append(stuck, fmt.Sprintf("%s (%s) in %s", n.Key, n.Type, n.State))
		}
	}
	if len(stuck) > 0 {
		return invalidf("the change leaves nodes in a state that cannot commit, move them to a committable state first: %s", joinSorted(stuck))
	}
	return nil
}

// commitBlackboard is what the guardian judges: the change with, for each impact, its pre version and what it commits (the
// version on the change's branch), those committed nodes returned apart; not whole, the versions without their links
// (what the committable states read).
func (g *Graph) commitBlackboard(ctx context.Context, tx Tx, c domain.Change, whole bool) (domain.Blackboard, []domain.NodeView, error) {
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
		ids := make([]domain.NodeID, 0, len(impacts))
		for _, cn := range impacts {
			if id := nodeOf(cn); id != "" { // a planned creation has no node yet (ADR 0121)
				ids = append(ids, id)
			}
		}
		if head, err = branchVersions(ctx, tx, own, ids); err != nil { // the nodes of the impacts only (ADR 0137)
			return bb, nil, err
		}
	}
	// the versions the blackboard shows, read in one batch
	var refs []domain.NodeRef
	for _, cn := range impacts {
		if cn.Pre != nil {
			refs = append(refs, *cn.Pre)
		}
		if at, ok := head[nodeOf(cn)]; ok && cn.Committed != nil {
			refs = append(refs, domain.NodeRef{ID: nodeOf(cn), Version: at})
		}
	}
	if !whole {
		if err := prefetchVersions(ctx, tx, refs); err != nil {
			return bb, nil, err
		}
	} else if err := r.prefetch(ctx, tx, refs); err != nil {
		return bb, nil, err
	}
	view := func(ref domain.NodeRef) (domain.NodeView, error) {
		var v domain.NodeView
		var err error
		if whole {
			v, err = viewIn(ctx, tx, r, ref)
		} else {
			v.Node, err = r.node(ctx, tx, ref)
		}
		if lc := ix.lifecycleOf(v.Type); err == nil && lc != nil && v.State != "" {
			v.NotCommittable = !lc.Committable(v.State)
		}
		return v, err
	}
	var committed []domain.NodeView
	for _, cn := range impacts {
		if cn.Pre != nil {
			v, err := view(*cn.Pre)
			if err != nil {
				return bb, nil, err
			}
			bb.Nodes[*cn.Pre] = v
		}
		at, ok := head[nodeOf(cn)]
		if !ok || cn.Committed == nil {
			continue
		}
		to := &domain.NodeRef{ID: nodeOf(cn), Version: at}
		v, err := view(*to)
		if err != nil {
			return bb, nil, err
		}
		bb.Nodes[*to] = v
		bb.Nodes[*cn.Post] = v // the impact's post reads what it commits
		committed = append(committed, v)
	}
	return bb, committed, nil
}
