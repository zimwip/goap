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
	// links are the outgoing links the commit wrote for post
	links []domain.Link
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
	type commit struct {
		cn domain.ChangeImpact
		d  domain.Draft
	}
	var todo []commit
	impacts := a.mainImpacts()
	// the versions the drafts start from, read in one batch: what the drafts are compared with, and the links the new
	// versions share or write a delta over (ADR 0141)
	var bases []domain.NodeRef
	for _, cn := range impacts {
		if cn.Review != domain.ReviewAccepted {
			continue
		}
		if d := ownDraft(rows, cn.ID, ""); d != nil && d.Base != nil {
			bases = append(bases, *d.Base)
		}
		if cn.Pre != nil {
			bases = append(bases, *cn.Pre)
		}
	}
	if err := prefetchVersions(a.ctx, a.tx, bases); err != nil {
		return err
	}
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
		if !a.takesImpact(cn, impacts) {
			continue
		}
		if err := a.g.checkOrigins(impacts, cn, d); err != nil {
			return err
		}
		todo = append(todo, commit{cn, *d})
	}
	// 1. the versions: the next version of each node, whatever the branch it was written on; the latest version of
	// each node, and the latest on the change's branch, read in two statements
	var ids []domain.NodeID
	var keys []string // of the creations, checked free in one statement
	for _, l := range todo {
		if l.d.Base != nil {
			ids = append(ids, l.d.Node)
		} else {
			keys = append(keys, l.d.Key)
		}
	}
	forget, err := prefetchNodeKeys(a.ctx, a.tx, a.change.Namespace, keys)
	defer forget()
	if err != nil {
		return err
	}
	if a.latest, err = a.tx.LatestVersions(a.ctx, ids); err != nil {
		return err
	}
	if a.latestOn, err = a.tx.LatestVersionsOn(a.ctx, ids, a.branch); err != nil {
		return err
	}
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
	wrote := make([][]domain.Link, len(todo))
	for i, l := range todo {
		links := make([]domain.Link, 0, len(l.d.Links))
		for _, dl := range l.d.Links {
			to := dl.To
			if v, ok := written[to.ID]; ok && (to.IsDraft() || to.Version == from[to.ID] || (l.d.Base != nil && to.Version == l.d.Base.Version && to.ID == l.d.Node)) {
				to.Version = v
			}
			if to.IsDraft() {
				return invalidf("%s links to the draft of node %s, which the change does not commit", nodes[i].Key, to.ID)
			}
			links = append(links, domain.Link{ID: dl.ID, Type: dl.Type, To: to, Target: dl.Target, Properties: dl.Properties})
		}
		// the links of a version are a set, shared with the version it was checked out from or a delta over it (ADR 0141)
		var like []domain.NodeRef
		for _, p := range nodes[i].Parents {
			like = append(like, domain.NodeRef{ID: nodes[i].ID, Version: p})
		}
		if err := a.g.putLinks(a.ctx, a.tx, nodes[i], links, like...); err != nil {
			return err
		}
		for j := range links {
			links[j].From, links[j].ChangeID = nodes[i].Ref(), nodes[i].ChangeID
		}
		wrote[i] = links
	}
	// 3. what the change leaves
	for i, l := range todo {
		cp := cpost{cn: l.cn, post: nodes[i], links: wrote[i]}
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
		State: d.State, ChangeID: a.change.ID, Responsible: d.Owner, ChangeImpact: cn.ID, Comment: comment, Execution: d.Execution, Origins: slices.Clone(d.Origins), CreatedAt: a.g.now(),
		Creator: domain.Creator{Subject: d.Creator}, Editor: d.Editor}
	if d.Base == nil {
		// a key is one node's: another change may have created it meanwhile
		id, err := a.tx.NodeIDByKey(a.ctx, n.Namespace, n.Key)
		switch {
		case err == nil && id != n.ID:
			return n, fmt.Errorf("%s (%s): the key is taken by a node another change created meanwhile: rename it or withdraw it: %w", d.Key, d.Type, ErrConflict)
		case err == nil:
			// a node only abandoned changes or other lines of work wrote, taken over by the creation (takeKey): its next
			// version, still a creation
			if err := a.g.takeKey(a.ctx, a.tx, a.change, n.ID, n.Key, n.Type); err != nil {
				return n, err
			}
			last, err := a.tx.LatestVersions(a.ctx, []domain.NodeID{n.ID})
			if err != nil {
				return n, err
			}
			n.Version = last[n.ID] + 1
		case errors.Is(err, ErrNotFound):
			n.Version = 1
		default:
			return n, err
		}
		n.Reason = domain.ReasonCreate
		return n, a.tx.PutNode(a.ctx, n)
	}
	last, ok := a.latest[d.Node]
	if !ok {
		return n, fmt.Errorf("node %s: %w", d.Node, ErrNotFound)
	}
	n.Version, n.Parents, n.Reason = last+1, []domain.Version{d.Base.Version}, domain.ReasonDerive
	// the version checked out from is the latest of the node on the branch, or the node is not on it (the latest being
	// that version, it is on the branch)
	if on, ok := a.latestOn[d.Node]; ok && on != d.Base.Version {
		return n, fmt.Errorf("%s changed on the branch of change %s since it was checked out (v%d, checked out from v%d): %w", d.Key, a.change.ID, on, d.Base.Version, ErrConflict)
	} else if ok {
		n.Reason = domain.ReasonRevise
	}
	if d.MergedWith != nil && d.MergedWith.Version != d.Base.Version {
		// a rebase merged the parent branch's version into the change's (ADR 0102): a merge written on the current
		// branch, seen from it (ADR 0133)
		n.Parents, n.Reason = append(n.Parents, d.MergedWith.Version), domain.ReasonRebase
	}
	return n, a.tx.PutNode(a.ctx, n)
}

