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

// emit appends events to the impact logs and applies each one to the projection. The events of one batched operation
// that follow each other with one envelope (domain.SameEntry: the verdicts of a review, the versions of a commit) are
// one log entry (ADR 0144).
func (g *Graph) emit(ctx context.Context, tx Tx, events ...domain.ImpactEvent) error {
	for i := range events {
		g.envelope(ctx, &events[i])
	}
	for len(events) > 0 {
		n := 1
		for n < len(events) && domain.SameEntry(events[0], events[n]) {
			n++
		}
		if err := g.emitEntry(ctx, tx, events[:n], nil); err != nil {
			return err
		}
		events = events[n:]
	}
	return nil
}

// envelope sets what an event leaves to its caller: its time and its author.
func (g *Graph) envelope(ctx context.Context, e *domain.ImpactEvent) {
	if e.At.IsZero() {
		e.At = g.now()
	}
	if e.By == "" {
		e.By = g.caller(ctx)
	}
}

// impactStage is the events of an operation applied to the change before they are logged, as one entry when the
// operation is over (flush): the verdicts of a review, which judge each other (the origins of an acceptance settled by
// the same review) and commit as one entry (ADR 0144).
type impactStage struct {
	change domain.ChangeID
	entry  string
	events []domain.ImpactEvent
	// st is the fold of the change holding the staged events: nothing else may write the log of the change meanwhile
	st *draftState
}

// stage applies an event to the projection and to the fold of its change now, and logs it when the stage is flushed.
func (g *Graph) stage(ctx context.Context, tx Tx, s *impactStage, e domain.ImpactEvent) error {
	g.envelope(ctx, &e)
	if s.entry == "" {
		s.change, s.entry = e.Change, g.newID()
	}
	if len(s.events) > 0 {
		if !domain.SameEntry(s.events[0], e) {
			return fmt.Errorf("change %s: event %s of %s is not staged with %s of %s", e.Change, e.Op, e.Impact, s.events[0].Op, s.events[0].Impact)
		}
		e.At = s.events[0].At
	}
	return g.emitEntry(ctx, tx, []domain.ImpactEvent{e}, s)
}

// flush logs the staged events as one entry.
func (g *Graph) flush(ctx context.Context, tx Tx, s *impactStage) error {
	if len(s.events) == 0 {
		return nil
	}
	st := currentDrafts(tx, s.change)
	if st == nil || st != s.st {
		return fmt.Errorf("change %s: its log was written while events were staged", s.change)
	}
	entry, err := domain.ImpactBatchEntry(s.entry, s.events)
	if err != nil {
		return err
	}
	if entry, err = tx.AppendLog(ctx, entry); err != nil {
		return err
	}
	// the fold holds the events already: it is now up to the entry
	st.seq = entry.Seq
	readLog(tx, st)
	s.events = nil
	return nil
}

// emitEntry appends one log entry carrying the events (SameEntry) and applies them to the projection and to the fold of
// the change; staged (s set), it applies them and leaves the entry to flush.
func (g *Graph) emitEntry(ctx context.Context, tx Tx, events []domain.ImpactEvent, s *impactStage) error {
	change := events[0].Change
	id := g.newID()
	if s != nil {
		id = s.entry
	}
	for i := range events {
		e := &events[i]
		switch {
		case s != nil:
			e.ID = domain.PartID(id, len(s.events)+i)
		case len(events) > 1:
			e.ID = domain.PartID(id, i)
		case e.ID == "":
			e.ID = id
		}
		e.At = events[0].At
		if err := e.Validate(); err != nil {
			return fmt.Errorf("change %s: %v: %w", e.Change, err, ErrInvalid)
		}
	}
	// the projection before the events is the fold of the log (they are the same, ADR 0029), kept by the transaction:
	// a batch of writes to one change reads none of its impacts again
	st, err := g.draftState(ctx, tx, change)
	if err != nil {
		return err
	}
	// a fold holding the whole log under the lock of the change: the events are folded into it once appended, so the
	// next event of the operation reads no log
	holds := currentDrafts(tx, change) == st
	if s != nil {
		if !holds || (s.st != nil && s.st != st) {
			return fmt.Errorf("change %s: events are staged on the fold of the change, which its log has left", change)
		}
		for i := range events {
			events[i].Seq = int(st.seq)
		}
	} else {
		var entry domain.LogEntry
		if len(events) == 1 {
			entry, err = domain.ImpactEntry(events[0])
		} else {
			entry, err = domain.ImpactBatchEntry(id, events)
		}
		if err != nil {
			return err
		}
		if entry, err = tx.AppendLog(ctx, entry); err != nil {
			return err
		}
		for i := range events {
			events[i].Seq = int(entry.Seq)
		}
	}
	fold := holds && (s != nil || int64(events[0].Seq) > st.seq)
	next := st
	if fold {
		if own := ownDrafts(tx, change); own != st || st.frozen {
			next = st.copy()
		}
	}
	for _, e := range events {
		if err := g.project(ctx, tx, next, e); err != nil {
			return err
		}
		if fold {
			next.apply(e)
		}
	}
	if fold {
		keepDrafts(tx, next)
		readLog(tx, next)
	}
	if s != nil {
		s.events, s.st = append(s.events, events...), next
	}
	return nil
}

// project applies an event to the change_impact projection, st being the fold before it.
func (g *Graph) project(ctx context.Context, tx Tx, st *draftState, e domain.ImpactEvent) error {
	// an event touches its own change impact, an adoption every one of them
	cur := st.impacts
	if e.Op != domain.ImpactAdopted {
		cur = nil
		if cn, ok := st.impact(e.Impact); ok {
			cur = []domain.ChangeImpact{cn}
		}
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
