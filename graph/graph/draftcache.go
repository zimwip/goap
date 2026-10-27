package graph

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/lib/cache"
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
	// items are the items of the change, the facts of its log in their order (itemsOf): folded with the impact events,
	// so that reading a change does not decode its facts again
	items []domain.ChangeItem
	// events are the impact events folded, in the order of the log: what a flow other than the main one sees, and what
	// stale runs left, are read over them (domain.ImpactsSeenBy, DraftSeenBy) with no read of the log. Never modified.
	events []domain.ImpactEvent
	// log holds events, shared with the folds copied from this one, so a copy does not copy them again (eventLog)
	log *eventLog
	// phases are the state each change impact started its draft on the main flow in (ADR 0058 §4, 0079), folded with the
	// moves of the change: before its first move an impact is in the state that move leaves (phase.Before)
	phases map[domain.ChangeImpactID]phase
	// moved: the change took a move (a transition fact of the main flow); first is the state its first move left, state
	// the one its last move entered
	moved        bool
	first, state string
	// moveSeq is the position of each move in the log
	moveSeq map[domain.ItemID]int64
	// frozen: a reader of the transaction still reads it (draftReader), so its next fold is a copy
	frozen bool
	// bytes estimates the memory the view holds (eventSize, itemSize): what the cache counts against its bound
	bytes int64
	// top is, plus one, a last position of the whole log of the change at which the fold was known to hold every impact
	// event and fact (0: none known): read under the lock of the change, so no writer of those has written since
	// (ADR 0127). A transaction holding the lock that finds the log still there reads none of it (guardTx.topOf).
	top atomic.Int64
}

