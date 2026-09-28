package graph

import (
	"context"
	"fmt"
	"reflect"

	"github.com/zimwip/goap/pkg/domain"
)

// The change impacts are event-sourced (ADR 0029): every operation appends an event to the change's log, and the
// change_impact table is the projection of that log, updated in the same transaction. Nothing else writes it.

func (g *Graph) caller(ctx context.Context) string {
	if g.Caller == nil {
		return ""
	}
	return g.Caller(ctx)
}

// emit appends events to the impact logs and applies each one to the projection.
func (g *Graph) emit(ctx context.Context, tx Tx, events ...domain.ImpactEvent) error {
	for _, e := range events {
		if e.ID == "" {
			e.ID = g.newID()
		}
		if e.At.IsZero() {
			e.At = g.now()
		}
		if e.By == "" {
			e.By = g.caller(ctx)
		}
		if err := e.Validate(); err != nil {
			return fmt.Errorf("change %s: %v: %w", e.Change, err, ErrInvalid)
		}
		e, err := tx.AppendChangeEvent(ctx, e)
		if err != nil {
			return err
		}
		cur, err := tx.ChangeImpacts(ctx, e.Change)
		if err != nil {
			return err
		}
		next := domain.ApplyImpactEvent(cur, e)
		// the superseded ones first: a node has one live change impact per flow (index change_impact_live)
		var changed []domain.ChangeImpact
		for i, cn := range next {
			if i >= len(cur) || !reflect.DeepEqual(cur[i], cn) {
				changed = append(changed, cn)
			}
		}
		for _, first := range []bool{true, false} {
			for _, cn := range changed {
				if cn.Superseded == first {
					if err := tx.PutChangeImpact(ctx, e.Change, cn); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// ChangeEvents returns the impact log of a change (ADR 0029).
func (g *Graph) ChangeEvents(ctx context.Context, id domain.ChangeID) (out []domain.ImpactEvent, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if _, err := tx.Change(ctx, id); err != nil {
			return err
		}
		out, err = tx.ChangeEvents(ctx, id)
		return err
	})
	return
}

// MigrateImpactEvents gives the change impacts recorded before the log (ADR 0029 §6) one imported event each, so
// every change can be replayed. Idempotent: a change with events is left as is. It returns the changes migrated.
func (g *Graph) MigrateImpactEvents(ctx context.Context) (n int, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		cs, err := tx.Changes(ctx)
		if err != nil {
			return err
		}
		for _, c := range cs {
			impacts, err := tx.ChangeImpacts(ctx, c.ID)
			if err != nil || len(impacts) == 0 {
				if err != nil {
					return err
				}
				continue
			}
			events, err := tx.ChangeEvents(ctx, c.ID)
			if err != nil {
				return err
			}
			if len(events) > 0 {
				continue
			}
			for _, cn := range impacts {
				cn := cn
				e := domain.ImpactEvent{ID: g.newID(), Change: c.ID, Impact: cn.ID, Op: domain.ImpactImported, Flow: cn.Flow,
					Execution: cn.Execution, By: "graph.migration", At: cn.CreatedAt, State: &cn}
				if _, err := tx.AppendChangeEvent(ctx, e); err != nil {
					return err
				}
			}
			n++
		}
		return nil
	})
	return
}
