package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/contracts/lnk"
)

// The guard (ADR 0054) is the rule of the graph, in Go, whatever the storage: a database with constraints repeats
// it, a storage without any still gets it. Every repository transaction of a Graph goes through it:
//
//   - every node version, link, branch membership and node origin names the change that writes it, and that change
//     exists;
//   - every node version is owned by an organisational unit and every node was created in a project, nodes of the
//     structures in force; a version that names neither gets them from the version it follows, else from the change
//     that writes it (the unit holding it, the project it acts in); the project of a node never changes;
//   - every change names the unit holding it and the project it acts in, nodes of the structures in force;
//   - every commit is the result of a change (ADR 0056); what precedes the first change of a namespace is the empty
//     state, the empty commit id, which nothing stores;
//   - a version is immutable once written (ADR 0079): the working state of a node in a change is a draft, the version is
//     written when the change commits, with its outgoing links, in the same transaction; no version is ever edited,
//     frozen or dropped, and only a version written in the transaction gets links.
//
// What names a change, a unit or a project is checked when the transaction ends, before it commits (as a deferred
// foreign key): the bootstrap writes the root unit and the root project, each referencing the other and itself, in
// one transaction.

type guardRepo struct {
	Repo
	g *Graph
}

func (r *guardRepo) InTx(ctx context.Context, fn func(tx Tx) error) error {
	var gt *guardTx
	err := r.Repo.InTx(ctx, func(tx Tx) error {
		gt = &guardTx{Tx: tx, g: r.g}
		if err := fn(gt); err != nil {
			return err
		}
		return gt.verify(ctx)
	})
	if gt != nil && gt.purged {
		r.g.versions.reset() // what the transaction deleted may have been read meanwhile
		r.g.forgetSelections()
	}
	if err == nil {
		// the heads kept of the branches it moved move with it, the nodes of the keys it created known (heads.go)
		for k, id := range gt.newKeys {
			r.g.keyIDs.Put(k, id)
		}
		for k, head := range gt.movedBranches {
			if h, ok := gt.wroteHeads[head]; ok {
				name := domain.BranchOf(h.Branch)
				r.g.headMoved(k, headMove{head: h, moves: gt.headMoves[name], whole: true})
			}
		}
		for ns := range gt.moved {
			if ns == domain.NamespacePlatform {
				r.g.platformMoves.Add(1)
			}
			if r.g.OnBranchMoved != nil {
				r.g.OnBranchMoved(ns)
			}
		}
		for _, k := range gt.found {
			r.g.foundRefs.Put(k, true)
		}
		// the versions it wrote and read back are what is stored now that it committed: kept for the next readers (a
		// link to one checks its type)
		if len(gt.written) > 0 && !gt.purged {
			gen := r.g.versions.generation()
			for ref, n := range gt.nodes {
				if gt.written[ref] {
					kept := copyNode(n)
					r.g.versions.update(gen, ref, func(e *versionEntry) { e.node, e.hasNode, e.exists = kept, true, true })
				}
			}
		}
		// what the transaction folded of the logs it wrote is committed now: the next one folds only what follows
		for _, st := range gt.drafts {
			// a fold known whole at the lock, then holding every impact event and fact the transaction appended, is
			// whole up to its last append (the writers of a log hold its lock)
			if gt.whole[st.change] && st.seq >= gt.impactSeq[st.change] {
				st.top.Store(gt.appended[st.change] + 1)
			}
			r.g.draftStates.put(st)
		}
		// the caller's watermark: where the writes left the log of each change (ADR 0153)
		if w := graphapi.WatermarkOf(ctx); w != nil {
			for id, seq := range gt.impactSeq {
				w.Note(id, seq)
			}
		}
	}
	return err
}

