package graph

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// The change impacts are event-sourced (ADR 0029): every operation appends an event to the change's log, and the
// change_impact table is the projection of that log, updated in the same transaction. Nothing else writes it. The
// drafts of the nodes the change works on (ADR 0079) are no projection: they are folded from the same log when read
// (draftcache.go).

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
		e, err := appendImpactEvent(ctx, tx, e)
		if err != nil {
			return err
		}
		cur, err := tx.ChangeImpacts(ctx, e.Change)
		if err != nil {
			return err
		}
		next := domain.ApplyImpactEvent(cur, e)
		// a change impact the event removes (a creation cancelled before it is accepted, an impact removed, ADR 0076)
		for _, cn := range cur {
			if !slices.ContainsFunc(next, func(n domain.ChangeImpact) bool { return n.ID == cn.ID }) {
				if err := tx.DeleteChangeImpact(ctx, e.Change, cn.ID); err != nil {
					return err
				}
			}
		}
		cur = slices.DeleteFunc(slices.Clone(cur), func(c domain.ChangeImpact) bool {
			return !slices.ContainsFunc(next, func(n domain.ChangeImpact) bool { return n.ID == c.ID })
		})
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
		out, err = impactEvents(ctx, tx, id)
		return err
	})
	return
}
