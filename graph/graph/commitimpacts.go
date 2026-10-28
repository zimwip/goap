package graph

import (
	"context"

	"github.com/zimwip/goap/contracts/domain"
)

// A change impact is kept in the store only once it wrote a version (ADR 0174): before its commit, it is the log of
// its change and the view folded from it. Each path writing a version for a change writes, in the same transaction,
// the row of the impact behind it: from the version the change started from (or its previous commit of the node) to
// the version written, with the entry of the log that recorded the commit.

// commitImpactOf is the row of a version n written for the change impact cn (empty: none, a revert), its effect over
// the version it was checked out from and the position of the log entry recording it.
func commitImpactOf(n domain.Node, cn domain.ChangeImpact, effect domain.ImpactEffect, logSeq int64) CommitImpact {
	r := CommitImpact{Node: n.ID, Version: n.Version, Change: n.ChangeID, Impact: cn.ID, Key: n.Key, Type: n.Type, Intent: cn.Intent,
		Rationale: cn.Rationale, Reason: n.Reason, Effect: effect, LogSeq: logSeq, ProducedBy: cn.ProducedBy, Execution: cn.Execution,
		Via: cn.Via, CreatedAt: n.CreatedAt}
	if r.Intent == "" {
		r.Intent = domain.IntentModified
		if n.Reason == domain.ReasonCreate {
			r.Intent = domain.IntentCreated
		}
	}
	if r.Execution == "" {
		r.Execution = n.Execution
	}
	if len(n.Parents) > 0 {
		r.Pre = n.Parents[0]
	}
	if len(n.Parents) > 1 {
		r.MergedWith = n.Parents[1]
	}
	if len(cn.Reviews) > 0 {
		rv := cn.Reviews[len(cn.Reviews)-1]
		r.Review = &rv
	}
	return r
}

// putCommitImpacts writes the rows of a commit of change id on its branch: the versions of its impacts (cps, recorded
// by the committed events, in their order) and the reverts of the impacts withdrawn since their commit. A commit of
// reverts alone points at the last entry of the change's log.
func (g *Graph) putCommitImpacts(ctx context.Context, tx Tx, id domain.ChangeID, cps []cpost, committed []domain.ImpactEvent, reverted []domain.Node) error {
	if len(cps) == 0 && len(reverted) == 0 {
		return nil
	}
	var seq int64
	if len(committed) > 0 {
		seq = int64(committed[0].Seq)
	} else {
		st, err := g.draftState(ctx, tx, id)
		if err != nil {
			return err
		}
		seq = st.seq
	}
	rows := make([]CommitImpact, 0, len(cps)+len(reverted))
	for _, cp := range cps {
		rows = append(rows, commitImpactOf(cp.post, cp.cn, cp.step, seq))
	}
	for _, n := range reverted {
		r := commitImpactOf(n, domain.ChangeImpact{}, domain.ImpactEffect{}, seq)
		r.Rationale = n.Comment
		rows = append(rows, r)
	}
	return tx.PutCommitImpacts(ctx, rows)
}
