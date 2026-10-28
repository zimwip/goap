package graph

import (
	"context"
	"maps"
	"math"

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
	out, err := forkVersions(ctx, tx, b, ids)
	if err != nil {
		return nil, err
	}
	ls, err := tx.BranchCommits(ctx, b.Namespace, b.Name, math.MaxInt64, ids)
	if err != nil {
		return nil, err
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
	return out, nil
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
	_, err = g.commitOwn(ctx, tx, own, domain.Commit{Name: c.Title, ChangeID: c.ID, Kind: domain.CommitKindRebase, MergedFrom: cur.ForkCommit, RebasedFrom: from})
	return err
}
