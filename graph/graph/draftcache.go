package graph

import (
	"container/list"
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/zimwip/goap/contracts/domain"
)

// The drafts of a change are derived from its log, not stored (ADR 0079 §2): the fold of the impact events
// (domain.FoldDrafts, one event at a time: ApplyImpactEvent then ApplyDraftEvent), which `created` and `checkedOut`
// open with a whole snapshot and the other events patch. A read folds from a cached copy: the cache holds, per change, the
// fold up to a log position (seq); a read asks the log for the impact events after that position (one indexed query, empty
// when nothing happened) and folds only those on top of it.
//
// The log is the truth and the cache a validated copy:
//   - an entry is immutable (a fold copies it first; the drafts it returns are deep copies), keyed by the change and
//     stamped with the seq of the last event it folds, so a second Graph on the same store (another process) or an
//     entry that is behind catches up from the shared log;
//   - an entry is published only once what it folds is committed: at once by a transaction that appended nothing to that
//     change's log, at its end by one that did. A transaction that wrote (the operations that emit an event then read the
//     draft) keeps its fold to itself (the guard's transaction, ownDrafts) and folds its newer events in place at the next
//     read, so that a batch of N writes to one change folds each event once and copies the fold once; it publishes its folds once it has committed (guardRepo.InTx), so a
//     rollback leaves no trace in the cache;
//   - the cache is bounded (LRU of DraftCacheSize changes) and guarded by a mutex.
//
// Writers of one change are serialized (ADR 0127: the lock of its tree, taken before the first append; the change_impact
// projection of ADR 0029 needs it too): with seqs handed out in commit order, "everything up to seq S" is a stable prefix.

// DefaultDraftCacheSize is the number of changes whose folded drafts the graph keeps in memory.
const DefaultDraftCacheSize = 128

// draftState is the fold of a change's impact log up to Seq: the change impacts (the drafts of a superseded impact go)
// and the drafts, indexed. Immutable once published; the transaction that keeps one (ownDrafts) folds its next events in
// place, so that an operation on a change of N impacts costs what its event touches, not N.
type draftState struct {
	change  domain.ChangeID
	seq     int64
	impacts []domain.ChangeImpact
	// at is the position of each change impact in impacts
	at map[domain.ChangeImpactID]int
	// drafts are the drafts of each change impact, one per flow
	drafts map[domain.ChangeImpactID][]domain.Draft
	// nodes are the change impacts that held a draft of each node (some may hold none any more)
	nodes map[domain.NodeID][]domain.ChangeImpactID
	// conflicts are the pending conflicts of the change impacts that have some (domain.PendingConflicts)
	conflicts map[domain.ChangeImpactID][]string
	// frozen: a reader of the transaction still reads it (draftReader), so its next fold is a copy
	frozen bool
}

// copy is a state the caller may fold in place (the values it holds are never modified, only replaced).
func (s *draftState) copy() *draftState {
	n := &draftState{change: s.change, seq: s.seq, impacts: slices.Clone(s.impacts), at: maps.Clone(s.at), drafts: maps.Clone(s.drafts), nodes: maps.Clone(s.nodes), conflicts: maps.Clone(s.conflicts)}
	if n.at == nil {
		n.at = map[domain.ChangeImpactID]int{}
	}
	if n.drafts == nil {
		n.drafts = map[domain.ChangeImpactID][]domain.Draft{}
	}
	if n.nodes == nil {
		n.nodes = map[domain.NodeID][]domain.ChangeImpactID{}
	}
	return n
}

// impact is the change impact of an id.
func (s *draftState) impact(id domain.ChangeImpactID) (domain.ChangeImpact, bool) {
	if i, ok := s.at[id]; ok {
		return s.impacts[i], true
	}
	return domain.ChangeImpact{}, false
}

// draft is the draft a change impact has on a flow (the value is shared: Clone it to edit it).
func (s *draftState) draft(id domain.ChangeImpactID, flow string) (domain.Draft, bool) {
	for _, d := range s.drafts[id] {
		if d.Flow == flow {
			return d, true
		}
	}
	return domain.Draft{}, false
}

