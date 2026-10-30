package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// The change impacts are event-sourced (ADR 0029): every operation appends an event to the change's log, folded into
// the view of the change (draftcache.go), which is all there is of an impact until its commit: the store keeps a
// change impact only once it wrote a version (ADR 0174, commitimpacts.go). The drafts of the nodes the change works
// on (ADR 0079) are folded from the same log.

func (g *Graph) caller(ctx context.Context) string {
	if g.Caller == nil {
		return ""
	}
	return g.Caller(ctx)
}

// author is the person behind an operation (ADR 0161 §3): the end user it is done for, else the caller.
func (g *Graph) author(ctx context.Context) string {
	if g.EndUser != nil {
		if u := g.EndUser(ctx); u != "" {
			return u
		}
	}
	return g.caller(ctx)
}

// emit appends events to the impact logs and folds each one into the view of its change. The events of one batched operation
// that follow each other with one envelope (domain.SameEntry: the verdicts of a review, the versions of a commit) are
// one log entry (ADR 0144).
func (g *Graph) emit(ctx context.Context, tx Tx, events ...domain.ImpactEvent) error {
	for i := range events {
		g.envelope(ctx, &events[i])
	}
	var s *impactStage
	if len(events) > 0 {
		if st, ok := ctx.Value(entriesKey{g}).(*impactStage); ok && st.change == events[0].Change {
			s = st
		}
	}
	for len(events) > 0 {
		n := 1
		for n < len(events) && domain.SameEntry(events[0], events[n]) {
			n++
		}
		if err := g.emitEntry(ctx, tx, events[:n], s); err != nil {
			return err
		}
		events = events[n:]
	}
	return nil
}

// entriesKey carries the stage an operation of many writes to one change logs its entries through (withEntries).
type entriesKey struct{ g *Graph }

// withEntries runs fn with the entries it logs to change id held, then appended in one statement (a batch of
// checkouts: one entry per event, as each would be logged alone, ADR 0144 aside). Nothing else may write the log of
// the change meanwhile.
func (g *Graph) withEntries(ctx context.Context, tx Tx, id domain.ChangeID, fn func(ctx context.Context) error) error {
	s := &impactStage{change: id, separate: true}
	if err := fn(context.WithValue(ctx, entriesKey{g}, s)); err != nil {
		return err
	}
	return g.flush(ctx, tx, s)
}

// envelope sets what an event leaves to its caller: its time and its author.
func (g *Graph) envelope(ctx context.Context, e *domain.ImpactEvent) {
	if e.At.IsZero() {
		e.At = g.now()
	}
	if e.By == "" {
		e.By = g.caller(ctx)
		if g.EndUser != nil {
			if u := g.EndUser(ctx); u != e.By {
				e.For = u // a service acting for a person: the edits are the person's (ADR 0161 §3)
			}
		}
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
	// separate: each group of events emitted is an entry of its own, as emit logs it (withEntries), the groups the
	// sizes of those entries, in order
	separate bool
	groups   []stagedGroup
}

type stagedGroup struct {
	id string
	n  int
}

// stage applies an event to the fold of its change now, and logs it when the stage is flushed.
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
	// seqs is the position of the entry of each staged event
	seqs := make([]int64, len(s.events))
	if s.separate {
		entries := make([]domain.LogEntry, len(s.groups))
		at := 0
		for i, gr := range s.groups {
			evs := s.events[at : at+gr.n]
			var err error
			if gr.n == 1 {
				entries[i], err = domain.ImpactEntry(evs[0])
			} else {
				entries[i], err = domain.ImpactBatchEntry(gr.id, evs)
			}
			if err != nil {
				return err
			}
			at += gr.n
		}
		entries, err := appendLogs(ctx, tx, entries)
		if err != nil {
			return err
		}
		at = 0
		for i, gr := range s.groups {
			for k := 0; k < gr.n; k++ {
				seqs[at+k] = entries[i].Seq
			}
			at += gr.n
		}
		s.groups = nil
	} else {
		entry, err := domain.ImpactBatchEntry(s.entry, s.events)
		if err != nil {
			return err
		}
		if entry, err = tx.AppendLog(ctx, entry); err != nil {
			return err
		}
		for i := range seqs {
			seqs[i] = entry.Seq
		}
	}
	// the fold holds the events already: it is now up to the entries
	st.seq = seqs[len(seqs)-1]
	base := len(st.events) - len(s.events)
	for i := range s.events {
		st.events[base+i].Seq = int(seqs[i])
	}
	for i, e := range s.events {
		if p, ok := st.phases.get(e.Impact); ok && e.StartsDraft() && e.Flow == "" {
			p.Seq = seqs[i]
			st.phases.set(e.Impact, p)
		}
	}
	readLog(tx, st)
	s.events = nil
	return nil
}

// committedAt stamps the versions a commit wrote with the position of its committed entry (guardTx.committedAt).
func committedAt(ctx context.Context, tx Tx, seq int64, events []domain.ImpactEvent) error {
	var named []domain.NodeRef
	for _, e := range events {
		if e.Op == domain.ImpactCommitted && e.Committed != nil {
			named = append(named, *e.Committed)
		}
	}
	if len(named) == 0 {
		return nil
	}
	if gt, ok := unwrapGuard(tx); ok {
		return gt.committedAt(ctx, events[0].Change, seq, named)
	}
	return nil
}

// emitEntry appends one log entry carrying the events (SameEntry) and applies them to the fold of the change; staged
// (s set), it applies them and leaves the entry to flush.
func (g *Graph) emitEntry(ctx context.Context, tx Tx, events []domain.ImpactEvent, s *impactStage) error {
	change := events[0].Change
	id := g.newID()
	if s != nil && !s.separate {
		id = s.entry
	}
	for i := range events {
		e := &events[i]
		switch {
		case s != nil && !s.separate:
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
	// the fold of the log before the events, kept by the transaction: a batch of writes to one change reads none of its
	// impacts again
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
		if err := committedAt(ctx, tx, entry.Seq, events); err != nil {
			return err
		}
	}
	fold := holds && (s != nil || int64(events[0].Seq) > st.seq)
	next := st
	if fold {
		if own := ownDrafts(tx, change); own != st || st.frozen {
			next = st.copy()
		}
	}
	if fold {
		for _, e := range events {
			next.apply(e)
		}
		keepDrafts(tx, next)
		readLog(tx, next)
	}
	if s != nil {
		s.events, s.st = append(s.events, events...), next
		if s.separate {
			s.groups = append(s.groups, stagedGroup{id: id, n: len(events)})
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
		out, err = g.foldedEvents(ctx, tx, id)
		out = slices.Clone(out)
		return err
	})
	return
}
