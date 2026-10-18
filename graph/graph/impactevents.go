package graph

import (
	"context"
	"fmt"
	"reflect"

	"github.com/zimwip/goap/contracts/domain"
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
		// the projection before the event is the fold of the log (they are the same, ADR 0029), kept by the transaction:
		// a batch of writes to one change reads none of its impacts again
		st, err := g.draftState(ctx, tx, e.Change)
		if err != nil {
			return err
		}
		// an event touches its own change impact, an adoption every one of them
		cur := st.impacts
		if e.Op != domain.ImpactAdopted {
			cur = nil
			if cn, ok := st.impact(e.Impact); ok {
				cur = []domain.ChangeImpact{cn}
			}
		}
		if e, err = appendImpactEvent(ctx, tx, e); err != nil {
			return err
		}
		next := domain.ApplyImpactEvent(cur, e)
		kept := make(map[domain.ChangeImpactID]bool, len(next))
		for _, n := range next {
			kept[n.ID] = true
		}
		// a change impact the event removes (a creation cancelled before it is accepted, an impact removed, ADR 0076)
		before := make(map[domain.ChangeImpactID]domain.ChangeImpact, len(cur))
		for _, cn := range cur {
			before[cn.ID] = cn
			if !kept[cn.ID] {
				if err := tx.DeleteChangeImpact(ctx, e.Change, cn.ID); err != nil {
					return err
				}
			}
		}
		// the superseded ones first: a node has one live change impact per flow (index change_impact_live)
		var changed []domain.ChangeImpact
		for _, cn := range next {
			if was, ok := before[cn.ID]; !ok || !reflect.DeepEqual(was, cn) {
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
