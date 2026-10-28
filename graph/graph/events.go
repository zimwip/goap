package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/events"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/lib/outbox"
)

// EventSink receives typed events: what Republish hands the index (ADR 0026). It is satisfied by engine.Publisher.
type EventSink interface {
	Publish(ctx context.Context, subject string, v any) error
}

// OutboxOrigin names the graph in the ids of its events (ADR 0136), unless Graph.Origin names it otherwise.
const OutboxOrigin = "graph"

// Observe makes the graph emit its events (ADR 0136): the changes (change.created, change.applied...), the technical
// change.updated and change documents of the index, a NodeEvent for every node version written (at commit, ADR 0079:
// a node in a change is a draft, no version) and a CommitEvent for every commit created. They are written in the
// outbox of the graph in the transaction that writes their fact, whatever the write path, and delivered to sink once
// it commits; the relay returned delivers again what could not be (Run it). It is called once, before serving.
func (g *Graph) Observe(sink outbox.Sink) *outbox.Relay {
	if sink == nil {
		return nil
	}
	store, ok := g.base.(outbox.Store)
	if !ok {
		panic(fmt.Sprintf("graph: repository %T keeps no outbox", g.base))
	}
	origin := g.Origin
	if origin == "" {
		origin = OutboxOrigin
	}
	g.relay = &outbox.Relay{Origin: origin, Store: store, Sink: sink}
	a := g.repo.(*ambientRepo)
	a.Repo = &observedRepo{Repo: a.Repo, g: g}
	return g.relay
}

// Relay is the relay of the outbox of the graph, nil until Observe.
func (g *Graph) Relay() *outbox.Relay { return g.relay }

type observedRepo struct {
	Repo
	g *Graph
}

type observedTx struct {
	Tx
	// nodes are the versions written by the transaction
	nodes   []domain.NodeRef
	commits []domain.Commit
	// deltas are what the commits written over their parent changed (putCommitOver)
	deltas map[domain.CommitID]map[domain.NodeID]domain.Version
	// changes written in the transaction, in order: the header when it was written, else just the id
	changes []domain.ChangeID
	headers map[domain.ChangeID]domain.Change
	// indexed are the changes whose document for the index must be published (ADR 0095): the header or the impacts were
	// written, in order; purged are the ones deleted.
	indexed []domain.ChangeID
	purged  []domain.ChangeID
	// raised are the events of the changes the operations of the transaction emitted (raise), in order
	raised []published
	// seq is, per change, the position the transaction left its log at (its last impact event or fact, ADR 0153),
	// carried by the change.updated event so that a reader can require it of its next read
	seq map[domain.ChangeID]int64
}

// raise emits an event of a change from the transaction that writes its fact (ADR 0136): stored in the outbox with
// the writes, published once they commit. Without an observer (no Observe) it does nothing. The header is the one of
// the event (c, items and impacts left out); set completes the event.
func (g *Graph) raise(ctx context.Context, tx Tx, typ string, c domain.Change, set func(*domain.ChangeEvent)) {
	ot, ok := tx.(*observedTx)
	if !ok {
		return
	}
	c.Items, c.Nodes = nil, nil
	ev := domain.ChangeEvent{Type: typ, Change: c}
	if g.EndUser != nil {
		ev.EndUser = g.EndUser(ctx) // what the event sets off is attributed to its end user (ADR 0114)
	}
	if set != nil {
		set(&ev)
	}
	ot.raised = append(ot.raised, published{events.ChangeSubject(string(c.ID), typ), ev})
}

// raiseItems emits change.item_added for items written on c, and change.signal when one of them is a signal.
func (g *Graph) raiseItems(ctx context.Context, tx Tx, c domain.Change, items []domain.ChangeItem) {
	if len(items) == 0 {
		return
	}
	with := func(ev *domain.ChangeEvent) { ev.Items = items }
	g.raise(ctx, tx, events.ChangeItemAdded, c, with)
	if slices.ContainsFunc(items, func(it domain.ChangeItem) bool { return it.Type == domain.TypeSignal }) {
		g.raise(ctx, tx, events.ChangeSignal, c, with)
	}
}

