package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// cpost is a change impact of the change being applied, with the version it
// produced (the latest of its node on the change branch).
type cpost struct {
	cn   domain.ChangeImpact
	pre  *domain.Node
	post domain.Node
}

// prepareChangeImpacts writes, from the drafts of the change impacts, the versions the commit produces (ADR 0079): a node
// has no version until here. One rule for every impact of the main flow (ADR 0102): the accepted modifications are
// written; everything else stays in the change as it is (a candidate, a draft left as it was checked out, a modification
// awaiting its review or rejected, one committed already). The versions are written on the branch of the change with
// their outgoing links, the links to a node of the change resolved to the version this commit writes for it; they are
// checked when written (walkChangeImpacts), the transaction being rolled back when one fails.
func (a *applier) prepareChangeImpacts() error {
	rows, err := a.g.drafts(a.ctx, a.tx, a.change.ID)
	if err != nil {
		return err
	}
	type landing struct {
		cn domain.ChangeImpact
		d  domain.Draft
	}
	var todo []landing
	impacts := a.mainImpacts()
	for _, cn := range impacts {
		if cn.Review != domain.ReviewAccepted {
			continue
		}
		d := ownDraft(rows, cn.ID, "")
		if ok, err := a.g.draftModifies(a.ctx, a.tx, d); err != nil {
			return err
		} else if !ok {
			continue
		}
		if err := a.g.checkOrigins(impacts, cn, d); err != nil {
			return err
		}
		todo = append(todo, landing{cn, *d})
	}
	// 1. the versions: the next version of each node, whatever the branch it was written on
	written := map[domain.NodeID]domain.Version{}
	from := map[domain.NodeID]domain.Version{} // the version each draft was checked out from
	nodes := make([]domain.Node, len(todo))
	for i, l := range todo {
		n, err := a.writeVersion(l.cn, l.d)
		if err != nil {
			return err
		}
		nodes[i] = n
		written[n.ID] = n.Version
		if l.d.Base != nil {
			from[n.ID] = l.d.Base.Version // after an earlier commit of the change: the version it wrote (ADR 0102)
		} else if l.cn.Pre != nil {
			from[n.ID] = l.cn.Pre.Version
		}
	}
	// 2. their links, to the versions of the change
	for i, l := range todo {
		for _, dl := range l.d.Links {
			to := dl.To
			if v, ok := written[to.ID]; ok && (to.IsDraft() || to.Version == from[to.ID] || (l.d.Base != nil && to.Version == l.d.Base.Version && to.ID == l.d.Node)) {
				to.Version = v
			}
			if to.IsDraft() {
				return invalidf("%s links to the draft of node %s, which the change does not land", nodes[i].Key, to.ID)
			}
			if err := a.tx.PutLink(a.ctx, domain.Link{ID: dl.ID, Type: dl.Type, From: nodes[i].Ref(), To: to, Properties: dl.Properties, ChangeID: a.change.ID}); err != nil {
				return err
			}
		}
	}
	// 3. what the change leaves
	for i, l := range todo {
		cp := cpost{cn: l.cn, post: nodes[i]}
		if l.cn.Pre != nil {
			pre, err := a.tx.Node(a.ctx, *l.cn.Pre)
			if err != nil {
				return err
			}
			cp.pre = &pre
		}
		a.cposts = append(a.cposts, cp)
		a.target.set(nodes[i].ID, nodes[i].Version)
	}
	return nil
}

// writeVersion writes the version of a draft on the branch of the change: the next version number of the node, the
// version the draft was checked out from as its parent, the origin of the change impact (its last review comment, else
// its rationale). The node changing on the change branch since the draft was checked out is a conflict (the branch moved
// by other means, such as the merge of a sub-change, ADR 0103).
func (a *applier) writeVersion(cn domain.ChangeImpact, d domain.Draft) (domain.Node, error) {
	comment := cn.Rationale
	if len(cn.Reviews) > 0 {
		comment = cn.Reviews[len(cn.Reviews)-1].Comment
	}
	n := domain.Node{ID: d.Node, Branch: a.branch, Namespace: domain.NamespaceOf(a.change.Namespace), Key: d.Key, Type: d.Type, Properties: cloneMap(d.Properties),
		State: d.State, ChangeID: a.change.ID, Owner: d.Owner, ChangeImpact: cn.ID, Comment: comment, Execution: d.Execution, Origins: slices.Clone(d.Origins), CreatedAt: a.g.now()}
	if d.Base == nil {
		// a key is one node's: another change may have created it meanwhile
		if id, err := a.tx.NodeIDByKey(a.ctx, n.Namespace, n.Key); err == nil && id != n.ID {
			return n, fmt.Errorf("%s (%s): the key is taken by a node another change created meanwhile: rename it or withdraw it: %w", d.Key, d.Type, ErrConflict)
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return n, err
		}
		n.Version, n.Reason = 1, domain.ReasonCreate
		return n, a.tx.PutNode(a.ctx, n)
	}
	vs, err := a.tx.Versions(a.ctx, d.Node)
	if err != nil {
		return n, err
	}
	n.Version, n.Parents, n.Reason = domain.Version(len(vs)+1), []domain.Version{d.Base.Version}, domain.ReasonDerive
	if on, err := onBranch(a.ctx, a.tx, *d.Base, a.branch); err != nil {
		return n, err
	} else if on {
		n.Reason = domain.ReasonRevise
	}
	if d.MergedWith != nil && d.MergedWith.Version != d.Base.Version {
		// a rebase merged the parent branch's version into the change's (ADR 0102): a merge written on the current
		// branch, seen from it (ADR 0133)
		n.Parents, n.Reason = append(n.Parents, d.MergedWith.Version), domain.ReasonRebase
	}
	if latest, err := a.tx.LatestOn(a.ctx, d.Node, a.branch); err == nil && latest.Version != d.Base.Version {
		return n, fmt.Errorf("%s changed on the branch of change %s since it was checked out (v%d, checked out from v%d): %w", d.Key, a.change.ID, latest.Version, d.Base.Version, ErrConflict)
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return n, err
	}
	return n, a.tx.PutNode(a.ctx, n)
}

