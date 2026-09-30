package graph

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
)

// PurgeChange removes a change that landed nothing, with its whole log (ADR 0037). It is the exception to the
// insert-only log of ADR 0030: what is removed was never part of the graph. A node version, once used, is never
// removed, so a change is refused (ErrConflict) as soon as anything of it is applied or built upon: it is applied,
// merged into, has sub-changes, or a baseline, a later version or a link of another change uses what it wrote.
//
// PurgePolicy lets a business rule keep a discarded change instead (to reuse its information later): it is asked
// first, and its error refuses the purge.
func (g *Graph) PurgeChange(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	var c domain.Change
	err := g.repo.InTx(ctx, func(tx Tx) (err error) {
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
		if g.PurgePolicy != nil {
			if err := g.PurgePolicy(ctx, c); err != nil {
				return err
			}
		}
		branch := ""
		own, isOwn, err := ownBranch(ctx, tx, c)
		if err != nil {
			return err
		}
		if isOwn {
			if own.Head != own.ForkBaseline {
				return fmt.Errorf("change %s: its branch received a merge: %w", id, ErrConflict)
			}
			branch = own.Name
		}
		return tx.DeleteChange(ctx, id, c.Namespace, branch)
	})
	return c, err
}