// raiseApplied emits change.applied: the change committed in what it was forked from, leaving b (its header: the state
// is not carried).
func (g *Graph) raiseApplied(ctx context.Context, tx Tx, c domain.Change, b domain.Commit) {
	b.Nodes = nil
	g.raise(ctx, tx, events.ChangeApplied, c, func(ev *domain.ChangeEvent) { ev.Commit = &b })
	if ot, ok := tx.(*observedTx); ok {
		evs, _ := ctx.Value(carriedKey{}).([]graphapi.CarriedEvent)
		for _, e := range evs {
			ot.raised = append(ot.raised, published{e.Subject, e.Payload})
		}
	}
}

type carriedKey struct{}

// withCarried is ctx carrying the writer's events of a Commit to the transaction that commits it.
func withCarried(ctx context.Context, evs []graphapi.CarriedEvent) context.Context {
	if len(evs) == 0 {
		return ctx
	}
	return context.WithValue(ctx, carriedKey{}, evs)
}

// carriedFamilies are the subjects the graph emits itself: a writer may not carry one (ADR 0136).
var carriedFamilies = []string{"goap.change.", "goap.changed.", "goap.changeindex.", "goap.node.", "goap.commit."}

// checkCarried refuses a carried event outside goap.>, with a wildcard or an empty token, or of the graph's families,
// and one whose payload is not JSON.
func checkCarried(evs []graphapi.CarriedEvent) error {
	for _, e := range evs {
		bad := !strings.HasPrefix(e.Subject, "goap.") || strings.ContainsAny(e.Subject, "*> \t\r\n") ||
			strings.Contains(e.Subject, "..") || strings.HasSuffix(e.Subject, ".") || !json.Valid(e.Payload)
		for _, f := range carriedFamilies {
			bad = bad || strings.HasPrefix(e.Subject, f)
		}
		if bad {
			return fmt.Errorf("event %q: a writer carries its own events, under goap., as JSON: %w", e.Subject, ErrInvalid)
		}
	}
	return nil
}

// raiseCommitted emits change.committed (ADR 0159): c committed on the branch of b (its header: the state is not
// carried), writing the versions nodes.
func (g *Graph) raiseCommitted(ctx context.Context, tx Tx, c domain.Change, b domain.Commit, nodes []domain.Node) {
	b.Nodes = nil
	g.raise(ctx, tx, events.ChangeCommitted, c, func(ev *domain.ChangeEvent) {
		ev.Commit, ev.Branch, ev.Nodes = &b, domain.BranchOf(b.Branch), eventNodes(nodes)
	})
}

// raiseRebased emits change.rebased (ADR 0159): the branch of change id was moved onto the head of what it is forked
// from, the versions nodes the commit of its merges wrote there (none when only its fork moved, or while a conflict
// waits for its resolution).
func (g *Graph) raiseRebased(ctx context.Context, tx Tx, id domain.ChangeID, branch string, nodes []domain.Node) {
	if _, ok := tx.(*observedTx); !ok {
		return
	}
	c, err := tx.Change(ctx, id)
	if err != nil {
		return
	}
	g.raise(ctx, tx, events.ChangeRebased, c, func(ev *domain.ChangeEvent) {
		ev.Branch, ev.Nodes = domain.BranchOf(branch), eventNodes(nodes)
	})
}

// raiseMerged emits change.merged (ADR 0159): c merged into branch into, leaving there the versions nodes (written or
// joined by the merge), b the commit it left when the branch keeps one (nil: the branch of a change).
func (g *Graph) raiseMerged(ctx context.Context, tx Tx, c domain.Change, into string, b *domain.Commit, nodes []domain.Node) {
	if b != nil {
		h := *b
		h.Nodes = nil
		b = &h
	}
	g.raise(ctx, tx, events.ChangeMerged, c, func(ev *domain.ChangeEvent) {
		ev.Commit, ev.Branch, ev.Nodes = b, domain.BranchOf(into), eventNodes(nodes)
	})
}

