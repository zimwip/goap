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
func branchVersions(ctx context.Context, tx Tx, b domain.Branch, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	fork, err := forkVersions(ctx, tx, b, ids)
	if err != nil {
		return nil, err
	}
	ls, err := tx.BranchCommits(ctx, b.Namespace, b.Name, math.MaxInt64, ids)
	if err != nil {
		return nil, err
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

// forkVersions reads the versions some nodes (nil: every node) have in the fork of the branch of a change.
func forkVersions(ctx context.Context, tx Tx, b domain.Branch, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	if ids != nil {
		return versionsAt(ctx, tx, b.ForkCommit, ids)
	}
	fork, err := tx.Commit(ctx, b.ForkCommit)
	if err != nil {
		return nil, err
	}
	out := maps.Clone(fork.Nodes)
	if out == nil {
		out = map[domain.NodeID]domain.Version{}
	}
	return out, nil
}

// onto is what the branch of a change is rebased onto and merged into: the head of the branch it was forked from, a
// lasting branch or the branch of its parent change (ADR 0103).
type onto struct {
	parent domain.Branch
	// head is the head of the parent: for the branch of a change, its head commit, else its fork (Branch.HeadOrFork)
	head domain.CommitID
}

// ontoOf reads what the branch of a change is forked from, as it stands now.
func ontoOf(ctx context.Context, tx Tx, own domain.Branch) (onto, error) {
	p, err := branchOf(ctx, tx, own.Namespace, own.Parent)
	if err != nil {
		return onto{}, err
	}
	if p.Ephemeral {
		return onto{parent: p, head: p.HeadOrFork()}, nil
	}
	h, err := branchHeadHeader(ctx, tx, own.Namespace, p.Name)
	return onto{parent: p, head: h.ID}, err
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

// moved reports whether what the branch own is forked from moved since its fork: the head of its parent branch.
func (o onto) moved(_ context.Context, _ Tx, own domain.Branch) (bool, error) {
	return o.head != own.ForkCommit, nil
}

// forkOn moves the fork of a branch to what it is forked from now (a rebase, ADR 0101).
func (o onto) forkOn(ctx context.Context, tx Tx, own *domain.Branch) error {
	if own.RebasedFrom == "" {
		own.RebasedFrom = own.ForkCommit
	}
	return o.forkOnHead(ctx, tx, own)
}

// forkOnHead forks a branch from what o is now: its head, read when o has none.
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

// forkMoved records the move of the fork of the branch of a change (a rebase that only moves the fork, ADR 0140; the
// fork after a merge, ADR 0166; a new parent, ADR 0106) as a commit of the branch, once it committed something: its
// head then stands on the new fork (ADR 0175). A branch nothing committed on yet is its fork, no commit says it.
func (g *Graph) forkMoved(ctx context.Context, tx Tx, c domain.Change, own *domain.Branch, from domain.CommitID) error {
	if err := tx.PutBranch(ctx, *own); err != nil {
		return err
	}
	cur, err := tx.Branch(ctx, own.Namespace, own.Name)
	if err != nil {
		return err
	}
	*own = cur
	if cur.Head == "" || cur.ForkCommit == from {
		return nil
	}
	reverts, seq, err := g.refreshReverts(ctx, tx, c, cur, from)
	if err != nil {
		return err
	}
	b, err := g.commitOwn(ctx, tx, own, domain.Commit{Name: c.Title, ChangeID: c.ID, Kind: domain.CommitKindRebase, MergedFrom: cur.ForkCommit, RebasedFrom: from})
	if err != nil || len(reverts) == 0 {
		return err
	}
	// the reverts written again commit for their impacts, in the entry they name (ADR 0174 addendum)
	committed := make([]domain.ImpactEvent, len(reverts))
	for i, n := range reverts {
		ref := n.Ref()
		committed[i] = domain.ImpactEvent{Change: c.ID, Impact: n.ChangeImpact, Op: domain.ImpactCommitted, Committed: &ref, Commit: b.ID, Branch: domain.BranchOf(own.Name)}
	}
	return g.emitAt(ctx, tx, seq, committed...)
}

// refreshReverts writes again, over the new fork of the branch of a change, the reverts the branch holds of the nodes
// the fork moved (ADR 0176): a revert is a version like any other, a copy of the version of the fork it puts back, and a
// branch taking the commits of the change reads it as it is, so it must stay the copy of the fork the branch stands on.
// The new versions commit in the commit recording the move, named by the log entry at the position returned.
func (g *Graph) refreshReverts(ctx context.Context, tx Tx, c domain.Change, own domain.Branch, from domain.CommitID) ([]domain.Node, int64, error) {
	ls, err := tx.BranchCommits(ctx, own.Namespace, own.Name, math.MaxInt64, nil)
	if err != nil {
		return nil, 0, err
	}
	var ids []domain.NodeID
	for id, l := range ls {
		if l.Revert {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, 0, nil
	}
	slices.Sort(ids)
	before, err := versionsAt(ctx, tx, from, ids)
	if err != nil {
		return nil, 0, err
	}
	after, err := versionsAt(ctx, tx, own.ForkCommit, ids)
	if err != nil {
		return nil, 0, err
	}
	var wrote []domain.Node
	var seq int64
	for _, id := range ids {
		if before[id] == after[id] {
			continue
		}
		if seq == 0 {
			if seq, err = tx.ReserveLogSeq(ctx, c.ID); err != nil {
				return nil, 0, err
			}
		}
		// the impact the revert commits for: the one of the revert it replaces
		n, err := g.writeRevert(ctx, tx, own.Name, c.ID, "", seq, id, after[id], "reverted again over the new fork of the change")
		if err != nil {
			return nil, 0, err
		}
		wrote = append(wrote, n)
	}
	return wrote, seq, nil
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
	if err := tx.PutNode(ctx, n); err != nil {
		return domain.Node{}, err
	}
	if err := g.putLinks(ctx, tx, n, links, like...); err != nil {
		return domain.Node{}, err
	}
	return n, nil
}
