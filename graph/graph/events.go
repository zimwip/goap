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

// OutboxOrigin names the graph in the ids of its events (ADR 0136).
const OutboxOrigin = "graph"

// Observe makes the graph emit its events (ADR 0136): the changes (change.created, change.applied...), the technical
// change.updated and change documents of the index, a NodeEvent for every node version written (at commit, ADR 0079:
// a node in a change is a draft, no version) and a BaselineEvent for every baseline created. They are written in the
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
	g.relay = &outbox.Relay{Origin: OutboxOrigin, Store: store, Sink: sink}
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
	nodes     []domain.NodeRef
	baselines []domain.Baseline
	// deltas are what the baselines written over their parent changed (putBaselineOver)
	deltas map[domain.BaselineID]map[domain.NodeID]domain.Version
	// changes written in the transaction, in order: the header when it was written, else just the id
	changes []domain.ChangeID
	headers map[domain.ChangeID]domain.Change
	// indexed are the changes whose document for the index must be published (ADR 0095): the header or the impacts were
	// written, in order; purged are the ones deleted.
	indexed []domain.ChangeID
	purged  []domain.ChangeID
	// raised are the events of the changes the operations of the transaction emitted (raise), in order
	raised []published
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
func (g *Graph) raiseApplied(ctx context.Context, tx Tx, c domain.Change, b domain.Baseline) {
	b.Nodes = nil
	g.raise(ctx, tx, events.ChangeApplied, c, func(ev *domain.ChangeEvent) { ev.Baseline = &b })
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
var carriedFamilies = []string{"goap.change.", "goap.changed.", "goap.changeindex.", "goap.node.", "goap.baseline."}

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

// raiseBaseline is raise with the header of the baseline the event names.
func (g *Graph) raiseBaseline(ctx context.Context, tx Tx, typ string, c domain.Change, b domain.Baseline) {
	b.Nodes = nil
	g.raise(ctx, tx, typ, c, func(ev *domain.ChangeEvent) { ev.Baseline = &b })
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

func (t *observedTx) PutChangeImpact(ctx context.Context, change domain.ChangeID, cn domain.ChangeImpact) error {
	if err := t.Tx.PutChangeImpact(ctx, change, cn); err != nil {
		return err
	}
	t.touch(change)
	t.index(change)
	return nil
}

func (t *observedTx) AppendLog(ctx context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	e, err := t.Tx.AppendLog(ctx, e)
	if err == nil {
		t.touch(e.Change)
	}
	return e, err
}

func (t *observedTx) PutNode(ctx context.Context, n domain.Node) error {
	if err := t.Tx.PutNode(ctx, n); err != nil {
		return err
	}
	t.nodes = append(t.nodes, n.Ref())
	return nil
}

func (t *observedTx) PutBaseline(ctx context.Context, b domain.Baseline) error {
	if err := t.Tx.PutBaseline(ctx, b); err != nil {
		return err
	}
	t.baselines = append(t.baselines, b)
	return nil
}

// putBaselineOver writes a baseline over another (putBaselineOver); its event is the delta when over is its parent.
func (t *observedTx) putBaselineOver(ctx context.Context, b domain.Baseline, over domain.BaselineID, delta map[domain.NodeID]domain.Version) error {
	if err := putBaselineOver(ctx, t.Tx, b, over, delta); err != nil {
		return err
	}
	if over != b.ParentID {
		if domain.BranchOf(b.Branch) != domain.MainBranch {
			b.Nodes = nil // its event carries no diff (BaselineEvent): no state is read for it
			t.baselines = append(t.baselines, b)
			return nil
		}
		full, err := t.Tx.Baseline(ctx, b.ID)
		if err != nil {
			return err
		}
		t.baselines = append(t.baselines, full)
		return nil
	}
	if t.deltas == nil {
		t.deltas = map[domain.BaselineID]map[domain.NodeID]domain.Version{}
	}
	t.deltas[b.ID] = delta
	b.Nodes = nil
	t.baselines = append(t.baselines, b)
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
		out = append(out, published{fmt.Sprintf(domain.SubjectChangeTouched, id), domain.ChangeEvent{Type: "change.updated", Change: c}})
	}
	docs, err := g.changeDocs(ctx, tx, ot)
	if err != nil {
		return nil, err
	}
	out = append(out, docs...)
	out = append(out, ot.raised...)
	if len(ot.nodes) > 0 {
		// typesAt ignores its baseline argument (the type catalogue is process-global, not
		// namespace-scoped graph data): no baseline lookup is needed to build it.
		ix, err := g.typesAt(ctx, tx, "")
		if err != nil {
			return nil, err
		}
		keys := map[domain.NodeID]string{}
		keyOf := func(id domain.NodeID) string { // the project and owner of a version are nodes: the index holds their keys
			if id == "" {
				return ""
			}
			if k, ok := keys[id]; ok {
				return k
			}
			k := string(id)
			if u, err := tx.Node(ctx, domain.NodeRef{ID: id}); err == nil {
				k = u.Key
			}
			keys[id] = k
			return k
		}
		for _, ref := range ot.nodes {
			n, err := tx.Node(ctx, ref)
			if err != nil {
				return nil, err
			}
			ev := domain.NodeEvent{ID: n.ID, Version: n.Version, Branch: domain.BranchOf(n.Branch), Namespace: domain.NamespaceOf(n.Namespace), Key: n.Key,
				Type: n.Type, State: n.State, Deleted: n.Deleted, ChangeID: n.ChangeID, Time: n.CreatedAt, Project: keyOf(n.Project), Owner: keyOf(n.Owner)}
			ev.Text, ev.Facets = ix.searchable(n)
			out = append(out, published{fmt.Sprintf(domain.SubjectNodeWritten, subjectToken(ev.Namespace), subjectToken(n.Type), n.ID), ev})
		}
	}
	for _, b := range ot.baselines {
		ev := domain.BaselineEvent{ID: b.ID, Branch: domain.BranchOf(b.Branch), Parent: b.ParentID, Set: map[domain.NodeID]domain.Version{}, Time: b.CreatedAt}
		if delta, ok := ot.deltas[b.ID]; ok {
			for _, id := range slices.Sorted(maps.Keys(delta)) {
				if v := delta[id]; v == 0 {
					ev.Removed = append(ev.Removed, id)
				} else {
					ev.Set[id] = v
				}
			}
			out = append(out, published{fmt.Sprintf(domain.SubjectBaselineAdvanced, subjectToken(ev.Branch)), ev})
			continue
		}
		if ev.Branch != domain.MainBranch {
			// the index follows main only: the baseline of another branch (a rebase on a change's own branch) names
			// itself, without reading two states to diff them
			out = append(out, published{fmt.Sprintf(domain.SubjectBaselineAdvanced, subjectToken(ev.Branch)), ev})
			continue
		}
		var parent map[domain.NodeID]domain.Version
		if b.ParentID != "" {
			p, err := tx.Baseline(ctx, b.ParentID)
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
		out = append(out, published{fmt.Sprintf(domain.SubjectBaselineAdvanced, subjectToken(ev.Branch)), ev})
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
		if !ok { // the impacts alone were written: the header is read (its items are not needed, but the repository has no lighter read)
			var err error
			if c, err = tx.Change(ctx, id); err != nil {
				return nil, err
			}
		}
		ev := domain.ChangeDocEvent{ID: c.ID, Title: c.Title, Intent: c.Intent, Goal: c.Goal, Methodology: c.Methodology, Namespace: c.Namespace,
			Status: c.Status, State: c.State, ProjectID: c.ProjectID, OwnerOrg: c.OwnerOrg, ParentID: c.ParentID, Branch: c.Branch, CreatedAt: c.CreatedAt}
		impacts, err := tx.ChangeImpacts(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, cn := range impacts {
			if len(ev.Impacts) == maxIndexedImpacts {
				break
			}
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
// head of each namespace's main as a baseline event, to rebuild an index (ADR 0026). It returns the
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
			ot.baselines = append(ot.baselines, head)
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