// raiseMergedRefs is raiseMerged with the versions read by reference.
func (g *Graph) raiseMergedRefs(ctx context.Context, tx Tx, c domain.Change, into string, b *domain.Commit, refs []domain.NodeRef) error {
	if _, ok := tx.(*observedTx); !ok {
		return nil
	}
	var nodes []domain.Node
	if len(refs) > 0 {
		var err error
		if nodes, err = tx.NodesAt(ctx, refs, ""); err != nil {
			return err
		}
	}
	g.raiseMerged(ctx, tx, c, into, b, nodes)
	return nil
}

func eventNodes(nodes []domain.Node) []domain.EventNode {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]domain.EventNode, len(nodes))
	for i, n := range nodes {
		out[i] = domain.EventNodeOf(n)
	}
	return out
}
func (t *observedTx) index(id domain.ChangeID) {
	if id != "" && !slices.Contains(t.indexed, id) {
		t.indexed = append(t.indexed, id)
	}
}

func (t *observedTx) DeleteChange(ctx context.Context, id domain.ChangeID, namespace, branch string) error {
	if err := t.Tx.DeleteChange(ctx, id, namespace, branch); err != nil {
		return err
	}
	t.indexed = slices.DeleteFunc(t.indexed, func(x domain.ChangeID) bool { return x == id })
	if !slices.Contains(t.purged, id) {
		t.purged = append(t.purged, id)
	}
	return nil
}

func (t *observedTx) touch(id domain.ChangeID) {
	if id == "" {
		return
	}
	for _, c := range t.changes {
		if c == id {
			return
		}
	}
	t.changes = append(t.changes, id)
}

// the observed transaction forwards what the guard keeps of the views of the changes
var _ draftKeeper = (*observedTx)(nil)

// wroteLog forwards the guard's record of the log entries the transaction appended.
func (t *observedTx) wroteLog(id domain.ChangeID) bool { return wroteLog(t.Tx, id) }

func (t *observedTx) ownDrafts(id domain.ChangeID) *draftState { return ownDrafts(t.Tx, id) }

func (t *observedTx) keepDrafts(s *draftState) { keepDrafts(t.Tx, s) }

func (t *observedTx) current(id domain.ChangeID) *draftState { return currentDrafts(t.Tx, id) }

func (t *observedTx) readLog(st *draftState) { readLog(t.Tx, st) }

func (t *observedTx) PutChange(ctx context.Context, c domain.Change) error {
	if err := t.Tx.PutChange(ctx, c); err != nil {
		return err
	}
	if t.headers == nil {
		t.headers = map[domain.ChangeID]domain.Change{}
	}
	c.Items, c.Nodes = nil, nil
	t.headers[c.ID] = c
	t.touch(c.ID)
	t.index(c.ID)
	return nil
}

func (t *observedTx) AppendLog(ctx context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	e, err := t.Tx.AppendLog(ctx, e)
	if err == nil {
		t.touch(e.Change)
		if strings.HasPrefix(e.Type, domain.LogImpact+".") {
			t.index(e.Change) // an impact is written: its change's document (ADR 0095, 0174)
		}
		if strings.HasPrefix(e.Type, domain.LogImpact+".") || strings.HasPrefix(e.Type, domain.LogFact+".") {
			if t.seq == nil {
				t.seq = map[domain.ChangeID]int64{}
			}
			t.seq[e.Change] = e.Seq
		}
	}
	return e, err
}

func (t *observedTx) AppendLogs(ctx context.Context, entries []domain.LogEntry) ([]domain.LogEntry, error) {
	out, err := appendLogs(ctx, t.Tx, entries)
	if err != nil {
		return nil, err
	}
	for _, e := range out {
		t.touch(e.Change)
		if strings.HasPrefix(e.Type, domain.LogImpact+".") {
			t.index(e.Change) // an impact is written: its change's document (ADR 0095, 0174)
		}
		if strings.HasPrefix(e.Type, domain.LogImpact+".") || strings.HasPrefix(e.Type, domain.LogFact+".") {
			if t.seq == nil {
				t.seq = map[domain.ChangeID]int64{}
			}
			t.seq[e.Change] = e.Seq
		}
	}
	return out, nil
}

