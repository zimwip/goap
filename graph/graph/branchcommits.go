package graph

import (
	"context"
	"math"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// A branch reads commits, not versions (ADR 0176). It holds commits, one row each (branch_commit): its own, from
// their order (the first version, join or commit of the transaction marks it, guardTx.commitOn), and the commits it
// took from another branch as they are, from the order of the merge that took them (a fast-forward of the branch of a
// change, the source of a three-way merge), never one row per version. The version of a node a branch holds is the
// greatest of the commits it holds (sqlCommittedVersions): one way to read any branch.

// takeCommits makes the branch target take the commits of the branch source it does not hold yet (ADR 0176): the
// commits source holds (its own and the ones it took), from the order of the commit being written on target, recorded
// by the entry logSeq of the log of change (the versions are not rewritten). moved are the versions the merge moves
// target to (the head kept in memory follows them, heads.go).
func takeCommits(ctx context.Context, tx Tx, namespace, target, source string, change domain.ChangeID, logSeq int64, moved []domain.NodeRef) error {
	seqs, err := tx.JoinedCommits(ctx, source, 0, math.MaxInt64)
	if err != nil {
		return err
	}
	slices.Sort(seqs)
	seqs = slices.Compact(seqs)
	if len(seqs) > 0 {
		if err := tx.JoinCommits(ctx, target, change, 0, logSeq, seqs); err != nil {
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
