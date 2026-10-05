package graph

import (
	"container/list"
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/zimwip/goap/pkg/domain"
)

// The drafts of a change are derived from its log, not stored (ADR 0079 §2): the fold of the impact events
// (domain.FoldDrafts, one event at a time: ApplyImpactEvent then ApplyDraftEvent), which `created` and `checkedOut`
// open with a whole snapshot and the other events patch. A read folds from a cached copy: the cache holds, per change, the
// fold up to a log position (seq); a read asks the log for the impact events after that position (one indexed query, empty
// when nothing happened) and folds only those on top of it.
//
// The log is the truth and the cache a validated copy:
//   - an entry is immutable (a fold builds a new one; the slices it returns are deep copies), keyed by the change and
//     stamped with the seq of the last event it folds, so a second Graph on the same store (another process) or an
//     entry that is behind catches up from the shared log;
//   - an entry is published only by a transaction that appended nothing to that change's log: what it read is committed.
//     A transaction that wrote (the operations that emit an event then read the draft) folds the cached prefix and its own
//     newer events directly and publishes nothing, so a rollback leaves no trace in the cache;
//   - the cache is bounded (LRU of DraftCacheSize changes) and guarded by a mutex.
//
// Writers of one change are assumed serialized (the change_impact projection ADR 0029 maintained already needed it):
// with seqs handed out in commit order, "everything up to seq S" is a stable prefix.

// DefaultDraftCacheSize is the number of changes whose folded drafts the graph keeps in memory.
const DefaultDraftCacheSize = 128

// draftState is the fold of a change's impact log up to Seq: the change impacts (the drafts of a superseded impact go)
// and the drafts. Immutable once published.
type draftState struct {
	change  domain.ChangeID
	seq     int64
	impacts []domain.ChangeImpact
	drafts  []domain.Draft
}

type draftCache struct {
	mu sync.Mutex
	ll *list.List // of *draftState, most recently used first
	m  map[domain.ChangeID]*list.Element
	// folded counts the events folded since the graph started (tests and benchmarks: a read after N events folds the new
	// ones only).
	folded atomic.Int64
}

func (c *draftCache) get(id domain.ChangeID) *draftState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[id]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*draftState)
	}
	return nil
}

// put keeps a state when it is ahead of the one held.
func (c *draftCache) put(s *draftState, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m, c.ll = map[domain.ChangeID]*list.Element{}, list.New()
	}
	if el, ok := c.m[s.change]; ok {
		if el.Value.(*draftState).seq < s.seq {
			el.Value = s
		}
		c.ll.MoveToFront(el)
		return
	}
	c.m[s.change] = c.ll.PushFront(s)
	for c.ll.Len() > size {
		last := c.ll.Back()
		delete(c.m, last.Value.(*draftState).change)
		c.ll.Remove(last)
	}
}

func (c *draftCache) drop(id domain.ChangeID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[id]; ok {
		c.ll.Remove(el)
		delete(c.m, id)
	}
}

// logWriter is what a transaction says of the log entries it appended.
type logWriter interface{ wroteLog(domain.ChangeID) bool }

// wroteLog reports whether a transaction appended to the log of a change; a transaction that cannot say is taken to have.
func wroteLog(tx Tx, id domain.ChangeID) bool {
	w, ok := tx.(logWriter)
	return !ok || w.wroteLog(id)
}

// draftState folds the impact log of a change up to its last event as the transaction sees it.
func (g *Graph) draftState(ctx context.Context, tx Tx, id domain.ChangeID) (*draftState, error) {
	prev := g.draftStates.get(id)
	st := prev
	if st == nil {
		st = &draftState{change: id}
	}
	entries, err := tx.Log(ctx, domain.LogFilter{Change: id, Types: []string{domain.LogImpact + "."}, AfterSeq: st.seq})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return st, nil
	}
	events, err := decodeLog(entries, func(e *domain.ImpactEvent, l domain.LogEntry) { e.Seq = int(l.Seq) })
	if err != nil {
		return nil, err
	}
	next := &draftState{change: id, seq: st.seq, impacts: st.impacts, drafts: st.drafts}
	for _, e := range events {
		next.impacts = domain.ApplyImpactEvent(next.impacts, e)
		next.drafts = domain.ApplyDraftEvent(next.drafts, e, next.impacts)
		next.seq = int64(e.Seq)
	}
	g.draftStates.folded.Add(int64(len(events)))
	if !wroteLog(tx, id) {
		size := g.DraftCacheSize
		if size <= 0 {
			size = DefaultDraftCacheSize
		}
		g.draftStates.put(next, size)
	}
	return next, nil
}

// drafts lists the drafts of a change (every flow), by impact then flow, deep copies the caller may edit.
func (g *Graph) drafts(ctx context.Context, tx Tx, id domain.ChangeID) ([]domain.Draft, error) {
	st, err := g.draftState(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Draft, len(st.drafts))
	for i, d := range st.drafts {
		out[i] = d.Clone()
	}
	slices.SortStableFunc(out, func(a, b domain.Draft) int {
		if a.Impact != b.Impact {
			if a.Impact < b.Impact {
				return -1
			}
			return 1
		}
		switch {
		case a.Flow < b.Flow:
			return -1
		case a.Flow > b.Flow:
			return 1
		}
		return 0
	})
	return out, nil
}