func (t *observedTx) PutNode(ctx context.Context, n domain.Node) error {
	if err := t.Tx.PutNode(ctx, n); err != nil {
		return err
	}
	t.nodes = append(t.nodes, n.Ref())
	return nil
}

func (t *observedTx) PutCommit(ctx context.Context, b domain.Commit) error {
	if err := t.Tx.PutCommit(ctx, b); err != nil {
		return err
	}
	t.commits = append(t.commits, b)
	return nil
}

// putCommitOver writes a commit over another (putCommitOver); its event is the delta when over is its parent.
func (t *observedTx) putCommitOver(ctx context.Context, b domain.Commit, over domain.CommitID, delta map[domain.NodeID]domain.Version) error {
	if err := putCommitOver(ctx, t.Tx, b, over, delta); err != nil {
		return err
	}
	if over != b.ParentID {
		if domain.BranchOf(b.Branch) != domain.MainBranch {
			b.Nodes = nil // its event carries no diff (CommitEvent): no state is read for it
			t.commits = append(t.commits, b)
			return nil
		}
		full, err := t.Tx.Commit(ctx, b.ID)
		if err != nil {
			return err
		}
		t.commits = append(t.commits, full)
		return nil
	}
	if t.deltas == nil {
		t.deltas = map[domain.CommitID]map[domain.NodeID]domain.Version{}
	}
	t.deltas[b.ID] = delta
	b.Nodes = nil
	t.commits = append(t.commits, b)
	return nil
}

type published struct {
	subject string
	v       any
}

func (r *observedRepo) InTx(ctx context.Context, fn func(tx Tx) error) error {
	wrote := false
	err := r.Repo.InTx(ctx, func(tx Tx) error {
		wrote = false
		ot := &observedTx{Tx: tx}
		if err := fn(ot); err != nil {
			return err
		}
		out, err := r.g.eventsOf(ctx, tx, ot)
		if err != nil || len(out) == 0 {
			return err
		}
		msgs := make([]outbox.Message, 0, len(out))
		for _, p := range out {
			m, err := outbox.New(ctx, p.subject, p.v, r.g.now())
			if err != nil {
				return err
			}
			msgs = append(msgs, m)
		}
		wrote = true
		return tx.AppendOutbox(ctx, msgs)
	})
	if err == nil && wrote {
		// delivered at once; what fails is delivered again by the relay's Run
		_ = r.g.relay.Drain(ctx)
	}
	return err
}

