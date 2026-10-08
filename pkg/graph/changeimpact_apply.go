package graph

import (
	"context"
	"errors"
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

// prepareChangeImpacts writes, from the drafts of the accepted change impacts, the versions the change produces (ADR
// 0079): a node has no version until here. Every change impact must have been reviewed; a rejected one is left out, an
// accepted one without a draft is a confirmation (a modified node: nothing to write) or an error (a created one). The
// versions are written on the branch of the change with their outgoing links, the links to a node of the change
// resolved to the version this landing writes for it; they are checked when written (checkChangeImpacts), the
// transaction being rolled back when one fails.
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
		d := ownDraft(rows, cn.ID, "")
		if d == nil {
			if cn.Intent == domain.IntentModified {
				continue // an impact confirmed, with no new version to write
			}
			return invalidf("change impact %s (%s) is accepted but its node is not written: write it or reject it", cn.Key, cn.ID)
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
			from[n.ID] = l.d.Base.Version
		}
		if l.cn.Pre != nil {
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
		a.target[nodes[i].ID] = nodes[i].Version
	}
	return nil
}

// writeVersion writes the version of a draft on the branch of the change: the next version number of the node, the
// version the draft was checked out from as its parent, the origin of the change impact (its last review comment, else
// its rationale). The node changing on the change branch since the draft was checked out is a conflict (the branch moved
// by other means: a sub-change lands in its parent's log, ADR 0081).
func (a *applier) writeVersion(cn domain.ChangeImpact, d domain.Draft) (domain.Node, error) {
	comment := cn.Rationale
	if len(cn.Reviews) > 0 {
		comment = cn.Reviews[len(cn.Reviews)-1].Comment
	}
	n := domain.Node{ID: d.Node, Branch: a.branch, Namespace: domain.NamespaceOf(a.change.Namespace), Key: d.Key, Type: d.Type, Properties: cloneMap(d.Properties),
		State: d.State, ChangeID: a.change.ID, Owner: d.Owner, ChangeImpact: cn.ID, Comment: comment, Execution: d.Execution, Origins: slices.Clone(d.Origins), CreatedAt: a.g.now()}
	if d.Base == nil {
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
	if latest, err := a.tx.LatestOn(a.ctx, d.Node, a.branch); err == nil && latest.Version != d.Base.Version {
		return n, fmt.Errorf("%s changed on the branch of change %s since it was checked out (v%d, checked out from v%d): %w", d.Key, a.change.ID, latest.Version, d.Base.Version, ErrConflict)
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return n, err
	}
	return n, a.tx.PutNode(a.ctx, n)
}

// mainImpacts are the impacts of the main flow that count at landing.
func (a *applier) mainImpacts() []domain.ChangeImpact {
	return slices.DeleteFunc(slices.Clone(a.change.Nodes), func(cn domain.ChangeImpact) bool { return cn.Flow != "" || cn.Superseded })
}

// checkChangeImpacts validates the versions produced by the change impacts once the target graph is known: property
// validators, the states the nodes are left in and the transitions they went through, then what they leave (the landing
// gate, else no node left in a state that cannot land). A transition was authorized, guarded and acted when it was taken
// (ImpactNodeTransition, ADR 0076): the walk only checks that each state change on the branch is a move of the lifecycle.
func (a *applier) checkChangeImpacts() error {
	stuck, err := a.walkChangeImpacts()
	if err != nil {
		return err
	}
	return a.settleChangeImpacts(stuck)
}

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

// settleChangeImpacts decides what the walk found: the guardian of the change, else the states left that cannot land.
func (a *applier) settleChangeImpacts(stuck []string) error {
	// The guardian of the change (ADR 0098) may itself need to read the graph, which this transaction would block (the
	// stores are not reentrant): askLandingGate asks it in its own rolled-back pass before this transaction opens, and
	// passes the answer in as a.landing. a.collect means this call IS that earlier pass: nothing to check yet, just leave the
	// blackboard (landingBlackboard) for the caller to evaluate outside it.
	if a.collect {
		return nil
	}
	if a.change.Guardian != "" && a.landing == nil {
		return fmt.Errorf("change %s: its guardian was not asked before this transaction: %w", a.change.ID, ErrInvalid)
	}
	switch {
	case a.landing != nil && a.landing.decided:
		if !a.landing.ok {
			return invalidf("the guardian of the change refuses its landing")
		}
	case len(stuck) > 0:
		return invalidf("the change leaves nodes in a state that cannot land, move them to a landable state first: %s", joinSorted(stuck))
	}
	return nil
}

// landingBlackboard builds the blackboard the guardian decides the landing against (Guardian.MayCommit): the change with its impacts (for the
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
			v.NotLandable = !lc.Landable(n.State)
		}
		bb.Nodes[n.Ref()] = v
	}
	for _, cp := range a.cposts {
		add(cp.pre)
		post := cp.post
		add(&post)
		// the change impact still holds the draft reference (ADR 0079): it reads the version written from the draft
		bb.Nodes[domain.DraftRef(post.ID)] = bb.Nodes[post.Ref()]
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