// mainImpacts are the impacts of the main flow that count at commit.
func (a *applier) mainImpacts() []domain.ChangeImpact { return mainImpactsOf(a.change) }

// walkChangeImpacts validates the properties of each produced version and walks its transitions: it returns the nodes
// left in a state that cannot commit (ADR 0078).
func (a *applier) walkChangeImpacts() (stuck []string, err error) {
	for _, cp := range a.cposts {
		n := cp.post
		if n.Deleted {
			continue
		}
		if err := a.g.checkVersion(a.ctx, a.ix, n, cp.links, storedResolver(a.ctx, a.tx)); err != nil {
			return nil, err
		}
		lc := a.ix.lifecycleOf(n.Type)
		if lc == nil || n.State == "" {
			continue
		}
		if !lc.Committable(n.State) {
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
		evs, err := a.g.foldedEvents(a.ctx, a.tx, a.change.ID)
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

// commit records, once the branch of a change is merged into its parent (the baseline result), the
// version each change impact committed as (only: the impacts of the nodes this merge moved; nil: every one): the version it wrote, joined to the parent, or the merge
// version of mergeChange. The other open changes on the same nodes are not read: each starts from the latest version
// of its nodes when it is rebased, at its own merge (ADR 0101, 0140).
func (g *Graph) commit(ctx context.Context, tx Tx, c domain.Change, own domain.Branch, mergeChange domain.ChangeID, result domain.BaselineID, only map[domain.NodeID]bool) error {
	commits := func(cn domain.ChangeImpact) bool {
		return cn.Post != nil && (cn.Review == domain.ReviewAccepted || cn.Committed != nil) && cn.Flow == "" && !cn.Superseded &&
			(only == nil || only[cn.Post.ID])
	}
	// the latest version of each node on the target, read in two statements
	var ids []domain.NodeID
	for _, cn := range c.Nodes {
		if commits(cn) {
			ids = append(ids, cn.Post.ID)
		}
	}
	at, err := tx.LatestVersionsOn(ctx, ids, domain.BranchOf(own.Parent))
	if err != nil {
		return err
	}
	refs := make([]domain.NodeRef, 0, len(at))
	for id, v := range at {
		refs = append(refs, domain.NodeRef{ID: id, Version: v})
	}
	heads, err := tx.NodesAt(ctx, refs, "")
	if err != nil {
		return err
	}
	byID := make(map[domain.NodeID]domain.Node, len(heads))
	for _, n := range heads {
		byID[n.ID] = n
	}
	var committed []domain.ImpactEvent
	for _, cn := range c.Nodes {
		if !commits(cn) {
			continue
		}
		post := *cn.Post
		if post.IsDraft() && cn.Committed != nil {
			post = *cn.Committed // checked out again after its commit, left as it was (ADR 0102)
		}
		head, ok := byID[post.ID]
		if !ok {
			return fmt.Errorf("node %s on %s: %w", post.ID, domain.BranchOf(own.Parent), ErrNotFound)
		}
		if head.ChangeID != mergeChange && head.Ref() != post {
			continue // left as is on the target: nothing of this change committed
		}
		ref := head.Ref()
		comment := cn.Rationale
		if len(cn.Reviews) > 0 {
			comment = cn.Reviews[len(cn.Reviews)-1].Comment
		}
		// a version the change wrote itself already names it (a fast-forward): rewriting the row would only cost a new
		// tuple and its index entries
		if head.ChangeID != c.ID || head.ChangeImpact != cn.ID || head.Comment != comment {
			if err := tx.SetNodeOrigin(ctx, ref, c.ID, cn.ID, comment); err != nil {
				return err
			}
		}
		committed = append(committed, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactCommitted, Committed: &ref, Baseline: result, Branch: domain.BranchOf(own.Parent)})
	}
	// one entry for the commit (ADR 0144)
	return g.emit(ctx, tx, committed...)
}