type guardTx struct {
	Tx
	g *Graph
	// moved are the namespaces a branch of which the transaction wrote (Graph.Sources and OnBranchMoved, once it
	// commits)
	moved map[string]bool
	// movedBranches are the branches the transaction wrote (guardBranchKey) and their heads: the heads kept of them
	// move once it commits (headMoved), with the commits it wrote (wroteHeads, their order set) and the versions it
	// moved on each branch by name (headMoves, 0: a version deleted, unknownVersion: one taken it did not read), the
	// nodes it created by namespace and key (newKeys)
	movedBranches map[string]domain.CommitID
	// ownHeads are the heads the transaction gave branches of changes by their commits (commitOwn, ADR 0175)
	ownHeads   map[string]domain.CommitID
	wroteHeads map[domain.CommitID]domain.Commit
	headMoves  map[string]map[domain.NodeID]domain.Version
	newKeys    map[string]domain.NodeID
	// what the transaction wrote that names a change, a unit or a project, checked at its end
	changeRefs map[domain.ChangeID]string
	owners     map[lnk.URL]string
	projects   map[lnk.URL]string
	changes    map[domain.ChangeID]domain.Change
	// newCommits are the commits the transaction wrote (their headers are not cached before it ends)
	newCommits map[domain.CommitID]bool
	// commitReads are the versions the transaction read of some nodes in commits it did not write (0: not held)
	commitReads map[domain.CommitID]map[domain.NodeID]domain.Version
	// commit is, per branch, the order of the commit the versions the transaction writes or joins there commit in
	// (ADR 0140): taken by the first of them, given to the commit written after them
	commit map[string]int64
	// headers caches the changes read to stamp versions
	headers map[domain.ChangeID]domain.Change
	// written are the versions the transaction wrote
	written map[domain.NodeRef]bool
	// reserved is the position of the log entry committing the versions being written (ReserveLogSeq): the next entry
	// the transaction appends, of that change, and the one every version of that change written meanwhile names
	reserved logPos
	// nodes are the versions it read that it did not write (versioncache.go)
	nodes map[domain.NodeRef]domain.Node
	// latestOn are the latest versions on a branch it read in a batch, by branch then node (0: none there), dropped for
	// a node it writes or joins a version of (versioncache.go)
	latestOn map[string]map[domain.NodeID]domain.Version
	// wroteLinks are the links of versions it wrote, read back in a batch (prefetch), dropped when it writes links of
	// the version again
	wroteLinks map[domain.NodeRef][]domain.Link
	// keys are the ids of the keys it read in a batch, by namespace and key ("": no node), dropped for a node it writes
	keys map[string]domain.NodeID
	// purged: the transaction deleted a change, and versions with it (the version cache is emptied once it is over)
	purged bool
	// logged are the changes whose log the transaction appended to (the draft cache, ADR 0079, never publishes what a
	// transaction that may roll back has seen of its own writes)
	logged map[domain.ChangeID]bool
	// drafts are the folds of the logs the transaction appended to, as it sees them (never published to the draft cache)
	drafts map[domain.ChangeID]*draftState
	// impactSeq is, per change, the seq of the last impact event or fact the transaction appended (what the fold holds)
	impactSeq map[domain.ChangeID]int64
	// fresh is, per change whose lock the transaction holds, the fold it last read the log for: only its own appends can
	// follow it (ADR 0127)
	fresh map[domain.ChangeID]*draftState
	// heads are the headers of the changes read by the transaction, their impacts taken from the fold of their log
	heads map[domain.ChangeID]domain.Change
	// locked are the changes and branches whose lock the transaction holds (ADR 0127)
	locked map[string]bool
	// tops are the last positions of the logs of the changes it locked, read with their header (lockReader)
	tops map[domain.ChangeID]int64
	// appended is, per change, the last position the transaction appended to its log
	appended map[domain.ChangeID]int64
	// found are the references to units and projects the transaction found, kept once it commits (inStructure)
	found []string
	// whole are the changes whose log the transaction read whole under their lock, before it appended to it
	whole map[domain.ChangeID]bool
	// branches are the branches the transaction read since it took its last lock, by namespace and name (a zero
	// branch: none): a lock taken empties it (what a lock protects is read again once held), a write of a branch drops it
	branches map[string]domain.Branch
}

func guardBranchKey(namespace, name string) string {
	return domain.NamespaceOf(namespace) + "\x00" + name
}

// Branch reads a branch once per transaction and lock (ADR 0127): the writers of a branch hold the lock of its change
// tree or of the branch, which the transaction takes before it reads what it changes.
func (t *guardTx) Branch(ctx context.Context, namespace, name string) (domain.Branch, error) {
	key := guardBranchKey(namespace, name)
	if b, ok := t.branches[key]; ok {
		if b.Name == "" {
			return b, fmt.Errorf("branch %s: %w", name, ErrNotFound)
		}
		return b, nil
	}
	b, err := t.Tx.Branch(ctx, namespace, name)
	switch {
	case errors.Is(err, ErrNotFound):
		t.keepBranch(key, domain.Branch{}) // none: kept too, until a lock or a write
	case err == nil:
		t.keepBranch(key, b)
	}
	return b, err
}

// forgetBranch drops what the transaction kept of a branch: the next read reads it again, as it stands now.
func (t *guardTx) forgetBranch(namespace, name string) {
	delete(t.branches, guardBranchKey(namespace, name))
}