// apply folds an event in place: domain.ApplyImpactEvent, then domain.ApplyDraftEvent and domain.ApplyConflictEvent.
func (s *draftState) apply(e domain.ImpactEvent) {
	i, ok := s.at[e.Impact]
	if !ok {
		i = -1
	}
	n := len(s.impacts)
	s.impacts = domain.ApplyImpactEventAt(s.impacts, i, e)
	switch {
	case len(s.impacts) < n: // a change impact removed
		clear(s.at)
		for j, cn := range s.impacts {
			s.at[cn.ID] = j
		}
	case len(s.impacts) > n:
		s.at[e.Impact] = len(s.impacts) - 1
	}
	switch e.Op {
	case domain.ImpactCreated, domain.ImpactCheckedOut, domain.ImpactTransitioned, domain.ImpactUpdated, domain.ImpactCancelled:
		rows := slices.Clone(s.drafts[e.Impact])
		j := slices.IndexFunc(rows, func(d domain.Draft) bool { return d.Flow == e.Flow })
		var cur *domain.Draft
		if j >= 0 {
			cur = &rows[j]
		}
		next := domain.ApplyDraftTo(cur, e)
		switch {
		case next == nil && j >= 0:
			rows = slices.Delete(rows, j, j+1)
		case next != nil && j >= 0:
			rows[j] = *next
		case next != nil:
			rows = append(rows, *next)
		}
		if next != nil && !slices.Contains(s.nodes[next.Node], e.Impact) {
			s.nodes[next.Node] = append(slices.Clone(s.nodes[next.Node]), e.Impact)
		}
		if len(rows) == 0 {
			delete(s.drafts, e.Impact)
		} else {
			s.drafts[e.Impact] = rows
		}
	case domain.ImpactWithdrawn, domain.ImpactLanded:
		delete(s.drafts, e.Impact)
	case domain.ImpactAdopted:
		// the adopted flow's drafts were installed on the main flow by the events that follow (see the adoption)
		for id, rows := range s.drafts {
			if rows = slices.DeleteFunc(slices.Clone(rows), func(d domain.Draft) bool { return d.Flow == e.Flow }); len(rows) == 0 {
				delete(s.drafts, id)
			} else {
				s.drafts[id] = rows
			}
		}
	}
	// a draft whose impact is gone, or superseded by an adopted flow, goes with it: an adoption may touch every impact,
	// another event its own only
	live := func(id domain.ChangeImpactID) bool {
		cn, ok := s.impact(id)
		return ok && !cn.Superseded
	}
	if e.Op == domain.ImpactAdopted {
		for id := range s.drafts {
			if !live(id) {
				delete(s.drafts, id)
			}
		}
	} else if !live(e.Impact) {
		delete(s.drafts, e.Impact)
	}
	if c := domain.ApplyConflictEvent(s.conflicts[e.Impact], e); len(c) > 0 {
		if s.conflicts == nil {
			s.conflicts = map[domain.ChangeImpactID][]string{}
		}
		s.conflicts[e.Impact] = c
	} else {
		delete(s.conflicts, e.Impact)
	}
	s.seq = int64(e.Seq)
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

// draftKeeper is a transaction holding the folds of the logs it appended to.
type draftKeeper interface {
	ownDrafts(domain.ChangeID) *draftState
	keepDrafts(*draftState)
	current(domain.ChangeID) *draftState
	readLog(*draftState)
}

func currentDrafts(tx Tx, id domain.ChangeID) *draftState {
	if k, ok := tx.(draftKeeper); ok {
		return k.current(id)
	}
	return nil
}

func readLog(tx Tx, st *draftState) {
	if k, ok := tx.(draftKeeper); ok {
		k.readLog(st)
	}
}

func ownDrafts(tx Tx, id domain.ChangeID) *draftState {
	if k, ok := tx.(draftKeeper); ok {
		return k.ownDrafts(id)
	}
	return nil
}

func keepDrafts(tx Tx, s *draftState) {
	if k, ok := tx.(draftKeeper); ok {
		k.keepDrafts(s)
	}
}

// draftState folds the impact log of a change up to its last event as the transaction sees it.
func (g *Graph) draftState(ctx context.Context, tx Tx, id domain.ChangeID) (*draftState, error) {
	if st := currentDrafts(tx, id); st != nil {
		return st, nil // nothing can follow it: no need to ask the log
	}
	st := g.draftStates.get(id)
	if own := ownDrafts(tx, id); own != nil && (st == nil || own.seq >= st.seq) {
		st = own
	}
	if st == nil {
		st = &draftState{change: id}
	}
	entries, err := tx.Log(ctx, domain.LogFilter{Change: id, Types: []string{domain.LogImpact + "."}, AfterSeq: st.seq})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		readLog(tx, st)
		return st, nil
	}
	events, err := decodeLog(entries, func(e *domain.ImpactEvent, l domain.LogEntry) { e.Seq = int(l.Seq) })
	if err != nil {
		return nil, err
	}
	// the transaction's own fold is folded on in place, any other copied first
	next := st
	if own := ownDrafts(tx, id); own == nil || own != st || st.frozen {
		next = st.copy()
	}
	for _, e := range events {
		next.apply(e)
	}
	g.draftStates.folded.Add(int64(len(events)))
	readLog(tx, next)
	if wroteLog(tx, id) {
		keepDrafts(tx, next)
	} else {
		g.draftStates.put(next, g.draftCacheSize())
	}
	return next, nil
}

func (g *Graph) draftCacheSize() int {
	if g.DraftCacheSize > 0 {
		return g.DraftCacheSize
	}
	return DefaultDraftCacheSize
}

// drafts lists the drafts of a change (every flow), by impact then flow, deep copies the caller may edit.
func (g *Graph) drafts(ctx context.Context, tx Tx, id domain.ChangeID) ([]domain.Draft, error) {
	st, err := g.draftState(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	var out []domain.Draft
	for _, rows := range st.drafts {
		for _, d := range rows {
			out = append(out, d.Clone())
		}
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
