package graph

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
)

// Record appends records to the execution journal of their changes (ids are
// assigned when empty).
func (g *Graph) Record(ctx context.Context, recs []domain.ExecutionRecord) error {
	return g.repo.InTx(ctx, func(tx Tx) error {
		for _, r := range recs {
			if r.ChangeID == "" || r.ProcessID == "" || r.Kind == "" {
				return fmt.Errorf("execution record needs a change, a process and a kind: %w", ErrInvalid)
			}
			if r.ID == "" {
				r.ID = g.newID()
			}
			if r.StartedAt.IsZero() {
				r.StartedAt = g.now()
			}
			if err := tx.PutExecution(ctx, r); err != nil {
				return err
			}
		}
		return nil
	})
}

// Journal returns the execution records of a change and / or of processes.
func (g *Graph) Journal(ctx context.Context, f domain.ExecutionFilter) (rs []domain.ExecutionRecord, err error) {
	if f.ChangeID == "" && len(f.ProcessIDs) == 0 {
		return nil, fmt.Errorf("journal filter needs a change or processes: %w", ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error { rs, err = tx.Executions(ctx, f); return err })
	return
}
