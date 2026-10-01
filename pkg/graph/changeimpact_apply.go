package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// cpost is a change impact of the change being applied, with the version it
// produced (the latest of its node on the change branch).
type cpost struct {
	cn   domain.ChangeImpact
	pre  *domain.Node
	post domain.Node
}

// prepareChangeImpacts puts the versions produced by the accepted change impacts
// into the target graph. Every change impact must have been reviewed; a rejected
// one is left out, an accepted one must have been realized.
func (a *applier) prepareChangeImpacts() error {
	for _, cn := range a.change.Nodes {
		if len(cn.Items) > 0 {
			continue // derived from items: their proposals are applied above
		}
		if cn.Flow != "" || cn.Superseded {
			continue // a candidate of a flow that is not adopted, or replaced by one that is
		}
		switch cn.Review {
		case domain.ReviewRejected:
			continue
		case domain.ReviewProposed:
			return fmt.Errorf("change impact %s (%s) awaits its review: %w", cn.Key, cn.ID, ErrConflict)
		}
		if cn.Post == nil {
			if cn.Intent == domain.IntentModified {
				continue // an impact confirmed, with no new version to write
			}
			return invalidf("change impact %s (%s) is accepted but its node is not written: write it or reject it", cn.Key, cn.ID)
		}
		post, err := a.tx.LatestOn(a.ctx, cn.Post.ID, a.branch)
		if err != nil {
			return err
		}
		cp := cpost{cn: cn, post: post}
		if cn.Pre != nil {
			pre, err := a.tx.Node(a.ctx, *cn.Pre)
			if err != nil {
				return err
			}
			cp.pre = &pre
		}
		a.cposts = append(a.cposts, cp)
		if post.Deleted {
			delete(a.target, post.ID)
		} else {
			a.target[post.ID] = post.Version
		}
	}
	return nil
}

// checkChangeImpacts validates the versions produced by the change impacts once the
// target graph is known: property validators, the states the nodes are left in,
// and the transitions they went through (permission, requirements, guards, then
// actions), like checkProps and checkMoves do for proposals.
func (a *applier) checkChangeImpacts() error {
	type moved struct {
		node     domain.Node
		t        domain.Transition
		children []domain.Node
	}
	var moves []moved
	var editable []string
	for _, cp := range a.cposts {
		n := cp.post
		if n.Deleted {
			continue
		}
		if err := a.g.validateProps(a.ctx, a.ix, n, n.Properties); err != nil {
			return err
		}
		lc := a.ix.lifecycleOf(n.Type)
		if lc == nil || n.State == "" {
			continue
		}
		if lc.Editable(n.State) && !lc.RestInEditable {
			editable = append(editable, fmt.Sprintf("%s (%s) in %s", n.Key, n.Type, n.State))
		}
		prev, from := lc.Initial, 0
		if cp.pre != nil {
			prev, from = cp.pre.State, int(cp.pre.Version)
			if prev == "" {
				prev = lc.Initial
			}
		}
		vs, err := a.tx.Versions(a.ctx, n.ID)
		if err != nil {
			return err
		}
		var last *domain.Transition
		for _, v := range vs {
			if !v.On(a.branch) || int(v.Version) <= from || v.State == prev {
				continue
			}
			t, ok := lc.Move(prev, v.State)
			if !ok {
				return invalidf("%s (%s) cannot go from %s to %s", n.Key, n.Type, prev, v.State)
			}
			if a.g.Authorizer != nil {
				at := v
				at.State = prev
				m := pendingMove{node: at, t: t}
				switch {
				case a.collect != nil:
					*a.collect = append(*a.collect, m)
				case a.authorized != nil && !a.authorized[m.key()]:
					return fmt.Errorf("%s moved to %s while the change was applied: apply it again: %w", n.Key, v.State, ErrConflict)
				case a.authorized == nil:
					if err := a.g.Authorizer(a.ctx, at, t); err != nil {
						return err
					}
				}
			}
			last, prev = &t, v.State
		}
		if last != nil {
			moves = append(moves, moved{node: n, t: *last})
		}
	}
	if len(editable) > 0 && !a.activityGated() {
		return invalidf("the change leaves nodes in an editable state, move them out of it before applying: %s", joinSorted(editable))
	}
	// ActivityGoalsMet may itself need to read the graph (e.g. the methodology namespace), which this transaction
	// would block (the stores are not reentrant): authorizeMoves evaluates it in its own rolled-back pass before
	// this transaction opens, and passes the result in as a.activityMet (same reason and pattern as authorized,
	// just above). a.collect != nil means this call IS that earlier pass: nothing to check yet, just leave the
	// blackboard for the caller (activityGated()/activityBlackboard() below) to build and evaluate outside it.
	if a.activityGated() && a.collect == nil {
		if a.activityMet == nil {
			return fmt.Errorf("change %s: activity %s was not evaluated before this transaction: %w", a.change.ID, a.change.ActivityRef, ErrInvalid)
		}
		if !*a.activityMet {
			return invalidf("the change does not satisfy the goal of its activity %s", a.change.ActivityRef)
		}
	}
	for i, m := range moves {
		children, err := a.checkTransition(m.node, m.t)
		if err != nil {
			return err
		}
		moves[i].children = children
	}
	for _, m := range moves {
		if err := a.runActions(m.node, m.t, m.children); err != nil {
			return err
		}
	}
	return nil
}