func (t *guardTx) keepBranch(key string, b domain.Branch) {
	if t.branches == nil {
		t.branches = map[string]domain.Branch{}
	}
	t.branches[key] = b
}

func (t *guardTx) PutBranch(ctx context.Context, b domain.Branch) error {
	key := guardBranchKey(b.Namespace, b.Name)
	if h, ok := t.ownHeads[key]; ok && b.Ephemeral {
		// the head of the branch of a change moves by its commits only (commitOwn, ADR 0175): a branch written from a
		// copy read before one keeps it
		b.Head = h
	}
	delete(t.branches, key)
	if t.moved == nil {
		t.moved = map[string]bool{}
	}
	t.moved[domain.NamespaceOf(b.Namespace)] = true
	if t.movedBranches == nil {
		t.movedBranches = map[string]domain.CommitID{}
	}
	t.movedBranches[key] = b.Head
	return t.Tx.PutBranch(ctx, b)
}

// CommitHeader reads the header of a commit, kept once written (a header never changes).
func (t *guardTx) CommitHeader(ctx context.Context, id domain.CommitID) (domain.Commit, error) {
	return t.header(ctx, id)
}

// LockChange takes the lock of the tree of a change once per transaction (ADR 0127). Every write naming a change takes
// it (the log, the header, the projection of the impacts); an operation that reads before it writes takes it first,
// so that what it checked still holds when it writes.
func (t *guardTx) LockChange(ctx context.Context, id domain.ChangeID) error {
	if id == "" || t.locked["change:"+string(id)] {
		return nil
	}
	if err := t.Tx.LockChange(ctx, id); err != nil {
		return err
	}
	t.lock("change:" + string(id))
	if r, ok := t.Tx.(lockReader); ok {
		if b, hasBranch, top, ok := r.lockReads(id); ok {
			if hasBranch {
				t.keepBranch(guardBranchKey(b.Namespace, b.Name), b)
			}
			if t.tops == nil {
				t.tops = map[domain.ChangeID]int64{}
			}
			t.tops[id] = top
		}
	}
	return nil
}

func (t *guardTx) wholeLog(id domain.ChangeID) {
	if t.whole == nil {
		t.whole = map[domain.ChangeID]bool{}
	}
	t.whole[id] = true
}

// newChange holds the lock of a change the transaction creates: its id is unknown outside it until it commits, so
// no writer can contend for it. Its log is empty.
func (t *guardTx) newChange(id domain.ChangeID) {
	key := "change:" + string(id)
	if t.locked == nil {
		t.locked = map[string]bool{}
	}
	t.locked[key] = true
	if t.tops == nil {
		t.tops = map[domain.ChangeID]int64{}
	}
	t.tops[id] = 0
}

// lockReader is a repository transaction that reads, with the header of a change once its lock is held, the branch
// the change names and the last position of its log (the PostgreSQL one: what saves a statement per read).
type lockReader interface {
	lockReads(id domain.ChangeID) (b domain.Branch, hasBranch bool, top int64, ok bool)
}

// topOf is the last position of the log of a change when the transaction took its lock, false when it does not know
// it or appended to that log since (only its own appends follow it then, which its fold holds).
func (t *guardTx) topOf(id domain.ChangeID) (int64, bool) {
	if t.logged[id] {
		return 0, false
	}
	top, ok := t.tops[id]
	return top, ok
}

// LockBranch takes the lock of a branch once per transaction (ADR 0127).
func (t *guardTx) LockBranch(ctx context.Context, namespace, name string) error {
	key := "branch:" + domain.NamespaceOf(namespace) + "/" + name
	if t.locked[key] {
		return nil
	}
	if err := t.Tx.LockBranch(ctx, namespace, name); err != nil {
		return err
	}
	t.lock(key)
	return nil
}

func (t *guardTx) lock(key string) {
	if t.locked == nil {
		t.locked = map[string]bool{}
	}
	t.locked[key] = true
	t.branches = nil // what the writers the lock held off changed is read again
}

// Change reads a change: its header once per transaction, its change impacts and items from the fold of its log (ADR
// 0153; the store keeps no impact before its commit, ADR 0174), so that an operation on a change of N impacts decodes
// none of them.
func (t *guardTx) Change(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	c, ok := t.heads[id]
	if !ok {
		var err error
		if c, err = t.Tx.ChangeHead(ctx, id); err != nil {
			return c, err
		}
		if t.heads == nil {
			t.heads = map[domain.ChangeID]domain.Change{}
		}
		t.heads[id] = c
	}
	st, err := t.g.draftState(ctx, t, id)
	if err != nil {
		return c, err
	}
	c.Data = maps.Clone(c.Data)
	c.Guardians = slices.Clone(c.Guardians)
	c.DependsOn = slices.Clone(c.DependsOn)
	c.Items = slices.Clone(st.items)
	c.ShareImpacts(domain.Impacts{})
	c.Nodes = st.impacts.list().Slice() // values the fold replaces, never edits
	c.Seq = st.seq
	return c, nil
}

