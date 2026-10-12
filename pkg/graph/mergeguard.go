package graph

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
)

// Merge guards (ADR 0102): what may hold the merge of a change is not the graph's to say. The graph keeps the mechanism
// (commit, rebase, fast-forward); the rules that move with the use cases (the lifecycle of the change, the goals of its
// activity, the landable states of the domain) are guards, asked in order before every merge, outside any
// transaction (a guard may read the graph). The first guard that decides, decides: an error refuses the merge, a
// guard that allows it ends the chain; a guard that has nothing to say passes. A merge no guard refuses goes on.

// MergeCheck is what a merge guard judges: the change, the lifecycle it follows (nil: none), the blackboard of what it
// lands (the change with its impacts, their pre version and what each lands: the version committed on the change's
// branch, the draft a sub-change hands its parent) and those landed nodes.
type MergeCheck struct {
	Change     domain.Change
	Lifecycle  *domain.Lifecycle
	Blackboard domain.Blackboard
	Landed     []domain.NodeView
}

// MergeGuard is one rule a merge must pass. decided true allows the merge and ends the chain; an error refuses it.
type MergeGuard interface {
	CheckMerge(ctx context.Context, m MergeCheck) (decided bool, err error)
}

// MergeGuardFunc adapts a function to a MergeGuard.
type MergeGuardFunc func(ctx context.Context, m MergeCheck) (bool, error)

func (f MergeGuardFunc) CheckMerge(ctx context.Context, m MergeCheck) (bool, error) { return f(ctx, m) }

// DefaultMergeGuards are the guards of a graph given none (Graph.MergeGuards unset): the final state of the change's
// lifecycle, then the landable states of what it lands.
func DefaultMergeGuards() []MergeGuard { return []MergeGuard{FinalStateGuard{}, LandableStatesGuard{}} }

// FinalStateGuard refuses the merge of a change whose lifecycle has not reached a final state (ADR 0058 §4).
type FinalStateGuard struct{}

func (FinalStateGuard) CheckMerge(_ context.Context, m MergeCheck) (bool, error) {
	if m.Lifecycle == nil {
		return false, nil
	}
	if s, ok := m.Lifecycle.State(m.Change.State); !ok || !s.Final {
		return false, fmt.Errorf("change %s is in state %s of lifecycle %s: it is merged once it is in a final state: %w", m.Change.ID, m.Change.State, m.Change.Lifecycle, ErrConflict)
	}
	return false, nil
}

// LandableStatesGuard refuses the merge of a change that lands a node in a state its lifecycle cannot land (ADR 0078).
type LandableStatesGuard struct{}

func (LandableStatesGuard) CheckMerge(_ context.Context, m MergeCheck) (bool, error) {
	var stuck []string
	for _, n := range m.Landed {
		if n.NotLandable {
			stuck = append(stuck, fmt.Sprintf("%s (%s) in %s", n.Key, n.Type, n.State))
		}
	}
	if len(stuck) > 0 {
		return false, invalidf("the change leaves nodes in a state that cannot land, move them to a landable state first: %s", joinSorted(stuck))
	}
	return false, nil
}

// LandingGate adapts a gate deciding on the blackboard of a change (ADR 0066: the goals of the Activity it is scoped to,
// internal/registrysvc): undecided, it passes; decided, it allows the merge (and so replaces the guards after it, the
// landable states for one) or refuses it.
func LandingGate(gate func(ctx context.Context, c domain.Change, bb domain.Blackboard) (decided, ok bool, err error)) MergeGuard {
	return MergeGuardFunc(func(ctx context.Context, m MergeCheck) (bool, error) {
		decided, ok, err := gate(ctx, m.Change, m.Blackboard)
		switch {
		case err != nil:
			return false, err
		case decided && !ok:
			return false, invalidf("the change does not satisfy the goal of its landing gate")
		}
		return decided, nil
	})
}

func (g *Graph) mergeGuards() []MergeGuard {
	if g.MergeGuards != nil {
		return g.MergeGuards
	}
	return DefaultMergeGuards()
}

// checkMerge asks the merge guards about a change, outside any transaction: it builds what they judge in a read pass.
func (g *Graph) checkMerge(ctx context.Context, id domain.ChangeID) error {
	var m MergeCheck
	err := g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		m, err = g.mergeCheck(ctx, tx, c)
		return err
	})
	if err != nil {
		return err
	}
	if m.Change.Lifecycle != "" {
		lc, err := g.changeLifecycle(ctx, m.Change)
		if err != nil {
			return err
		}
		m.Lifecycle = lc
	}
	for _, guard := range g.mergeGuards() {
		if decided, err := guard.CheckMerge(ctx, m); err != nil || decided {
			return err
		}
	}
	return nil
}

// mergeCheck builds what the guards judge: the blackboard of the change with, for each impact, its pre version and what
// it lands: the version on the change's branch (a root change), its draft (a sub-change, which hands it to its parent).
func (g *Graph) mergeCheck(ctx context.Context, tx Tx, c domain.Change) (MergeCheck, error) {
	m := MergeCheck{Change: c, Blackboard: domain.Blackboard{Change: c, Nodes: map[domain.NodeRef]domain.NodeView{}}}
	ix, err := g.typesAt(ctx, tx, c.BaselineID)
	if err != nil {
		return m, err
	}
	impacts := mainImpactsOf(c)
	r, err := g.newDraftReader(ctx, tx, c, "", impacts)
	if err != nil {
		return m, err
	}
	var head map[domain.NodeID]domain.Version
	if c.ParentID == "" {
		if own, ok, err := ownBranch(ctx, tx, c); err != nil {
			return m, err
		} else if ok {
			h, err := branchHead(ctx, tx, c.Namespace, own.Name)
			if err != nil {
				return m, err
			}
			head = h.Nodes
		}
	}
	view := func(ref domain.NodeRef) (domain.NodeView, error) {
		v, err := viewIn(ctx, tx, r, ref)
		if err != nil {
			return v, err
		}
		if lc := ix.lifecycleOf(v.Type); lc != nil && v.State != "" {
			v.NotLandable = !lc.Landable(v.State)
		}
		return v, nil
	}
	for _, cn := range impacts {
		if cn.Pre != nil {
			v, err := view(*cn.Pre)
			if err != nil {
				return m, err
			}
			m.Blackboard.Nodes[*cn.Pre] = v
		}
		var landed *domain.NodeRef
		switch {
		case c.ParentID != "" && cn.Post != nil && cn.Post.IsDraft() && cn.Review == domain.ReviewAccepted:
			landed = cn.Post
		case c.ParentID == "":
			if v, ok := head[nodeOf(cn)]; ok && cn.Landed != nil {
				landed = &domain.NodeRef{ID: nodeOf(cn), Version: v}
			}
		}
		if landed == nil {
			continue
		}
		v, err := view(*landed)
		if err != nil {
			return m, err
		}
		m.Blackboard.Nodes[*landed] = v
		if cn.Post != nil {
			m.Blackboard.Nodes[*cn.Post] = v // the impact's post reads what it lands
		}
		m.Landed = append(m.Landed, v)
	}
	return m, nil
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
