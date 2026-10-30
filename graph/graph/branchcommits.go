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
	// the commits the target holds already are left out: the ones the source took of it by a rebase (ADR 0177)
	seqs, err := tx.JoinedCommits(ctx, namespace, source, target, 0, math.MaxInt64)
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

// takeCommitsOf is TakeCommits from the reads it stands for, for the stores where a read costs no round trip.
func takeCommitsOf(ctx context.Context, tx interface {
	JoinedCommits(ctx context.Context, namespace, branch, except string, after, upTo int64) ([]int64, error)
	NextCommit(ctx context.Context) (int64, error)
	JoinCommits(ctx context.Context, branch string, change domain.ChangeID, at, logSeq int64, seqs []int64) error
}, branch string, change domain.ChangeID, logSeq int64, namespace, from string, after, upTo int64) error {
	seqs, err := tx.JoinedCommits(ctx, namespace, from, "", after, upTo)
	if err != nil || len(seqs) == 0 {
		return err
	}
	at, err := tx.NextCommit(ctx)
	if err != nil {
		return err
	}
	return tx.JoinCommits(ctx, branch, change, at, logSeq, seqs)
}

// takeRows makes the branch of a change take commits of the branch it is forked from by a rebase (ADR 0177): rows of
// the commits, at a new order of the branch that no commit of its own takes up (the rebase writes no commit), recorded
// by the entry logSeq of the log of change.
func takeRows(ctx context.Context, tx Tx, branch string, change domain.ChangeID, logSeq int64, seqs []int64) error {
	if len(seqs) == 0 {
		return nil
	}
	if gt, ok := unwrapGuard(tx); ok {
		return gt.takeRows(ctx, branch, change, logSeq, seqs)
	}
	at, err := tx.NextCommit(ctx)
	if err != nil {
		return err
	}
	return tx.JoinCommits(ctx, branch, change, at, logSeq, seqs)
}
