package graph

import (
	"context"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// A change is read as a view of its log (ADR 0153). The head view is the fold the operations share (draftState): every
// operation takes it once at the start of its transaction, computes on it, appends its events and folds them into it
// synchronously (emitEntry), so the view it leaves is at the position its writes reached, which the caller's watermark
// records (graphapi.WithWatermark) and a later read may require (graphapi.AtLeast). A view bounded at a position is the
// fold of the log up to it (ChangeAt): the change as it stood then.

// ChangeAt reads a change as its log stood at a position: its impacts, its items and its state up to seq (the header
// otherwise as it is now). A position at or past the head is the head.
func (g *Graph) ChangeAt(ctx context.Context, id domain.ChangeID, seq int64) (c domain.Change, err error) {
	if seq <= 0 {
		return c, invalidf("change %s: a position of its log is above 0", id)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		if seq >= c.Seq {
			return nil
		}
		entries, err := tx.Log(ctx, domain.LogFilter{Change: id, Types: []string{domain.LogImpact + ".", domain.LogFact + "."}})
		if err != nil {
			return err
		}
		n := len(entries)
		for n > 0 && entries[n-1].Seq > seq {
			n--
		}
		st := (&draftState{change: id}).copy()
		if _, err := st.foldEntries(ctx, tx, entries[:n]); err != nil {
			return err
		}
		if moves := c.StateMoves(); len(moves) > 0 {
			c.State = moves[0].From // before its first move
		}
		if st.moved {
			c.State = st.state
		}
		c.Items, c.Nodes, c.Seq = slices.Clone(st.items), slices.Clone(st.impacts), st.seq
		return nil
	})
	return
}