// mainImpacts are the impacts of the main flow that count at landing.
func (a *applier) mainImpacts() []domain.ChangeImpact { return mainImpactsOf(a.change) }

// walkChangeImpacts validates the properties of each produced version and walks its transitions: it returns the nodes
// left in a state that cannot land (ADR 0078).
func (a *applier) walkChangeImpacts() (stuck []string, err error) {
	for _, cp := range a.cposts {
		n := cp.post
		if n.Deleted {
			continue
		}
		if err := a.g.checkWritten(a.ctx, a.tx, a.ix, n); err != nil {
			return nil, err
		}
		lc := a.ix.lifecycleOf(n.Type)
		if lc == nil || n.State == "" {
			continue
		}
		if !lc.Landable(n.State) {
			stuck = append(stuck, fmt.Sprintf("%s (%s) in %s", n.Key, n.Type, n.State))
		}
		if err := a.walkTransitions(cp, lc); err != nil {
			return nil, err
		}
	}
	return stuck, nil
}

// walkTransitions replays the moves recorded in the log of the change for the draft a version was written from (ADR 0079,
// the moves of ADR 0058 are all in the log): from the state the draft started in, each transitioned event of the main
// flow must leave the state the draft was in and be a move of the lifecycle, and the chain must end in the state of the
// version. Each move was authorized, guarded and acted when it was taken (ImpactNodeTransition).
func (a *applier) walkTransitions(cp cpost, lc *domain.Lifecycle) error {
	if a.events == nil {
		evs, err := impactEvents(a.ctx, a.tx, a.change.ID)
		if err != nil {
			return err
		}
		a.events = &evs
	}
	norm := func(s string) string {
		if s == "" {
			return lc.Initial
		}
		return s
	}
	at, tracked := "", false
	for _, e := range *a.events {
		if e.Impact != cp.cn.ID || e.Flow != "" {
			continue
		}
		switch {
		case e.StartsDraft():
			at, tracked = norm(e.Draft.State), true
		case e.Op == domain.ImpactTransitioned && e.Patch["state"] != nil:
			st, _ := e.Patch["state"].(map[string]any)
			from, _ := st["from"].(string)
			to, _ := st["to"].(string)
			if tracked && norm(from) != at {
				return invalidf("%s (%s) was moved from %s while it was in %s", cp.post.Key, cp.post.Type, from, at)
			}
			if _, ok := lc.Move(norm(from), to); !ok {
				return invalidf("%s (%s) cannot go from %s to %s", cp.post.Key, cp.post.Type, from, to)
			}
			at, tracked = to, true
		}
	}
	if tracked && norm(cp.post.State) != at {
		return invalidf("%s (%s) is in %s, the moves recorded for it end in %s", cp.post.Key, cp.post.Type, cp.post.State, at)
	}
	return nil
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
		if cn.Post == nil || (cn.Review != domain.ReviewAccepted && cn.Landed == nil) || cn.Flow != "" || cn.Superseded {
			continue
		}
		post := *cn.Post
		if post.IsDraft() && cn.Landed != nil {
			post = *cn.Landed // checked out again after its commit, left as it was (ADR 0102)
		}
		head, err := tx.LatestOn(ctx, post.ID, domain.BranchOf(own.Parent))
		if err != nil {
			return err
		}
		if head.ChangeID != mergeChange && head.Ref() != post {
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
		for i, oid := range others {
			if oid == c.ID || slices.Contains(others[:i], oid) {
				continue
			}
			// the head tells the changes still open: the others of a node's history are not read
			oh, err := tx.ChangeHead(ctx, oid)
			if err != nil {
				return err
			}
			if oh.Status == domain.ChangeApplied || oh.Status == domain.ChangeAbandoned || domain.BranchOf(oh.Branch) == domain.BranchOf(c.Branch) {
				continue
			}
			oc, err := tx.Change(ctx, oid)
			if err != nil {
				return err
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
