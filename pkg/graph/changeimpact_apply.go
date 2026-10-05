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
// one is left out, an accepted one must have been realized; the working version of an accepted one is frozen
// here (ADR 0077).
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
		if err := a.g.checkOrigins(a.ctx, a.tx, a.change.ID, a.mainImpacts(), cn); err != nil {
			return err
		}
		post, err := a.tx.LatestOn(a.ctx, cn.Post.ID, a.branch)
		if err != nil {
			return err
		}
		// landing freezes the accepted working version (ADR 0077), and the ones of the change it supersedes (a flow
		// version a parent flow's working version was derived from); the frozen-state checks follow (checkChangeImpacts)
		if post.CheckedOut {
			vs, err := a.tx.Versions(a.ctx, post.ID)
			if err != nil {
				return err
			}
			for _, v := range vs {
				if v.CheckedOut && (v.ChangeID == a.change.ID || v.Ref() == post.Ref()) {
					if err := a.tx.FreezeVersion(a.ctx, v.Ref()); err != nil {
						return err
					}
				}
			}
			post.CheckedOut = false
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

// mainImpacts are the impacts of the main flow that count at landing.
func (a *applier) mainImpacts() []domain.ChangeImpact {
	return slices.DeleteFunc(slices.Clone(a.change.Nodes), func(cn domain.ChangeImpact) bool { return cn.Flow != "" || cn.Superseded })
}

// checkChangeImpacts validates the versions produced by the change impacts once the target graph is known: property
// validators, the states the nodes are left in and the transitions they went through, then what they leave (the landing
// gate, else no node left in an editable state). A transition was authorized, guarded and acted when it was taken
// (ImpactNodeTransition, ADR 0076): the walk only checks that each state change on the branch is a move of the lifecycle.
func (a *applier) checkChangeImpacts() error {
	editable, err := a.walkChangeImpacts()
	if err != nil {
		return err
	}
	return a.settleChangeImpacts(editable)
}

// walkChangeImpacts validates the properties of each produced version and walks its transitions: it returns the nodes
// left in an editable state.
func (a *applier) walkChangeImpacts() (editable []string, err error) {
	for _, cp := range a.cposts {
		n := cp.post
		if n.Deleted {
			continue
		}
		if err := a.g.checkFrozen(a.ctx, a.tx, a.ix, n); err != nil {
			return nil, err
		}
		lc := a.ix.lifecycleOf(n.Type)
		if lc == nil || n.State == "" {
			continue
		}
		if lc.Editable(n.State) && !lc.RestInEditable {
			editable = append(editable, fmt.Sprintf("%s (%s) in %s", n.Key, n.Type, n.State))
		}
		if err := a.walkTransitions(cp, lc); err != nil {
			return nil, err
		}
	}
	return editable, nil
}

// walkTransitions replays the versions of a node written on the change branch: each state change must be a
// transition of the lifecycle. A version whose state was set in place (ADR 0077: transitioned events with a state
// patch, each checked when it was taken) is followed through those moves: they must chain from the state the version
// started in to the state it carries.
func (a *applier) walkTransitions(cp cpost, lc *domain.Lifecycle) error {
	n := cp.post
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
	moved := map[domain.ChangeID]map[domain.NodeRef][]stateMove{} // by the change that wrote the version: a sub-change merged in too
	for _, v := range vs {
		if !v.On(a.branch) || int(v.Version) <= from {
			continue
		}
		if _, ok := moved[v.ChangeID]; !ok {
			if moved[v.ChangeID], err = a.inPlaceMoves(v.ChangeID); err != nil {
				return err
			}
		}
		if ms := moved[v.ChangeID][v.Ref()]; len(ms) > 0 {
			at := prev
			for _, m := range ms {
				if m.from != at {
					return invalidf("%s (%s) was moved from %s while it was in %s", n.Key, n.Type, m.from, at)
				}
				at = m.to
			}
			if at != v.State && !(v.State == "" && at == lc.Initial) {
				return invalidf("%s (%s) is in %s, the moves recorded for it end in %s", n.Key, n.Type, v.State, at)
			}
			prev = v.State
			continue
		}
		if v.State == prev {
			continue
		}
		if _, ok := lc.Move(prev, v.State); !ok {
			return invalidf("%s (%s) cannot go from %s to %s", n.Key, n.Type, prev, v.State)
		}
		prev = v.State
	}
	return nil
}

// stateMove is a transition taken in place on a working version.
type stateMove struct{ from, to string }

// inPlaceMoves reads, from the impact log of a change, the transitions taken in place on its working versions, by
// version, in order (ADR 0077).
func (a *applier) inPlaceMoves(change domain.ChangeID) (map[domain.NodeRef][]stateMove, error) {
	events, err := impactEvents(a.ctx, a.tx, change)
	if err != nil {
		return nil, err
	}
	out := map[domain.NodeRef][]stateMove{}
	for _, e := range events {
		st, ok := e.Patch["state"].(map[string]any)
		if e.Op != domain.ImpactTransitioned || !ok || e.Post == nil {
			continue
		}
		from, _ := st["from"].(string)
		to, _ := st["to"].(string)
		out[*e.Post] = append(out[*e.Post], stateMove{from, to})
	}
	return out, nil
}

// settleChangeImpacts decides what the walk found: the landing gate, else the editable states left.
func (a *applier) settleChangeImpacts(editable []string) error {
	// LandingGate may itself need to read the graph, which this transaction would block (the stores are not
	// reentrant): askLandingGate asks it in its own rolled-back pass before this transaction opens, and passes the
	// answer in as a.landing. a.collect means this call IS that earlier pass: nothing to check yet, just leave the
	// blackboard (landingBlackboard) for the caller to evaluate outside it.
	if a.collect {
		return nil
	}
	if a.g.LandingGate != nil && a.landing == nil {
		return fmt.Errorf("change %s: the landing gate was not asked before this transaction: %w", a.change.ID, ErrInvalid)
	}
	switch {
	case a.landing != nil && a.landing.decided:
		if !a.landing.ok {
			return invalidf("the change does not satisfy the goal of its landing gate")
		}
	case len(editable) > 0:
		return invalidf("the change leaves nodes in an editable state, move them out of it before applying: %s", joinSorted(editable))
	}
	return nil
}

// landingBlackboard builds the blackboard LandingGate decides against: the change with its impacts (for the
// changeImpacts condition variable), hydrated with the pre/post node content this Apply already resolved (ADR
// 0024) - not a fresh read, since this change's own pending writes are not yet visible outside this transaction.
func (a *applier) landingBlackboard() domain.Blackboard {
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
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactLanded, Landed: &ref, Baseline: result, Branch: domain.BranchOf(own.Parent)}); err != nil {
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