// copy is a state the caller may fold in place (the values it holds are never modified, only replaced).
func (s *draftState) copy() *draftState {
	n := &draftState{change: s.change, seq: s.seq, impacts: slices.Clone(s.impacts), at: maps.Clone(s.at), drafts: maps.Clone(s.drafts), nodes: maps.Clone(s.nodes), conflicts: maps.Clone(s.conflicts), items: slices.Clip(s.items), events: s.events, log: s.log,
		phases: maps.Clone(s.phases), moved: s.moved, first: s.first, state: s.state, moveSeq: maps.Clone(s.moveSeq), bytes: s.bytes}
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

// eventLog is the array of impact events the folds copied from one another share: the elements a fold holds are never
// moved, a fold appends in place while it holds the whole log, else it starts a log of its own (another fold appended
// to this one since they were copied), so a copy of a fold costs nothing and each event is copied once.
type eventLog struct {
	mu  sync.Mutex
	evs []domain.ImpactEvent
}

// addEvent appends an event to the events of the fold.
func (s *draftState) addEvent(e domain.ImpactEvent) {
	l := s.log
	if l != nil {
		l.mu.Lock()
		if len(l.evs) != len(s.events) {
			l.mu.Unlock()
			l = nil
		}
	}
	if l == nil {
		l = &eventLog{evs: slices.Clip(s.events)}
		s.log = l
		l.mu.Lock()
	}
	l.evs = append(l.evs, e)
	s.events = l.evs
	l.mu.Unlock()
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

// fact folds an item of the change, at its position in the log, in place: a move of the main flow moves the phase.
func (s *draftState) fact(it domain.ChangeItem, seq int64) {
	s.items = append(s.items, it)
	s.bytes += itemSize(it)
	if it.Type != domain.TypeTransition || it.Flow != "" {
		return
	}
	str := func(k string) string { v, _ := it.Data[k].(string); return v }
	if !s.moved {
		s.moved, s.first = true, str("from")
	}
	s.state = str("to")
	if s.moveSeq == nil {
		s.moveSeq = map[domain.ItemID]int64{}
	}
	s.moveSeq[it.ID] = seq
	s.seq = seq
}

// phaseOf is the state a change impact started its draft in; false when the change never moved or the impact started
// no draft on the main flow.
func (s *draftState) phaseOf(id domain.ChangeImpactID) (phase, bool) {
	p, ok := s.phases[id]
	if !ok || !s.moved {
		return phase{}, false
	}
	if p.Before {
		p.State = s.first
	}
	return p, true
}

// apply folds an event in place: domain.ApplyImpactEvent, then domain.ApplyDraftEvent and domain.ApplyConflictEvent.
func (s *draftState) apply(e domain.ImpactEvent) {
	s.addEvent(e)
	s.bytes += eventSize(e)
	if e.StartsDraft() && e.Flow == "" {
		if s.phases == nil {
			s.phases = map[domain.ChangeImpactID]phase{}
		}
		s.phases[e.Impact] = phase{State: s.state, Seq: int64(e.Seq), Before: !s.moved}
	}
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
	case domain.ImpactWithdrawn, domain.ImpactCommitted:
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

// draftCache keeps the views of the changes between calls in the one cache of the platform (lib/cache): bounded in
// memory (the estimated bytes of the views, the least recently read dropped first; a view larger than the bound is not
// kept) and in time (a view not read for the TTL goes).
type draftCache struct {
	views *cache.Cache[domain.ChangeID, *draftState]
	// folded counts the events folded since the graph started (tests and benchmarks: a read after N events folds the new
	// ones only).
	folded atomic.Int64
}

func newDraftCache(bounds func() cache.Bounds) draftCache {
	return draftCache{views: cache.New(bounds, func(_ domain.ChangeID, s *draftState) int64 { return s.bytes })}
}

func (c *draftCache) get(id domain.ChangeID) *draftState {
	s, _ := c.views.Get(id)
	return s
}

// put keeps a view when it is ahead of the one held.
func (c *draftCache) put(s *draftState) {
	c.views.Update(s.change, func(old *draftState, ok bool) (*draftState, bool) { return s, !ok || old.seq < s.seq })
}

func (c *draftCache) drop(id domain.ChangeID) { c.views.Remove(id) }

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

// lockedTop is the last position of the log of a change when the transaction took its lock, when it knows it and
// has not appended to that log since.
func lockedTop(tx Tx, id domain.ChangeID) (int64, bool) {
	if k, ok := tx.(interface {
		topOf(domain.ChangeID) (int64, bool)
	}); ok {
		return k.topOf(id)
	}
	return 0, false
}

// markWhole records that the transaction read the log of a change whole under its lock.
func markWhole(tx Tx, id domain.ChangeID) {
	if k, ok := tx.(interface{ wholeLog(domain.ChangeID) }); ok {
		k.wholeLog(id)
	}
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

// draftState is the view of a change (ADR 0153): the fold of its log up to its last impact event or fact as the
// transaction sees it, refused with ErrBehind when it does not reach the position the caller asks for
// (graphapi.AtLeast).
func (g *Graph) draftState(ctx context.Context, tx Tx, id domain.ChangeID) (*draftState, error) {
	st, err := g.foldDrafts(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if want := graphapi.AtLeastOf(ctx, id); want > st.seq {
		return nil, fmt.Errorf("change %s: its log is read up to %d, %d asked for: %w", id, st.seq, want, ErrBehind)
	}
	return st, nil
}

// foldDrafts folds the impact log of a change up to its last event as the transaction sees it.
func (g *Graph) foldDrafts(ctx context.Context, tx Tx, id domain.ChangeID) (*draftState, error) {
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
	top, whole := lockedTop(tx, id)
	if whole {
		markWhole(tx, id)
	}
	if whole && st.top.Load() == top+1 {
		readLog(tx, st) // the log has not moved since the fold was whole
		return st, nil
	}
	entries, err := tx.Log(ctx, domain.LogFilter{Change: id, Types: []string{domain.LogImpact + ".", domain.LogFact + "."}, AfterSeq: st.seq})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		if whole {
			st.top.Store(top + 1)
		}
		readLog(tx, st)
		return st, nil
	}
	// the transaction's own fold is folded on in place, any other copied first
	next := st
	if own := ownDrafts(tx, id); own == nil || own != st || st.frozen {
		next = st.copy()
	}
	n, err := next.foldEntries(ctx, tx, entries)
	if err != nil {
		return nil, err
	}
	next.seq = entries[len(entries)-1].Seq
	if whole {
		next.top.Store(max(top, next.seq) + 1)
	}
	g.draftStates.folded.Add(int64(n))
	readLog(tx, next)
	if wroteLog(tx, id) {
		keepDrafts(tx, next)
	} else {
		g.draftStates.put(next)
	}
	return next, nil
}

// foldEntries folds impact and fact entries of the log in place, in their order (a draft started after a move is in
// the state the move entered), and returns the number of impact events folded.
func (s *draftState) foldEntries(ctx context.Context, tx Tx, entries []domain.LogEntry) (int, error) {
	var impacts, facts []domain.LogEntry
	for _, l := range entries {
		if strings.HasPrefix(l.Type, domain.LogFact+".") {
			facts = append(facts, l)
		} else {
			impacts = append(impacts, l)
		}
	}
	events, err := decodeImpacts(ctx, tx, impacts)
	if err != nil {
		return 0, err
	}
	items, err := itemsOf(facts)
	if err != nil {
		return 0, err
	}
	f, k := 0, 0
	for _, l := range entries {
		if strings.HasPrefix(l.Type, domain.LogFact+".") {
			s.fact(items[f], l.Seq)
			f++
			continue
		}
		for ; k < len(events) && int64(events[k].Seq) <= l.Seq; k++ {
			s.apply(events[k])
		}
	}
	for ; k < len(events); k++ {
		s.apply(events[k])
	}
	if len(entries) > 0 {
		s.seq = entries[len(entries)-1].Seq
	}
	return len(events), nil
}

// draftsOf is the lookup of the drafts of a change in its view: the draft of an impact on a flow (a copy), nil when it
// has none.
func (g *Graph) draftsOf(ctx context.Context, tx Tx, id domain.ChangeID) (func(domain.ChangeImpactID, string) *domain.Draft, error) {
	st, err := g.draftState(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return func(impact domain.ChangeImpactID, flow string) *domain.Draft {
		d, ok := st.draft(impact, flow)
		if !ok {
			return nil
		}
		d = d.Clone()
		return &d
	}, nil
}

// foldedEvents is the impact log of a change as its fold holds it (shared: never modified).
func (g *Graph) foldedEvents(ctx context.Context, tx Tx, id domain.ChangeID) ([]domain.ImpactEvent, error) {
	st, err := g.draftState(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return slices.Clip(st.events), nil
}

// drafts lists the drafts of a change (every flow), by impact then flow, deep copies the caller may edit.
// ownDraftOf is the draft a change impact has on a flow (a copy), nil when it has none.
func (g *Graph) ownDraftOf(ctx context.Context, tx Tx, id domain.ChangeID, impact domain.ChangeImpactID, flow string) (*domain.Draft, error) {
	st, err := g.draftState(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	d, ok := st.draft(impact, flow)
	if !ok {
		return nil, nil
	}
	d = d.Clone()
	return &d, nil
}

// nodeDrafts are the drafts of the change impacts that held a draft of a node (copies), in the order of drafts.
func (g *Graph) nodeDrafts(ctx context.Context, tx Tx, id domain.ChangeID, node domain.NodeID) ([]domain.Draft, error) {
	st, err := g.draftState(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	impacts := slices.Clone(st.nodes[node])
	slices.Sort(impacts)
	var out []domain.Draft
	for _, imp := range slices.Compact(impacts) {
		rows := slices.Clone(st.drafts[imp])
		slices.SortStableFunc(rows, func(a, b domain.Draft) int { return strings.Compare(a.Flow, b.Flow) })
		for _, d := range rows {
			out = append(out, d.Clone())
		}
	}
	return out, nil
}

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
