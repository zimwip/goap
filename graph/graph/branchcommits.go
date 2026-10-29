package graph

import (
	"context"
	"math"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// A branch reads commits, not versions (ADR 0176). It holds the versions of its own commits (node_version.branch) and
// the versions of the commits it took from another branch as they are, each from the order of the commit that took it
// (branch_commit.at): a merge takes the commits of the branch it merges (a fast-forward of the branch of a change, the
// source of a three-way merge), one row per commit, never one per version. The version of a node a branch holds is the
// last of its own commits or, when later, the greatest of the commits it took.

// ownVersion is the last version of a node committed on its own branch, with the order of its commit.
type ownVersion struct {
	CommittedVersion
	Committed int64
}

// joinedVersion is the greatest version of a node among the commits a branch took, with the order it took it at. It is
// read as it is: a revert taken from the branch of a change is a version like any other, a copy of the version it puts
// back (refreshed when the fork of that branch moves, branchstate.go), never left to a fork (CommittedVersion.Revert is
// false).
type joinedVersion struct {
	CommittedVersion
	At int64
}

// lastCommitted is what a branch holds of each node in a range of orders: its own last commit, or the version of the
// commits it took when taken later (a merge's own version wins a tie: a three-way merge writes the nodes both sides
// changed at the order it takes the commits of the source).
func lastCommitted(own map[domain.NodeID]ownVersion, joined map[domain.NodeID]joinedVersion) map[domain.NodeID]CommittedVersion {
	out := make(map[domain.NodeID]CommittedVersion, len(own)+len(joined))
	for id, o := range own {
		out[id] = o.CommittedVersion
	}
	for id, j := range joined {
		if o, ok := own[id]; !ok || j.At > o.Committed {
			out[id] = j.CommittedVersion
		}
	}
	return out
}

// takeCommits makes the branch target take the commits of the branch source it does not hold yet (ADR 0176): the
// commits written on source and the ones source took, the order of the commit being written on target. moved are the
// versions the merge moves target to (the head kept in memory follows them, heads.go).
func takeCommits(ctx context.Context, tx Tx, namespace, target, source string, change domain.ChangeID, moved []domain.NodeRef) error {
	headers, err := tx.BranchCommitHeaders(ctx, namespace, source)
	if err != nil {
		return err
	}
	var seqs []int64
	for _, h := range headers {
		if h.Seq > 0 {
			seqs = append(seqs, h.Seq)
		}
	}
	taken, err := tx.JoinedCommits(ctx, source, 0, math.MaxInt64)
	if err != nil {
		return err
	}
	seqs = append(seqs, taken...)
	slices.Sort(seqs)
	seqs = slices.Compact(seqs)
	if len(seqs) > 0 {
		if err := tx.JoinCommits(ctx, target, change, 0, seqs); err != nil {
			return err
		}
	}
	if gt, ok := unwrapGuard(tx); ok {
		for _, r := range moved {
			gt.took(target, r)
		}
	}
	return nil
}
