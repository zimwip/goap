package graph

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/events"
)

// PurgeChange removes a change that landed nothing, with its whole log (ADR 0037). It is the exception to the
// insert-only log of ADR 0030: what is removed was never part of the graph. A change is refused (ErrConflict) unless it
// is draft, active or abandoned, has produced no baseline (no commit), has no sub-change, and its own branch, when it has
// one, has not moved since it was forked. A change whose log records an execution (an entry of a stream the graph does
// not write itself: the journal of a run, the exchanges with a model) is refused too: that record is the trace of what an
// AI did on whose behalf (EU AI Act Art. 12, 19), kept with the change, which can still be abandoned.
func (g *Graph) PurgeChange(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	var c domain.Change
	err := g.changeTx(ctx, id, func(tx Tx) (err error) {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		if c.Status != domain.ChangeDraft && c.Status != domain.ChangeActive && c.Status != domain.ChangeAbandoned {
			return fmt.Errorf("change %s is %s, it landed in the graph: %w", id, c.Status, ErrConflict)
		}
		if c.ResultBaselineID != "" {
			return fmt.Errorf("change %s produced baseline %s: %w", id, c.ResultBaselineID, ErrConflict)
		}
		if subs, err := subChanges(ctx, tx, id); err != nil {
			return err
		} else if len(subs) > 0 {
			return fmt.Errorf("change %s has sub-changes: %w", id, ErrConflict)
		}
		if kept, err := recordsExecution(ctx, tx, id); err != nil {
			return err
		} else if kept != "" {
			return fmt.Errorf("change %s records an execution (%s entries in its log), kept for audit: abandon it instead: %w", id, kept, ErrConflict)
		}
		branch := ""
		own, isOwn, err := ownBranch(ctx, tx, c)
		if err != nil {
			return err
		}
		if isOwn {
			// the branch of a change holds no baseline (ADR 0140): what it committed, or a sub-change merged into it, is a
			// landing on it
			if last, err := tx.LastLanding(ctx, own.Name); err != nil {
				return err
			} else if last > 0 {
				return fmt.Errorf("change %s: its branch holds commits: %w", id, ErrConflict)
			}
			branch = own.Name
		}
		g.draftStates.drop(id)
		if err := tx.DeleteChange(ctx, id, c.Namespace, branch); err != nil {
			return err
		}
		g.raise(ctx, tx, events.ChangePurged, c, nil)
		return nil
	})
	return c, err
}

// recordsExecution names the first stream of the log of a change the graph did not write itself (empty: none).
func recordsExecution(ctx context.Context, tx Tx, id domain.ChangeID) (string, error) {
	counts, err := tx.LogCounts(ctx, domain.LogFilter{Change: id})
	if err != nil {
		return "", err
	}
	for typ, n := range counts {
		if n == 0 {
			continue
		}
		switch s := (domain.LogEntry{Type: typ}).Stream(); s {
		case domain.LogFact, domain.LogImpact, domain.LogChange:
		default:
			return s, nil
		}
	}
	return "", nil
}