// activityGated reports whether landing this change is gated by its Activity's own goal condition
// (Graph.ActivityGoalsMet) instead of the node-type lifecycle's Editable floor.
func (a *applier) activityGated() bool {
	return a.change.ActivityRef != "" && a.g.ActivityGoalsMet != nil
}

// activityBlackboard builds the blackboard ActivityGoalsMet evaluates the activity's own goal condition
// against: the change with its impacts (for the changeImpacts condition variable), hydrated with the pre/post
// node content this Apply already resolved (ADR 0024) - not a fresh read, since this change's own pending
// writes are not yet visible outside this transaction.
func (a *applier) activityBlackboard() domain.Blackboard {
	bb := domain.Blackboard{Change: a.change, Nodes: map[domain.NodeRef]domain.NodeView{}}
	add := func(n *domain.Node) {
		if n == nil {
			return
		}
		v := domain.NodeView{Node: *n}
		if lc := a.ix.lifecycleOf(n.Type); lc != nil && n.State != "" {
			v.Frozen = !lc.Editable(n.State)
		}
		bb.Nodes[n.Ref()] = v
	}
	for _, cp := range a.cposts {
		add(cp.pre)
		post := cp.post
		add(&post)
	}
	return bb
}

func joinSorted(s []string) string {
	s = slices.Clone(s)
	slices.Sort(s)
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// land records, once the branch of a change is merged into its parent (the baseline result), the
// version each change impact landed as: the version it wrote, joined to the parent, or the merge
// version of mergeChange; and moves the planned change impacts of the other open changes on the
// same nodes to the new head, to be re-checked.
func (g *Graph) land(ctx context.Context, tx Tx, c domain.Change, own domain.Branch, mergeChange domain.ChangeID, result domain.BaselineID) error {
	for _, cn := range c.Nodes {
		if cn.Post == nil || cn.Review != domain.ReviewAccepted || cn.Flow != "" || cn.Superseded {
			continue
		}
		head, err := tx.LatestOn(ctx, cn.Post.ID, domain.BranchOf(own.Parent))
		if err != nil {
			return err
		}
		if head.ChangeID != mergeChange && head.Ref() != *cn.Post {
			continue // left as is on the target: nothing of this change landed
		}
		ref := head.Ref()
		comment := cn.Rationale
		if len(cn.Reviews) > 0 {
			comment = cn.Reviews[len(cn.Reviews)-1].Comment
		}
		if err := tx.SetNodeOrigin(ctx, ref, c.ID, cn.ID, comment); err != nil {
			return err
		}
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactLanded, Landed: &ref, Baseline: result}); err != nil {
			return err
		}
		others, err := tx.NodeChangeImpacts(ctx, ref.ID)
		if err != nil {
			return err
		}
		for _, oid := range others {
			if oid == c.ID {
				continue
			}
			oc, err := tx.Change(ctx, oid)
			if err != nil {
				return err
			}
			if oc.Status == domain.ChangeApplied || oc.Status == domain.ChangeAbandoned || domain.BranchOf(oc.Branch) == domain.BranchOf(c.Branch) {
				continue
			}
			for _, o := range oc.Nodes {
				if o.Post != nil || o.Pre == nil || o.Pre.ID != ref.ID || o.Pre.Version >= ref.Version {
					continue
				}
				// moved to the new head, to be re-checked
				if err := g.emit(ctx, tx, domain.ImpactEvent{Change: oid, Impact: o.ID, Op: domain.ImpactRebased, Pre: &ref}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
