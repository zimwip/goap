package graph

import (
	"context"
	"fmt"
	"maps"
	"math"

	"github.com/zimwip/goap/contracts/domain"
)

// The branch of a change holds no commit (ADR 0140): only the lasting branches (main, the named ones) do. Its state
// is its fork, the lasting commit it was forked from (Branch.ForkCommit), with the versions committed on it, the last
// commit of each node winning (Tx.BranchCommits); a revert (domain.ReasonRevert) gives the node back the version of
// the fork. A sub-change's branch is forked from its parent's (ADR 0103): its fork is the lasting commit under the
// chain with the commits of the branches of the chain up to the order it was forked at (Branch.ForkSeq), the order of
// commit being global.

// branchVersions reads the versions some nodes (nil: every node) have on the branch of a change, the nodes it does not
// hold left out.
func branchVersions(ctx context.Context, tx Tx, b domain.Branch, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	return chainVersions(ctx, tx, b, ids, true)
}

// forkVersions reads the versions some nodes (nil: every node) have in the fork of the branch of a change.
func forkVersions(ctx context.Context, tx Tx, b domain.Branch, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	return chainVersions(ctx, tx, b, ids, false)
}

func chainVersions(ctx context.Context, tx Tx, b domain.Branch, ids []domain.NodeID, own bool) (map[domain.NodeID]domain.Version, error) {
	type layer struct {
		name string
		upTo int64
	}
	var layers []layer // the innermost first
	upTo := int64(math.MaxInt64)
	if own {
		layers = append(layers, layer{b.Name, upTo})
	}
	for cur, hops := b, 0; cur.ForkSeq > 0; hops++ {
		if hops > 64 {
			return nil, fmt.Errorf("branch %s: the chain of the branches it is forked from does not end: %w", b.Name, ErrInvalid)
		}
		upTo = min(upTo, cur.ForkSeq)
		p, err := tx.Branch(ctx, domain.NamespaceOf(cur.Namespace), cur.Parent)
		if err != nil {
			return nil, err
		}
		layers = append(layers, layer{p.Name, upTo})
		cur = p
	}
	var out map[domain.NodeID]domain.Version
	if ids == nil {
		fork, err := tx.Commit(ctx, b.ForkCommit)
		if err != nil {
			return nil, err
		}
		out = maps.Clone(fork.Nodes)
		if out == nil {
			out = map[domain.NodeID]domain.Version{}
		}
	} else {
		var err error
		if out, err = versionsAt(ctx, tx, b.ForkCommit, ids); err != nil {
			return nil, err
		}
	}
	for i := len(layers) - 1; i >= 0; i-- {
		ls, err := tx.BranchCommits(ctx, b.Namespace, layers[i].name, layers[i].upTo, ids)
		if err != nil {
			return nil, err
		}
		for id, l := range ls {
			switch {
			case l.Revert:
				// back to what the branch was forked from: what the layers under it give
			case l.Deleted:
				delete(out, id)
			default:
				out[id] = l.Version
			}
		}
	}
	return out, nil
}

// endCommit ends the commit of the transaction on the branch of a change, which no commit takes up (ADR 0140), and
// returns its order (0: nothing committed there).
func endCommit(tx Tx, branch string) int64 {
	gt, ok := unwrapGuard(tx)
	if !ok {
		return 0
	}
	branch = domain.BranchOf(branch)
	s := gt.commit[branch]
	delete(gt.commit, branch)
	return s
}

// forkPoint is where a branch forked from the branch of a change starts (ADR 0103, 0140): the lasting commit under
// the chain, and the order of commit up to which the commits of the chain are in its fork, allocated now: every
// commit of the chain committed before is under it, the tree of the change being locked.
func forkPoint(ctx context.Context, tx Tx, parent domain.Branch) (domain.CommitID, int64, error) {
	if !parent.Ephemeral {
		h, err := branchHeadHeader(ctx, tx, parent.Namespace, parent.Name)
		return h.ID, 0, err
	}
	seq, err := tx.NextCommit(ctx)
	return parent.ForkCommit, seq, err
}

// onto is what the branch of a change is rebased onto and merged into: the head of a lasting branch, or the state of
// the branch of its parent change (ADR 0103), which no commit holds.
type onto struct {
	parent domain.Branch
	// head is the head of a lasting parent; for the branch of a change, the lasting commit under its chain
	head domain.CommitID
}

// ontoOf reads what the branch of a change is forked from, as it stands now.
func ontoOf(ctx context.Context, tx Tx, own domain.Branch) (onto, error) {
	p, err := branchOf(ctx, tx, own.Namespace, own.Parent)
	if err != nil {
		return onto{}, err
	}
	if p.Ephemeral {
		return onto{parent: p, head: p.ForkCommit}, nil
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

// moved reports whether what the branch own is forked from moved since its fork: the head of a lasting parent, else
// the fork of the parent's branch or a commit on it since (a move of the parent's own fork shows as one of those).
func (o onto) moved(ctx context.Context, tx Tx, own domain.Branch) (bool, error) {
	if !o.parent.Ephemeral {
		return o.head != own.ForkCommit, nil
	}
	if o.head != own.ForkCommit {
		return true, nil
	}
	last, err := tx.LastCommit(ctx, o.parent.Name)
	return last > own.ForkSeq, err
}

// forkOn moves the fork of a branch to what it is forked from now (a rebase, ADR 0101).
func (o onto) forkOn(ctx context.Context, tx Tx, own *domain.Branch) error {
	if own.RebasedFrom == "" {
		own.RebasedFrom = own.ForkCommit
	}
	return o.forkOnHead(ctx, tx, own)
}

// forkOnHead forks a branch from what o is now: the head of a lasting branch, read when o has none, or the point of the
// branch of a change it is at.
func (o onto) forkOnHead(ctx context.Context, tx Tx, own *domain.Branch) error {
	if !o.parent.Ephemeral && o.head == "" {
		h, err := branchHeadHeader(ctx, tx, o.parent.Namespace, o.parent.Name)
		if err != nil {
			return err
		}
		o.head = h.ID
	}
	own.ForkCommit, own.ForkSeq = o.head, 0
	if o.parent.Ephemeral {
		var err error
		if own.ForkCommit, own.ForkSeq, err = forkPoint(ctx, tx, o.parent); err != nil {
			return err
		}
	}
	return nil
}

// errEphemeral refuses to read the head commit of the branch of a change, which holds none (ADR 0140): its state is
// read with branchVersions.
func errEphemeral(b domain.Branch) error {
	return fmt.Errorf("branch %s is the branch of a change: it holds no commit, its state is its fork with what committed on it: %w", b.Name, ErrInvalid)
}

// changeBranchHead is the state of the branch of a change as a commit no one stores (ADR 0140): no id, its fork as
// parent, the order of its last commit, its nodes when whole.
func changeBranchHead(ctx context.Context, tx Tx, br domain.Branch, whole bool) (domain.Commit, error) {
	b := domain.Commit{Namespace: br.Namespace, Branch: br.Name, ParentID: br.ForkCommit}
	var err error
	if b.Seq, err = tx.LastCommit(ctx, br.Name); err != nil || !whole {
		return b, err
	}
	b.Nodes, err = branchVersions(ctx, tx, br, nil)
	return b, err
}
