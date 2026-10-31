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
	change domain.ChangeID
	seq    int64
	// impacts are kept in blocks, the containers keyed by impact or node in shards, which the copies of the view share
	// until they write (impactBlocks, cowMap): a write to a change of N impacts copies what it touches, not N
	impacts impactBlocks
	// at is the position of each change impact in impacts
	at cowMap[domain.ChangeImpactID, int]
	// byNode and byKey are the positions of the change impacts naming each node (Pre or Post) and of each key, in order;
	// odd counts the impacts superseded or of a flow: what the operations look for in the impacts (domain.ImpactLookup)
	byNode cowMap[domain.NodeID, []int]
	byKey  cowMap[string, []int]
	odd    int
	// drafts are the drafts of each change impact, one per flow
	drafts cowMap[domain.ChangeImpactID, []domain.Draft]
	// nodes are the change impacts that held a draft of each node (some may hold none any more)
	nodes cowMap[domain.NodeID, []domain.ChangeImpactID]
	// conflicts are the pending conflicts of the change impacts that have some (domain.PendingConflicts)
	conflicts cowMap[domain.ChangeImpactID, []string]
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
	phases cowMap[domain.ChangeImpactID, phase]
	// moved: the change took a move (a transition fact of the main flow); first is the state its first move left, state
	// the one its last move entered
	moved        bool
	first, state string
	// moveSeq is the position of each move in the log
	moveSeq map[domain.ItemID]int64
	// frozen: a reader of the transaction still reads it (draftReader, the impacts of a change read), so its next fold
	// is a copy
	frozen bool
	// sharedMoves: moveSeq is shared with the fold it was copied from, cloned at its first write
	sharedMoves bool
	// bytes estimates the memory the view holds (eventSize, itemSize): what the cache counts against its bound
	bytes int64
	// top is, plus one, a last position of the whole log of the change at which the fold was known to hold every impact
	// event and fact (0: none known): read under the lock of the change, so no writer of those has written since
	// (ADR 0127). A transaction holding the lock that finds the log still there reads none of it (guardTx.topOf).
	top atomic.Int64
}

// copy is a state the caller may fold in place (the values it holds are never modified, only replaced). It shares the
// containers of s, which copy what they write (impactBlocks, cowMap): a transaction that touches one impact of a change
// of N copies what that write touches, not the whole view.
func (s *draftState) copy() *draftState {
	return &draftState{change: s.change, seq: s.seq, impacts: s.impacts.share(), at: s.at.share(), byNode: s.byNode.share(), byKey: s.byKey.share(),
		odd: s.odd, drafts: s.drafts.share(), nodes: s.nodes.share(),
		conflicts: s.conflicts.share(), items: slices.Clip(s.items), events: s.events, log: s.log, phases: s.phases.share(), moved: s.moved,
		first: s.first, state: s.state, moveSeq: s.moveSeq, bytes: s.bytes, sharedMoves: true}
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
	if i, ok := s.at.get(id); ok {
		return s.impacts.at(i), true
	}
	return domain.ChangeImpact{}, false
}