// eventsOf builds the events of what a transaction wrote, inside the transaction (it reads the node types).
func (g *Graph) eventsOf(ctx context.Context, tx Tx, ot *observedTx) ([]published, error) {
	var out []published
	for _, id := range ot.changes {
		c := ot.headers[id]
		c.ID = id
		c.Seq = ot.seq[id] // 0: the transaction left the log where it was
		out = append(out, published{fmt.Sprintf(domain.SubjectChangeTouched, id), domain.ChangeEvent{Type: "change.updated", Change: c}})
	}
	docs, err := g.changeDocs(ctx, tx, ot)
	if err != nil {
		return nil, err
	}
	out = append(out, docs...)
	out = append(out, ot.raised...)
	if len(ot.nodes) > 0 {
		// typesAt ignores its commit argument (the type catalogue is process-global, not
		// namespace-scoped graph data): no commit lookup is needed to build it.
		ix, err := g.typesAt(ctx, tx, "")
		if err != nil {
			return nil, err
		}
		for _, ref := range ot.nodes {
			n, err := tx.Node(ctx, ref)
			if err != nil {
				return nil, err
			}
			ev := domain.NodeEvent{ID: n.ID, Version: n.Version, Branch: domain.BranchOf(n.Branch), Namespace: domain.NamespaceOf(n.Namespace), Key: n.Key,
				Type: n.Type, State: n.State, Deleted: n.Deleted, ChangeID: n.ChangeID, Time: n.CreatedAt, Project: n.Project.Key, Responsible: n.Responsible.Key,
				Visibility: domain.VisibilityOf(n.Visibility), Creator: n.Creator.Subject}
			ev.Text, ev.Facets = ix.searchable(n)
			out = append(out, published{fmt.Sprintf(domain.SubjectNodeWritten, subjectToken(ev.Namespace), subjectToken(n.Type), n.ID), ev})
		}
	}
	for _, b := range ot.commits {
		ev := domain.CommitEventOf(b)
		ev.Set = map[domain.NodeID]domain.Version{}
		if gt, ok := unwrapGuard(tx); ok && ev.Seq == 0 {
			ev.Seq = gt.wroteHeads[b.ID].Seq // the order the guard gave it
		}
		if delta, ok := ot.deltas[b.ID]; ok {
			ev.Whole = true
			for _, id := range slices.Sorted(maps.Keys(delta)) {
				if v := delta[id]; v == 0 {
					ev.Removed = append(ev.Removed, id)
				} else {
					ev.Set[id] = v
				}
			}
			out = append(out, published{fmt.Sprintf(domain.SubjectCommitAdvanced, subjectToken(ev.Branch)), ev})
			continue
		}
		if ev.Branch != domain.MainBranch {
			// the index follows main only: the commit of another branch (a rebase on a change's own branch) names
			// itself, without reading two states to diff them
			out = append(out, published{fmt.Sprintf(domain.SubjectCommitAdvanced, subjectToken(ev.Branch)), ev})
			continue
		}
		ev.Whole = true
		var parent map[domain.NodeID]domain.Version
		if b.ParentID != "" {
			p, err := tx.Commit(ctx, b.ParentID)
			if err != nil {
				return nil, err
			}
			parent = p.Nodes
		}
		for id, v := range b.Nodes {
			if parent[id] != v {
				ev.Set[id] = v
			}
		}
		for id := range parent {
			if _, ok := b.Nodes[id]; !ok {
				ev.Removed = append(ev.Removed, id)
			}
		}
		out = append(out, published{fmt.Sprintf(domain.SubjectCommitAdvanced, subjectToken(ev.Branch)), ev})
	}
	return out, nil
}

// maxIndexedImpacts bounds the node keys a change document carries.
const maxIndexedImpacts = 200

// changeDocs builds the index documents of the changes a transaction wrote or purged (ADR 0095), inside the transaction
// (it reads the impacts). Only the header and the impacts trigger it: a log entry alone does not change the document.
func (g *Graph) changeDocs(ctx context.Context, tx Tx, ot *observedTx) ([]published, error) {
	var out []published
	for _, id := range ot.indexed {
		c, ok := ot.headers[id]
		if !ok { // the impacts alone were written: the head is read, neither the impacts nor the items
			var err error
			if c, err = tx.ChangeHead(ctx, id); err != nil {
				return nil, err
			}
		}
		ev := domain.ChangeDocEvent{ID: c.ID, Title: c.Title, Intent: c.Intent, Goal: c.Goal, Methodology: c.Methodology, Namespace: c.Namespace,
			Status: c.Status, State: c.State, ProjectID: c.ProjectID.Key, OwnerOrg: c.OwnerOrg.Key, ParentID: c.ParentID, Branch: c.Branch, CreatedAt: c.CreatedAt}
		// the first impacts only, from the view of the change (ADR 0174), which the write folded already
		st, err := g.draftState(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		for i := 0; i < st.impacts.n && len(ev.Impacts) < maxIndexedImpacts; i++ {
			cn := st.impacts.at(i)
			ev.Impacts = append(ev.Impacts, domain.ChangeDocRef{Key: cn.Key, Type: cn.Type})
		}
		out = append(out, published{fmt.Sprintf(domain.SubjectChangeIndexed, id), ev})
	}
	for _, id := range ot.purged {
		out = append(out, published{fmt.Sprintf(domain.SubjectChangeIndexed, id), domain.ChangeDocEvent{ID: id, Deleted: true}})
	}
	return out, nil
}

// subjectToken makes a value usable as one token of a NATS subject.
func subjectToken(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '*' || r == '>' || r == ' ' {
			return '_'
		}
		return r
	}, s)
}