// Changes reads every change as Change does: its impacts and items from the fold of its log (ADR 0174).
func (t *guardTx) Changes(ctx context.Context) ([]domain.Change, error) {
	cs, err := t.Tx.Changes(ctx)
	if err != nil {
		return nil, err
	}
	return t.viewed(ctx, cs)
}

// SubChanges reads the sub-changes of a change as Change does.
func (t *guardTx) SubChanges(ctx context.Context, parent domain.ChangeID) ([]domain.Change, error) {
	cs, err := t.Tx.SubChanges(ctx, parent)
	if err != nil {
		return nil, err
	}
	return t.viewed(ctx, cs)
}

// viewed reads changes a store read again as Change does, their headers known.
func (t *guardTx) viewed(ctx context.Context, cs []domain.Change) ([]domain.Change, error) {
	t.knowHeads(cs)
	out := make([]domain.Change, len(cs))
	for i, c := range cs {
		var err error
		if out[i], err = t.Change(ctx, c.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// openChange reads a change as Change does, its impacts shared with the view of the change rather than copied: what an
// operation of the graph reads before it writes (changeOpen), never handed to a caller of the graph, which may edit
// them. The view's values are replaced, never edited, and the transaction's own view, frozen, is copied before its
// next write, which clones them (draftState.own).
func (t *guardTx) openChange(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	c, ok := t.heads[id]
	if !ok {
		var err error
		if c, err = t.Tx.ChangeHead(ctx, id); err != nil {
			return c, err
		}
		if t.heads == nil {
			t.heads = map[domain.ChangeID]domain.Change{}
		}
		t.heads[id] = c
	}
	st, err := t.g.draftState(ctx, t, id)
	if err != nil {
		return c, err
	}
	if ownDrafts(t, id) == st {
		st.frozen = true
	}
	c.Data = maps.Clone(c.Data)
	c.Guardians = slices.Clone(c.Guardians)
	c.DependsOn = slices.Clone(c.DependsOn)
	c.Items = slices.Clip(st.items)
	c.ShareImpacts(st.indexedList())
	c.Seq = st.seq
	return c, nil
}

// knowHeads gives the transaction the headers of changes read in one statement (a page of a listing), so that reading
// those changes reads their logs only.
func (t *guardTx) knowHeads(cs []domain.Change) {
	if t.heads == nil {
		t.heads = map[domain.ChangeID]domain.Change{}
	}
	for _, c := range cs {
		if _, ok := t.heads[c.ID]; !ok {
			t.heads[c.ID] = c
		}
	}
}

// logPos is a position in the log of a change.
type logPos struct {
	change domain.ChangeID
	seq    int64
}

// ReserveLogSeq reserves the position of the entry committing the versions written next (ADR 0174 addendum): they
// name it as they are written, the entry appended after them takes it, and nothing else is appended meanwhile.
func (t *guardTx) ReserveLogSeq(ctx context.Context, change domain.ChangeID) (int64, error) {
	if t.reserved.seq != 0 {
		return 0, fmt.Errorf("change %s: a log position is reserved already for change %s", change, t.reserved.change)
	}
	if err := t.LockChange(ctx, change); err != nil {
		return 0, err
	}
	seq, err := t.Tx.ReserveLogSeq(ctx, change)
	if err != nil {
		return 0, err
	}
	t.reserved = logPos{change, seq}
	return seq, nil
}

// committing checks the position of the log entry a version of a change names: the one reserved, and while one is,
// every version of that change names it.
func (t *guardTx) committing(what string, change domain.ChangeID, seq int64) error {
	switch {
	case seq == 0 && t.reserved.change != change:
		return nil
	case seq == 0 || t.reserved.seq == 0:
		return fmt.Errorf("%s: names log entry %d of change %s, %d is reserved (change %q): the entry committing a version is reserved before it is written: %w", what, seq, change, t.reserved.seq, t.reserved.change, ErrInvalid)
	case seq != t.reserved.seq || change != t.reserved.change:
		return fmt.Errorf("%s: names log entry %d of change %s, %d of %s is reserved: %w", what, seq, change, t.reserved.seq, t.reserved.change, ErrInvalid)
	}
	return nil
}

func (t *guardTx) AppendLog(ctx context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	if err := t.beforeAppend(ctx, e); err != nil {
		return e, err
	}
	out, err := t.Tx.AppendLog(ctx, e)
	if err == nil {
		t.afterAppend(out)
	}
	return out, err
}

// AppendLogs appends entries in their order, in one statement when the store can (logBatcher).
func (t *guardTx) AppendLogs(ctx context.Context, entries []domain.LogEntry) ([]domain.LogEntry, error) {
	if t.reserved.seq != 0 && len(entries) > 1 {
		return nil, fmt.Errorf("change %s: log entries appended while position %d is reserved", t.reserved.change, t.reserved.seq)
	}
	for _, e := range entries {
		if err := t.beforeAppend(ctx, e); err != nil {
			return nil, err
		}
	}
	out, err := appendLogs(ctx, t.Tx, entries)
	if err != nil {
		return nil, err
	}
	for _, e := range out {
		t.afterAppend(e)
	}
	return out, nil
}

func (t *guardTx) beforeAppend(ctx context.Context, e domain.LogEntry) error {
	if e.Seq != t.reserved.seq || (e.Seq != 0 && e.Change != t.reserved.change) {
		return fmt.Errorf("log entry %s of change %s at %d: position %d of change %q is reserved: %w", e.Type, e.Change, e.Seq, t.reserved.seq, t.reserved.change, ErrInvalid)
	}
	if err := t.LockChange(ctx, e.Change); err != nil {
		return err
	}
	if t.logged == nil {
		t.logged = map[domain.ChangeID]bool{}
	}
	t.logged[e.Change] = true
	if !strings.HasPrefix(e.Type, domain.LogImpact+".") {
		delete(t.heads, e.Change) // a fact is an item of the change
	}
	return nil
}

func (t *guardTx) afterAppend(e domain.LogEntry) {
	if e.Seq == t.reserved.seq {
		t.reserved = logPos{}
	}
	if t.appended == nil {
		t.appended = map[domain.ChangeID]int64{}
	}
	t.appended[e.Change] = max(t.appended[e.Change], e.Seq)
	if strings.HasPrefix(e.Type, domain.LogFact+".") {
		t.foldFact(e)
	}
	if strings.HasPrefix(e.Type, domain.LogImpact+".") || strings.HasPrefix(e.Type, domain.LogFact+".") {
		if t.impactSeq == nil {
			t.impactSeq = map[domain.ChangeID]int64{}
		}
		t.impactSeq[e.Change] = e.Seq
	}
}

// foldFact folds an item just appended into the fold of its change the transaction holds whole (current), so the
// operation's next read of the view reads no log; an impact event is folded by Graph.emit.
func (t *guardTx) foldFact(e domain.LogEntry) {
	st := t.current(e.Change)
	if st == nil || e.Seq <= st.seq {
		return
	}
	items, err := itemsOf([]domain.LogEntry{e})
	if err != nil || len(items) != 1 {
		return // the next read of the view folds it from the log
	}
	next := st
	if t.drafts[e.Change] != st || st.frozen {
		next = st.copy()
	}
	next.fact(items[0], e.Seq)
	next.seq = e.Seq
	t.keepDrafts(next)
	t.readLog(next)
}

var _ draftKeeper = (*guardTx)(nil)

func (t *guardTx) wroteLog(id domain.ChangeID) bool { return t.logged[id] }

func (t *guardTx) ownDrafts(id domain.ChangeID) *draftState { return t.drafts[id] }

// current is the fold of a change that holds every impact event of its log, when the transaction knows one: it holds the
// lock of the change since it read the log (ADR 0127), so only its own appends can follow, and it has folded them.
func (t *guardTx) current(id domain.ChangeID) *draftState {
	if st := t.fresh[id]; st != nil && st.seq >= t.impactSeq[id] {
		return st
	}
	return nil
}

// readLog records the fold a transaction holding the lock of a change read the log for.
func (t *guardTx) readLog(st *draftState) {
	if !t.locked["change:"+string(st.change)] {
		return
	}
	if t.fresh == nil {
		t.fresh = map[domain.ChangeID]*draftState{}
	}
	t.fresh[st.change] = st
}

func (t *guardTx) keepDrafts(s *draftState) {
	if t.drafts == nil {
		t.drafts = map[domain.ChangeID]*draftState{}
	}
	t.drafts[s.change] = s
}

func (t *guardTx) needChange(id domain.ChangeID, what string) error {
	if id == "" {
		return fmt.Errorf("%s is written outside of a change (ADR 0049, 0054): %w", what, ErrInvalid)
	}
	if t.changeRefs == nil {
		t.changeRefs = map[domain.ChangeID]string{}
	}
	if _, ok := t.changeRefs[id]; !ok {
		t.changeRefs[id] = what
	}
	return nil
}

// change reads the header of a change (cached for the transaction): its unit, its project, that it exists; never its
// log, which the fold of the change holds.
func (t *guardTx) change(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	if c, ok := t.headers[id]; ok {
		return c, nil
	}
	c, err := t.Tx.ChangeHead(ctx, id)
	if err != nil {
		return c, err
	}
	if t.headers == nil {
		t.headers = map[domain.ChangeID]domain.Change{}
	}
	t.headers[id] = c
	return c, nil
}

func (t *guardTx) PutNode(ctx context.Context, n domain.Node) error {
	what := "node " + n.Key + " v" + fmt.Sprint(n.Version)
	if err := t.needChange(n.ChangeID, what); err != nil {
		return err
	}
	t.forgetLatest(n.ID)
	delete(t.keys, nsKey(n.Namespace, n.Key))
	var prev *domain.Node
	if n.Version > 1 {
		// the previous version alone, whatever the length of the history (ADR 0140): the project is the node's
		// (the first parent when it names one: read once per transaction, versioncache.go)
		var vs []domain.Node
		if len(n.Parents) > 0 && n.Parents[0] >= 1 && n.Parents[0] < n.Version {
			p, err := t.Node(ctx, domain.NodeRef{ID: n.ID, Version: n.Parents[0]})
			if err != nil {
				return err
			}
			vs = []domain.Node{p}
		} else {
			var err error
			if vs, err = t.Tx.VersionRange(ctx, n.ID, 0, 1); err != nil {
				return err
			}
		}
		if len(vs) > 0 {
			p := vs[0]
			prev = &p
			if !n.Project.IsZero() && n.Project != p.Project {
				return fmt.Errorf("%s: the project of a node never changes (%s, not %s): %w", what, p.Project, n.Project, ErrInvalid)
			}
			n.Project = p.Project
		}
	}
	if n.Responsible.IsZero() && prev != nil {
		n.Responsible = prev.Responsible
	}
	if n.Responsible.IsZero() || n.Project.IsZero() {
		c, err := t.change(ctx, n.ChangeID)
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%s: change %s does not exist: %w", what, n.ChangeID, ErrInvalid)
		}
		if err != nil {
			return err
		}
		if n.Responsible.IsZero() {
			n.Responsible = c.OwnerOrg
		}
		if n.Project.IsZero() {
			n.Project = c.ProjectID
		}
	}
	// who created the node, who edited the version, how far it is seen (ADR 0161 §3-4): the creator is the first
	// version's; the editor, the person the write is done for unless the commit names the draft's; the scope, the
	// domain's for the type and the state now
	if n.Editor == "" {
		n.Editor = t.g.author(ctx)
	}
	if prev != nil {
		n.Creator = prev.Creator
	} else {
		if n.Creator.Subject == "" {
			n.Creator.Subject = n.Editor
		}
		n.Editor = n.Creator.Subject // the first version's editor is the creator
		if n.Creator.Unit.IsZero() {
			n.Creator.Unit = n.Responsible
		}
	}
	if n.Visibility == "" {
		n.Visibility = t.g.visibilityOf(n.Type, n.State)
	}
	if t.owners == nil {
		t.owners, t.projects = map[lnk.URL]string{}, map[lnk.URL]string{}
	}
	t.owners[n.Responsible] = what
	t.projects[n.Project] = what
	if err := t.committing(what, n.ChangeID, n.LogSeq); err != nil {
		return err
	}
	if n.Committed >= 0 { // a negative order commits the version in no commit (a test fixture)
		committed, err := t.commitOn(ctx, n.Branch, n.ChangeID)
		if err != nil {
			return err
		}
		n.Committed = committed
	}
	if err := t.Tx.PutNode(ctx, n); err != nil {
		return err
	}
	if n.Version == 1 {
		if t.newKeys == nil {
			t.newKeys = map[string]domain.NodeID{}
		}
		t.newKeys[nsKey(n.Namespace, n.Key)] = n.ID
	}
	if n.Committed > 0 {
		v := n.Version
		if n.Deleted {
			v = 0
		}
		t.headMove(n.Branch, n.ID, v)
	}
	if t.written == nil {
		t.written = map[domain.NodeRef]bool{}
	}
	t.written[n.Ref()] = true
	return nil
}

func (t *guardTx) PutLinks(ctx context.Context, ls []domain.Link) error {
	for _, l := range ls {
		if err := t.needChange(l.ChangeID, "link "+l.Type); err != nil {
			return err
		}
		if !t.written[l.From] {
			return fmt.Errorf("link %s: version %s is not written by this transaction, a version never changes (ADR 0079): %w", l.Type, l.From, ErrConflict)
		}
		delete(t.wroteLinks, l.From)
	}
	return t.Tx.PutLinks(ctx, ls)
}

func (t *guardTx) DropLink(ctx context.Context, from domain.NodeRef, row domain.LinkID, l domain.Link) error {
	if err := t.needChange(l.ChangeID, "link "+l.Type); err != nil {
		return err
	}
	if !t.written[from] {
		return fmt.Errorf("link %s: version %s is not written by this transaction, a version never changes (ADR 0079): %w", l.Type, from, ErrConflict)
	}
	delete(t.wroteLinks, from)
	return t.Tx.DropLink(ctx, from, row, l)
}

// PutLinkSet sets how a version written in the transaction holds its links, over versions of the same node written
// before it (ADR 0141).
func (t *guardTx) PutLinkSet(ctx context.Context, from domain.NodeRef, set LinkSet) error {
	if !t.written[from] {
		return fmt.Errorf("the links of version %s: it is not written by this transaction, a version never changes (ADR 0079): %w", from, ErrConflict)
	}
	delete(t.wroteLinks, from)
	for _, v := range []domain.Version{set.Of, set.Base} {
		if v >= from.Version {
			return fmt.Errorf("the links of version %s: v%d is not an earlier version of the node: %w", from, v, ErrInvalid)
		}
	}
	if set.Of != 0 && set.Base != 0 {
		return fmt.Errorf("the links of version %s: a version shares a set or writes a delta, not both: %w", from, ErrInvalid)
	}
	return t.Tx.PutLinkSet(ctx, from, set)
}

// logSeq is the entry of the change's log recording the merge: the one reserved, or one the transaction appended.
func (t *guardTx) JoinCommits(ctx context.Context, branch string, change domain.ChangeID, _, logSeq int64, seqs []int64) error {
	if err := t.needChange(change, "the commits "+branch+" takes"); err != nil {
		return err
	}
	if logSeq != 0 && !(t.reserved.change == change && t.reserved.seq == logSeq) && (logSeq > t.appended[change] || t.appended[change] == 0) {
		return fmt.Errorf("the commits %s takes: log entry %d of change %s is neither reserved nor appended by the transaction: %w", branch, logSeq, change, ErrInvalid)
	}
	at, err := t.commitOn(ctx, branch, change)
	if err != nil {
		return err
	}
	return t.Tx.JoinCommits(ctx, branch, change, at, logSeq, seqs)
}

// took notes a version a branch holds once it took the commits of another branch (takeCommits): what is kept of the
// node's latest version and the head kept in memory follow it.
func (t *guardTx) took(branch string, ref domain.NodeRef) {
	t.forgetLatest(ref.ID)
	n, ok := t.nodes[ref]
	if !ok {
		n, ok = t.g.versions.node(ref)
	}
	switch {
	case !ok:
		t.headMove(branch, ref.ID, unknownVersion)
	case n.Deleted:
		t.headMove(branch, ref.ID, 0)
	default:
		t.headMove(branch, ref.ID, ref.Version)
	}
}

// headMove notes the version a node has on a branch once the transaction commits (0: deleted there).
func (t *guardTx) headMove(branch string, id domain.NodeID, v domain.Version) {
	name := domain.BranchOf(branch)
	if t.headMoves == nil {
		t.headMoves = map[string]map[domain.NodeID]domain.Version{}
	}
	if t.headMoves[name] == nil {
		t.headMoves[name] = map[domain.NodeID]domain.Version{}
	}
	t.headMoves[name][id] = v
}

func (t *guardTx) PutChange(ctx context.Context, c domain.Change) error {
	if c.OwnerOrg.IsZero() || c.ProjectID.IsZero() {
		return fmt.Errorf("change %s names no owner unit or no project (ADR 0054): %w", c.ID, ErrInvalid)
	}
	// a new sub-change is written with its parent's tree (it forks the parent's branch)
	if c.ParentID != "" {
		if err := t.LockChange(ctx, c.ParentID); err != nil {
			return err
		}
	}
	if err := t.LockChange(ctx, c.ID); err != nil {
		return err
	}
	if err := t.Tx.PutChange(ctx, c); err != nil {
		return err
	}
	if t.changes == nil {
		t.changes = map[domain.ChangeID]domain.Change{}
	}
	h := c
	h.Items = nil
	h.ShareImpacts(domain.Impacts{})
	t.changes[c.ID] = h
	delete(t.headers, c.ID)
	delete(t.heads, c.ID)
	return nil
}

func (t *guardTx) DeleteChange(ctx context.Context, id domain.ChangeID, namespace, branch string) error {
	// a version commits only in a commit of the change that wrote or joined it (ADR 0140): the store refuses a change
	// that produced a commit, and checks the entries stored
	if err := t.LockChange(ctx, id); err != nil {
		return err
	}
	t.purged, t.nodes, t.latestOn, t.keys, t.wroteLinks = true, nil, nil, nil, nil
	t.g.versions.reset()
	if err := t.Tx.DeleteChange(ctx, id, namespace, branch); err != nil {
		return err
	}
	delete(t.changes, id)
	delete(t.headers, id)
	delete(t.heads, id)
	t.branches = nil
	return nil
}

// verify checks, at the end of the transaction, that what it wrote names changes, units and projects that exist, and
// that every version it committed is in a commit.
func (t *guardTx) verify(ctx context.Context) error {
	if t.reserved.seq != 0 {
		return fmt.Errorf("change %s: log position %d reserved for versions no entry committed", t.reserved.change, t.reserved.seq)
	}
	for branch := range t.commit {
		return fmt.Errorf("versions committed on branch %s in no commit: a version is written when its change commits (ADR 0079, 0140): %w", branch, ErrInvalid)
	}
	for id, what := range t.changeRefs {
		if _, ok := t.changes[id]; ok {
			continue
		}
		if _, err := t.change(ctx, id); errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%s names change %s, which does not exist: %w", what, id, ErrInvalid)
		} else if err != nil {
			return err
		}
	}
	for u, what := range t.owners {
		if err := t.inStructure(ctx, domain.StructureOrganisation, u, what+": owner"); err != nil {
			return err
		}
	}
	for u, what := range t.projects {
		if err := t.inStructure(ctx, domain.StructureProject, u, what+": project"); err != nil {
			return err
		}
	}
	for _, c := range t.changes {
		if err := t.inStructure(ctx, domain.StructureOrganisation, c.OwnerOrg, "change "+string(c.ID)+": owner"); err != nil {
			return err
		}
		if err := t.inStructure(ctx, domain.StructureProject, c.ProjectID, "change "+string(c.ID)+": project"); err != nil {
			return err
		}
	}
	return nil
}

// inStructure checks that a reference names a node of the structure of a kind: the URL names the structure's source
// and one of its types (ADR 0154 §9), and the node of its key exists (on any branch) and is of that type; asked to
// Graph.Directory when the structures live in another graph, else read in the transaction.
func (t *guardTx) inStructure(ctx context.Context, kind string, u lnk.URL, what string) error {
	st := t.g.Structure(kind)
	key := u.Key
	switch {
	case u.Source != st.Namespace:
		return fmt.Errorf("%s %s: a %s is read in the source %s: %w", what, u, kind, st.Namespace, ErrInvalid)
	case !t.g.isA(st.Namespace+"@"+u.Type, st.Type):
		return fmt.Errorf("%s %s: %s is not a type of the %s: %w", what, u, u.Type, kind, ErrInvalid)
	}
	if t.g.Directory != nil {
		_, ok, err := t.g.Directory.StructureNode(ctx, kind, key)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s %q is not a %s of the platform: %w", what, key, kind, ErrInvalid)
		}
		return nil
	}
	// a node found stays: none is deleted (ADR 0079) nor changes its type, and a purge removes only a change that
	// committed nothing
	found := kind + "\x00" + u.String()
	if _, ok := t.g.foundRefs.Get(found); ok {
		return nil
	}
	id, err := t.Tx.NodeIDByKey(ctx, st.Namespace, key)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%s %q is not a node of namespace %s: %w", what, key, st.Namespace, ErrInvalid)
	}
	if err != nil {
		return err
	}
	vs, err := t.Tx.VersionRange(ctx, id, 0, 1)
	if err == nil && len(vs) == 0 {
		return fmt.Errorf("%s %s does not exist: %w", what, key, ErrInvalid)
	}
	if err != nil {
		return err
	}
	if n := vs[0]; !t.g.inStructure(n, st) || !t.g.isA(n.Type, st.Namespace+"@"+u.Type) {
		return fmt.Errorf("%s %s is a %s of namespace %s, not a %s: %w", what, n.Key, n.Type, domain.NamespaceOf(n.Namespace), st.Type, ErrInvalid)
	}
	t.found = append(t.found, found)
	return nil
}
