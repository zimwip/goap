package graph

import (
	"context"
	"maps"
	"math"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// The branch of a change holds commits like any branch (ADR 0175): each commit of the change, each merge of a
// sub-change into it and each move of its fork once it committed is a row, its head the last (Branch.Head). Its state is
// its fork (Branch.ForkCommit, a commit of the branch it was forked from: a lasting one, or the branch of the parent
// change for a sub-change, ADR 0103) with the versions committed on it, the last commit of each node winning
// (Tx.BranchCommits); a revert (domain.ReasonRevert) gives the node back the version of the fork. It is the state of its
// head commit (the selection of ADR 0143 over the fork that commit records), read here from the branch as it stands in
// the transaction, its last commit written or not.

// branchVersions reads the versions some nodes (nil: every node) have on the branch of a change, the nodes it does not
// hold left out.
//
// The commits the branch shares with its target (its base, ADR 0177) are commits it holds: the greatest of all it holds
// is read over its fork alone, the base read only for the nodes a revert gives back to it.
func branchVersions(ctx context.Context, tx Tx, b domain.Branch, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	var fork map[domain.NodeID]domain.Version
	if ids != nil {
		at, err := versionsAt(ctx, tx, b.ForkCommit, ids)
		if err != nil {
			return nil, err
		}
		fork = maps.Clone(at)
	} else if b.ForkCommit != "" {
		at, err := tx.Commit(ctx, b.ForkCommit)
		if err != nil {
			return nil, err
		}
		fork = maps.Clone(at.Nodes)
	}
	ls, err := tx.BranchCommits(ctx, b.Namespace, b.Name, math.MaxInt64, ids)
	if err != nil {
		return nil, err
	}
	var reverts []domain.NodeID
	for id, l := range ls {
		if l.Revert {
			reverts = append(reverts, id)
		}
	}
	if len(reverts) > 0 {
		slices.Sort(reverts)
		base, err := baseVersions(ctx, tx, b, reverts)
		if err != nil {
			return nil, err
		}
		if fork == nil {
			fork = map[domain.NodeID]domain.Version{}
		}
		for _, id := range reverts {
			if v, ok := base[id]; ok {
				fork[id] = v
			} else {
				delete(fork, id)
			}
		}
	}
	return overFork(fork, ls), nil
}

// overFork is the state of a branch from the versions of its fork and the commits it holds (Tx.BranchCommits): a
// revert gives the fork's version back, a tombstone leaves the node out. fork is not changed.
func overFork(fork map[domain.NodeID]domain.Version, ls map[domain.NodeID]CommittedVersion) map[domain.NodeID]domain.Version {
	out := maps.Clone(fork)
	if out == nil {
		out = map[domain.NodeID]domain.Version{}
	}
	for id, l := range ls {
		switch {
		case l.Revert:
			// back to what the branch was forked from
		case l.Deleted:
			delete(out, id)
		default:
			out[id] = l.Version
		}
	}
	return out
}

// baseVersions reads the versions some nodes (nil: every node) have in the base of the branch of a change (ADR 0177):
// the commits it holds that its target holds too (the target as the branch last took it, and what the branch merged into
// it), over the view of its parent at its fork. It is what the branch stands on: a three-way merge's base, what a revert
// gives back.
func baseVersions(ctx context.Context, tx Tx, b domain.Branch, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	var out map[domain.NodeID]domain.Version
	if ids != nil {
		fork, err := versionsAt(ctx, tx, b.ForkCommit, ids)
		if err != nil {
			return nil, err
		}
		out = maps.Clone(fork)
	} else if b.ForkCommit != "" {
		fork, err := tx.Commit(ctx, b.ForkCommit)
		if err != nil {
			return nil, err
		}
		out = maps.Clone(fork.Nodes)
	}
	if out == nil {
		out = map[domain.NodeID]domain.Version{}
	}
	if b.Parent == "" || (ids != nil && len(ids) == 0) {
		return out, nil
	}
	shared, err := tx.SharedCommits(ctx, b.Namespace, b.Name, b.Parent, ids)
	if err != nil {
		return nil, err
	}
	for id, l := range shared {
		if l.Deleted {
			delete(out, id)
		} else {
			out[id] = l.Version
		}
	}
	return out, nil
}

// referenceVersions reads some nodes in the reference commit of a change: what its branch stands on (its fork and the
// commits it took of its target, baseVersions, ADR 0177), else its fork.
func referenceVersions(ctx context.Context, tx Tx, c domain.Change, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	own, ok, err := ownBranch(ctx, tx, c)
	if err != nil {
		return nil, err
	}
	if !ok {
		return versionsAt(ctx, tx, c.ForkCommit, ids)
	}
	return baseVersions(ctx, tx, own, ids)
}

// onto is what the branch of a change is rebased onto and merged into: the head of the branch it was forked from, a
// lasting branch or the branch of its parent change (ADR 0103), and where the branch stands on it (ADR 0177).
type onto struct {
	parent domain.Branch
	// head is the head of the parent: for the branch of a change, its head commit, else its fork (Branch.HeadOrFork)
	head domain.CommitID
	// pos is the position of the branch on the parent (Tx.Position), last the last order of the parent: the branch is
	// up to date when it holds the parent's last commit (pos == last)
	pos, last int64
}

// ontoOf reads what the branch of a change is forked from, as it stands now.
func ontoOf(ctx context.Context, tx Tx, own domain.Branch) (onto, error) {
	p, err := branchOf(ctx, tx, own.Namespace, own.Parent)
	if err != nil {
		return onto{}, err
	}
	o := onto{parent: p}
	if p.Ephemeral {
		o.head = p.HeadOrFork()
		if o.last, err = tx.LastCommit(ctx, p.Name); err != nil {
			return onto{}, err
		}
	} else {
		// a lasting branch is shared by the namespaces: its last order in this one is the order of its head
		h, err := branchHeadHeader(ctx, tx, own.Namespace, p.Name)
		if err != nil {
			return onto{}, err
		}
		o.head, o.last = h.ID, h.Seq
	}
	return o, o.position(ctx, tx, own)
}

// position reads where the branch own stands on the parent of o.
func (o *onto) position(ctx context.Context, tx Tx, own domain.Branch) error {
	var forkSeq int64
	if own.ForkCommit != "" {
		f, err := headerOf(ctx, tx, own.ForkCommit)
		if err != nil {
			return err
		}
		forkSeq = f.Seq
	}
	var err error
	o.pos, err = tx.Position(ctx, own.Name, o.parent.Name, forkSeq)
	return err
}

// versions reads some nodes (nil: every node) in what the branch is forked from now.
func (o onto) versions(ctx context.Context, tx Tx, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	if o.parent.Ephemeral {
		return branchVersions(ctx, tx, o.parent, ids)
	}
	if ids == nil {
		b, err := tx.Commit(ctx, o.head)
		return b.Nodes, err
	}
	return versionsAt(ctx, tx, o.head, ids)
}

// moved reports whether what the branch is forked from moved past what the branch took of it (ADR 0177).
func (o onto) moved(_ context.Context, _ Tx, _ domain.Branch) (bool, error) {
	return o.pos < o.last, nil
}

// forkOnHead forks a new branch from what o is now: its head, read when o has none.
func (o onto) forkOnHead(ctx context.Context, tx Tx, own *domain.Branch) error {
	if o.head == "" {
		if o.parent.Ephemeral {
			p, err := tx.Branch(ctx, o.parent.Namespace, o.parent.Name)
			if err != nil {
				return err
			}
			o.head = p.HeadOrFork()
		} else {
			h, err := branchHeadHeader(ctx, tx, o.parent.Namespace, o.parent.Name)
			if err != nil {
				return err
			}
			o.head = h.ID
		}
	}
	own.ForkCommit = o.head
	return nil
}

// commitOwn writes a commit on the branch of a change and makes it its head (ADR 0175): its parent is the head of the
// branch, else the commit it was forked from, and it records the fork it stands on (Commit.Fork). own follows the
// branch as written; the commit returned carries its order.
func (g *Graph) commitOwn(ctx context.Context, tx Tx, own *domain.Branch, b domain.Commit) (domain.Commit, error) {
	cur, err := tx.Branch(ctx, own.Namespace, own.Name)
	if err != nil {
		return b, err
	}
	b.ID = domain.CommitID(g.newID())
	b.Namespace, b.Branch = cur.Namespace, cur.Name
	b.ParentID, b.Fork = cur.HeadOrFork(), cur.ForkCommit
	if b.CreatedAt.IsZero() {
		b.CreatedAt = g.now()
	}
	if err := tx.PutCommit(ctx, b); err != nil {
		return b, err
	}
	if gt, ok := unwrapGuard(tx); ok {
		b.Seq = gt.wroteHeads[b.ID].Seq
	}
	cur.Head = b.ID
	if gt, ok := unwrapGuard(tx); ok {
		if gt.ownHeads == nil {
			gt.ownHeads = map[string]domain.CommitID{}
		}
		gt.ownHeads[guardBranchKey(cur.Namespace, cur.Name)] = b.ID
	}
	if err := tx.PutBranch(ctx, cur); err != nil {
		return b, err
	}
	*own = cur
	return b, nil
}

// takeTarget brings the branch of a change up to what it is forked from (ADR 0177): the branch takes, as rows, the
// commits its parent holds past its position, recorded by a change.updated entry naming the head taken
// (HeaderRebasedOnto); its fork never moves, nothing is updated. A revert of the branch whose node the commits taken
// moved is written again over them (a revert is the copy of what the branch stands on, ADR 0176), in one commit of the
// branch: the only commit a rebase with no conflict writes. own and to follow.
func (g *Graph) takeTarget(ctx context.Context, tx Tx, c domain.Change, own *domain.Branch, to *onto) error {
	if to.pos >= to.last {
		return nil
	}
	// the reverts of the branch, and what they stand on before the commits are taken
	var reverts []domain.NodeID
	var before map[domain.NodeID]domain.Version
	if own.Head != "" {
		mine, err := tx.BranchNodeIDs(ctx, own.Name, own.Parent)
		if err != nil {
			return err
		}
		// a revert is of a node no impact of the change holds any more (withdrawn after its commit, withdrawnSince)
		held := map[domain.NodeID]bool{}
		for _, cn := range mainImpactsOf(c).All() {
			held[nodeOf(cn)] = true
		}
		mine = slices.DeleteFunc(mine, func(id domain.NodeID) bool { return held[id] })
		if len(mine) > 0 {
			ls, err := tx.BranchCommits(ctx, own.Namespace, own.Name, math.MaxInt64, mine)
			if err != nil {
				return err
			}
			for id, l := range ls {
				if l.Revert {
					reverts = append(reverts, id)
				}
			}
		}
		if len(reverts) > 0 {
			slices.Sort(reverts)
			if before, err = baseVersions(ctx, tx, *own, reverts); err != nil {
				return err
			}
		}
	}
	seqs, err := tx.JoinedCommits(ctx, own.Namespace, to.parent.Name, "", to.pos, to.last)
	if err != nil {
		return err
	}
	e, err := domain.HeaderEntry(g.newID(), c.ID, g.caller(ctx), g.now(),
		domain.HeaderEdit{Fields: map[string]domain.HeaderValue{HeaderRebasedOnto: {To: string(to.head)}}})
	if err != nil {
		return err
	}
	e, err = tx.AppendLog(ctx, e)
	if err != nil {
		return err
	}
	if err := takeRows(ctx, tx, own.Name, c.ID, e.Seq, seqs); err != nil {
		return err
	}
	to.pos = to.last
	if len(reverts) == 0 {
		return nil
	}
	after, err := baseVersions(ctx, tx, *own, reverts)
	if err != nil {
		return err
	}
	var wrote []domain.Node
	var seq int64
	for _, id := range reverts {
		if before[id] == after[id] {
			continue
		}
		if seq == 0 {
			if seq, err = tx.ReserveLogSeq(ctx, c.ID); err != nil {
				return err
			}
		}
		// the impact the revert commits for: the one of the revert it replaces
		n, err := g.writeRevert(ctx, tx, own.Name, c.ID, "", seq, id, after[id], "reverted again over the commits taken by a rebase")
		if err != nil {
			return err
		}
		wrote = append(wrote, n)
	}
	if len(wrote) == 0 {
		return nil
	}
	b, err := g.commitOwn(ctx, tx, own, domain.Commit{Name: c.Title, ChangeID: c.ID, Kind: domain.CommitKindRebase, MergedFrom: to.head})
	if err != nil {
		return err
	}
	// the reverts written again commit for their impacts, in the entry they name (ADR 0174 addendum)
	committed := make([]domain.ImpactEvent, len(wrote))
	for i, n := range wrote {
		ref := n.Ref()
		committed[i] = domain.ImpactEvent{Change: c.ID, Impact: n.ChangeImpact, Op: domain.ImpactCommitted, Committed: &ref, Commit: b.ID, Branch: domain.BranchOf(own.Name)}
	}
	return g.emitAt(ctx, tx, seq, committed...)
}

// catchUp makes the new branch of a sub-change take what the branch of its parent took past the commit it is forked
// from (ADR 0177 §9): the sub-change starts from its parent's branch as it stands.
func (g *Graph) catchUp(ctx context.Context, tx Tx, c domain.Change) error {
	own, ok, err := ownBranch(ctx, tx, c)
	if err != nil || !ok || own.Parent == "" {
		return err
	}
	to, err := ontoOf(ctx, tx, own)
	if err != nil {
		return err
	}
	return g.takeTarget(ctx, tx, c, &own, &to)
}

// writeRevert writes on the branch of a change a revert of a node: a version with the properties and links of the
// version v of the fork of the branch, a deletion when the fork does not hold the node (v 0), which the branch reads as
// that version (ADR 0140) and a branch taking its commit as it is (ADR 0176). It names the impact withdrawn it commits
// for (empty: the one of the version it follows, a revert written again) and the log entry committing it (logSeq).
func (g *Graph) writeRevert(ctx context.Context, tx Tx, branch string, change domain.ChangeID, cn domain.ChangeImpactID, logSeq int64, id domain.NodeID, v domain.Version, comment string) (domain.Node, error) {
	latest, err := tx.LatestOn(ctx, id, branch)
	if err != nil {
		return domain.Node{}, err
	}
	next, err := nextVersion(ctx, tx, id)
	if err != nil {
		return domain.Node{}, err
	}
	if cn == "" {
		cn = latest.ChangeImpact
	}
	n := domain.Node{ID: id, Version: next, Branch: branch, Namespace: latest.Namespace, Key: latest.Key, Type: latest.Type, ChangeID: change,
		ChangeImpact: cn, LogSeq: logSeq, Reason: domain.ReasonRevert, Parents: []domain.Version{latest.Version}, Responsible: latest.Responsible, Project: latest.Project,
		Comment: comment, CreatedAt: g.now()}
	var links []domain.Link
	like := []domain.NodeRef{latest.Ref()}
	if v > 0 {
		src, err := tx.Node(ctx, domain.NodeRef{ID: id, Version: v})
		if err != nil {
			return domain.Node{}, err
		}
		n.Properties, n.State, n.Responsible, n.Origins = cloneMap(src.Properties), src.State, src.Responsible, slices.Clone(src.Origins)
		if links, err = tx.OutLinks(ctx, src.Ref()); err != nil {
			return domain.Node{}, err
		}
		like = []domain.NodeRef{src.Ref(), latest.Ref()} // the set of the fork's version, shared as is (ADR 0141)
	} else {
		n.Properties, n.State, n.Deleted = cloneMap(latest.Properties), latest.State, true
	}
	if err := g.putVersion(ctx, tx, n, links, like...); err != nil {
		return domain.Node{}, err
	}
	return n, nil
}