// searchable resolves the text and facet values of a node from the search declarations of its type
// (own and inherited, the subtype overriding by property name).
func (ix *typeIndex) searchable(n domain.Node) (map[string]string, map[string]any) {
	if ix == nil {
		return nil, nil
	}
	var text map[string]string
	var facets map[string]any
	for _, sp := range ix.searchOf(n.Type) {
		v, ok := n.Properties[sp.Property]
		if !ok || v == nil {
			continue
		}
		if sp.Text {
			if s := fmt.Sprint(v); s != "" {
				if text == nil {
					text = map[string]string{}
				}
				text[sp.Property] = s
			}
		}
		if sp.Facet {
			if facets == nil {
				facets = map[string]any{}
			}
			facets[sp.Property] = v
		}
	}
	return text, facets
}

// searchOf returns the search declarations of a type along its extends chain.
func (ix *typeIndex) searchOf(typ string) []domain.SearchProperty {
	if ix == nil || ix.cat == nil {
		return nil
	}
	return ix.cat.Search(typ)
}

// Republish publishes the node event of every node version (all branches, every namespace) and the
// head of each namespace's main as a commit event, to rebuild an index (ADR 0026). It returns the
// number of documents published (node versions and changes).
func (g *Graph) Republish(ctx context.Context, sink EventSink) (int, error) {
	var namespaces []string
	if err := g.repo.InTx(ctx, func(tx Tx) (err error) { namespaces, err = tx.Namespaces(ctx); return }); err != nil {
		return 0, err
	}
	total, err := g.republishChanges(ctx, sink)
	if err != nil {
		return total, err
	}
	for _, ns := range namespaces {
		n, err := g.republishNamespace(ctx, ns, sink)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// republishChanges publishes the index document of every change (ADR 0095); it returns their number.
func (g *Graph) republishChanges(ctx context.Context, sink EventSink) (int, error) {
	var events []published
	err := g.repo.InTx(ctx, func(tx Tx) error {
		cs, err := tx.Changes(ctx)
		if err != nil {
			return err
		}
		ot := &observedTx{}
		for _, c := range cs {
			ot.index(c.ID)
		}
		events, err = g.changeDocs(ctx, tx, ot)
		return err
	})
	if err != nil {
		return 0, err
	}
	for i, p := range events {
		if err := sink.Publish(ctx, p.subject, p.v); err != nil {
			return i, err
		}
	}
	return len(events), nil
}

// republishNamespace is the per-namespace core of Republish.
func (g *Graph) republishNamespace(ctx context.Context, namespace string, sink EventSink) (int, error) {
	var events []published
	err := g.repo.InTx(ctx, func(tx Tx) error {
		ot := &observedTx{Tx: tx}
		latest, err := tx.LatestNodes(ctx, namespace, domain.MainBranch)
		if err != nil {
			return err
		}
		for _, l := range latest {
			vs, err := tx.Versions(ctx, l.ID)
			if err != nil {
				return err
			}
			for _, v := range vs {
				ot.nodes = append(ot.nodes, v.Ref())
			}
		}
		if head, err := branchHead(ctx, tx, namespace, domain.MainBranch); err == nil && head.ID != "" {
			head.ParentID = ""
			ot.commits = append(ot.commits, head)
		}
		events, err = g.eventsOf(ctx, tx, ot)
		return err
	})
	if err != nil {
		return 0, err
	}
	nodes := 0
	for _, p := range events {
		if _, ok := p.v.(domain.NodeEvent); ok {
			nodes++
		}
		if err := sink.Publish(ctx, p.subject, p.v); err != nil {
			return nodes, err
		}
	}
	return nodes, nil
}