// draft is the draft a change impact has on a flow (the value is shared: Clone it to edit it).
func (s *draftState) draft(id domain.ChangeImpactID, flow string) (domain.Draft, bool) {
	rows, _ := s.drafts.get(id)
	for _, d := range rows {
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
	if s.sharedMoves || s.moveSeq == nil {
		s.moveSeq, s.sharedMoves = maps.Clone(s.moveSeq), false
		if s.moveSeq == nil {
			s.moveSeq = map[domain.ItemID]int64{}
		}
	}
	s.moveSeq[it.ID] = seq
	s.seq = seq
}

// phaseOf is the state a change impact started its draft in; false when the change never moved or the impact started
// no draft on the main flow.
func (s *draftState) phaseOf(id domain.ChangeImpactID) (phase, bool) {
	p, ok := s.phases.get(id)
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
		s.phases.set(e.Impact, phase{State: s.state, Seq: int64(e.Seq), Before: !s.moved})
	}
	s.applyImpact(e)
	switch e.Op {
	case domain.ImpactCreated, domain.ImpactCheckedOut, domain.ImpactTransitioned, domain.ImpactUpdated, domain.ImpactCancelled:
		rows, _ := s.drafts.get(e.Impact)
		rows = slices.Clone(rows)
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
		if next != nil {
			if held, _ := s.nodes.get(next.Node); !slices.Contains(held, e.Impact) {
				s.nodes.set(next.Node, append(slices.Clip(held), e.Impact))
			}
		}
		if len(rows) == 0 {
			s.drafts.del(e.Impact)
		} else {
			s.drafts.set(e.Impact, rows)
		}
	case domain.ImpactWithdrawn, domain.ImpactCommitted:
		s.drafts.del(e.Impact)
	case domain.ImpactAdopted:
		// the adopted flow's drafts were installed on the main flow by the events that follow (see the adoption)
		for _, id := range s.draftIDs() {
			rows, _ := s.drafts.get(id)
			if rows = slices.DeleteFunc(slices.Clone(rows), func(d domain.Draft) bool { return d.Flow == e.Flow }); len(rows) == 0 {
				s.drafts.del(id)
			} else {
				s.drafts.set(id, rows)
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
		for _, id := range s.draftIDs() {
			if !live(id) {
				s.drafts.del(id)
			}
		}
	} else if s.drafts.has(e.Impact) && !live(e.Impact) {
		s.drafts.del(e.Impact)
	}
	cur, _ := s.conflicts.get(e.Impact)
	if c := domain.ApplyConflictEvent(cur, e); len(c) > 0 {
		s.conflicts.set(e.Impact, c)
	} else {
		s.conflicts.del(e.Impact)
	}
	s.seq = int64(e.Seq)
}

// applyImpact folds an event into the change impacts (domain.ApplyImpactEventAt): an event touches its own impact, an
// adoption, or the removal of an impact, the list.
func (s *draftState) applyImpact(e domain.ImpactEvent) {
	i, ok := s.at.get(e.Impact)
	if e.Op == domain.ImpactAdopted {
		s.resetImpacts(domain.ApplyImpactEventAt(s.impacts.list().Slice(), -1, e))
		return
	}
	one, at := []domain.ChangeImpact(nil), -1
	if ok {
		one, at = []domain.ChangeImpact{s.impacts.at(i)}, 0
	}
	out := domain.ApplyImpactEventAt(one, at, e)
	switch {
	case ok && len(out) == 0: // a change impact removed
		all := s.impacts.list().Slice()
		s.resetImpacts(slices.Delete(all, i, i+1))
	case ok:
		s.unindex(i, s.impacts.at(i))
		s.impacts.set(i, out[0])
		s.index(i, out[0])
	case len(out) > 0:
		s.at.set(e.Impact, s.impacts.n)
		s.index(s.impacts.n, out[0])
		s.impacts.append(out[0])
	}
}

// resetImpacts makes the change impacts those of a list, indexed again.
func (s *draftState) resetImpacts(list []domain.ChangeImpact) {
	s.impacts.reset(list)
	s.at = cowMap[domain.ChangeImpactID, int]{}
	s.byNode, s.byKey, s.odd = cowMap[domain.NodeID, []int]{}, cowMap[string, []int]{}, 0
	for j, cn := range list {
		s.at.set(cn.ID, j)
		s.index(j, cn)
	}
}

// impactNodes are the nodes an impact names: its Pre and its Post (the same node but for a merge or a split).
func impactNodes(cn domain.ChangeImpact) (pre, post domain.NodeID) {
	if cn.Pre != nil {
		pre = cn.Pre.ID
	}
	if cn.Post != nil && cn.Post.ID != pre {
		post = cn.Post.ID
	}
	return pre, post
}

func oddImpact(cn domain.ChangeImpact) bool { return cn.Superseded || cn.Flow != "" }

// index adds the impact at position i to the lookups (positions in order: a new impact is the last, an impact replaced
// keeps its place).
func (s *draftState) index(i int, cn domain.ChangeImpact) {
	pre, post := impactNodes(cn)
	for _, n := range []domain.NodeID{pre, post} {
		if n != "" {
			at, _ := s.byNode.get(n)
			s.byNode.set(n, insertPos(at, i))
		}
	}
	if cn.Key != "" {
		at, _ := s.byKey.get(cn.Key)
		s.byKey.set(cn.Key, insertPos(at, i))
	}
	if oddImpact(cn) {
		s.odd++
	}
}

// unindex takes the impact at position i out of the lookups.
func (s *draftState) unindex(i int, cn domain.ChangeImpact) {
	pre, post := impactNodes(cn)
	for _, n := range []domain.NodeID{pre, post} {
		if n != "" {
			at, _ := s.byNode.get(n)
			if at = deletePos(at, i); len(at) == 0 {
				s.byNode.del(n)
			} else {
				s.byNode.set(n, at)
			}
		}
	}
	if cn.Key != "" {
		at, _ := s.byKey.get(cn.Key)
		if at = deletePos(at, i); len(at) == 0 {
			s.byKey.del(cn.Key)
		} else {
			s.byKey.set(cn.Key, at)
		}
	}
	if oddImpact(cn) {
		s.odd--
	}
}

// insertPos is a sorted list of positions with i, a new slice (the old one may be shared with a copy of the view).
func insertPos(at []int, i int) []int {
	j, found := slices.BinarySearch(at, i)
	if found {
		return at
	}
	return slices.Insert(slices.Clone(at), j, i)
}

// deletePos is a sorted list of positions without i, a new slice.
func deletePos(at []int, i int) []int {
	j, found := slices.BinarySearch(at, i)
	if !found {
		return at
	}
	return slices.Delete(slices.Clone(at), j, j+1)
}

// indexedList is the impacts of the view with its lookups (domain.ImpactLookup): what an operation reads of a view
// the transaction no longer writes (frozen, or published).
func (s *draftState) indexedList() domain.Impacts {
	return domain.IndexedImpacts(s.impacts.blocks, s.impacts.n, &viewLookup{at: s.at.share(), byNode: s.byNode.share(), byKey: s.byKey.share(), odd: s.odd})
}

// viewLookup is the domain.ImpactLookup of a view, over containers it shares with it.
type viewLookup struct {
	at     cowMap[domain.ChangeImpactID, int]
	byNode cowMap[domain.NodeID, []int]
	byKey  cowMap[string, []int]
	odd    int
}

func (v *viewLookup) Position(id domain.ChangeImpactID) (int, bool) { return v.at.get(id) }
func (v *viewLookup) OfNode(id domain.NodeID) []int {
	at, _ := v.byNode.get(id)
	return at
}
func (v *viewLookup) OfKey(key string) []int {
	at, _ := v.byKey.get(key)
	return at
}
func (v *viewLookup) Odd() int { return v.odd }

// draftIDs are the impacts holding drafts.
func (s *draftState) draftIDs() []domain.ChangeImpactID {
	out := make([]domain.ChangeImpactID, 0, s.drafts.len())
	for id := range s.drafts.all() {
		out = append(out, id)
	}
	return out
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
	return draftCache{views: cache.New(bounds, func(_ domain.ChangeID, s *draftState) int64 { return s.bytes }).Named("graph.drafts")}
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
	if whole && (st.top.Load() == top+1 || top <= st.seq) {
		// the log has not moved since the fold was whole, or holds nothing past it (a change with no entry yet)
		st.top.Store(top + 1)
		readLog(tx, st)
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
	held, _ := st.nodes.get(node)
	impacts := slices.Clone(held)
	slices.Sort(impacts)
	var out []domain.Draft
	for _, imp := range slices.Compact(impacts) {
		rows, _ := st.drafts.get(imp)
		rows = slices.Clone(rows)
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
	for _, rows := range st.drafts.all() {
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
