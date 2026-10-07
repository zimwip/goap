package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// EventSink receives the events of the node index (ADR 0026). It is satisfied by engine.Publisher.
type EventSink interface {
	Publish(ctx context.Context, subject string, v any) error
}

// Observe makes the graph publish a NodeEvent for every node version written (at landing, ADR 0079: a node in a change
// is a draft, no version) and a BaselineEvent
// for every baseline created, once the transaction that wrote them has committed. It sees every
// write path. Publishing is best effort: the index rebuilds from the graph (Reindex) when an
// event is lost.
func (g *Graph) Observe(sink EventSink) {
	if sink != nil {
		g.repo = &observedRepo{Repo: g.repo, g: g, sink: sink}
	}
}

type observedRepo struct {
	Repo
	g    *Graph
	sink EventSink
}

type observedTx struct {
	Tx
	// nodes are the versions written by the transaction
	nodes     []domain.NodeRef
	baselines []domain.Baseline
	// changes written in the transaction, in order: the header when it was written, else just the id
	changes []domain.ChangeID
	headers map[domain.ChangeID]domain.Change
	// indexed are the changes whose document for the index must be published (ADR 0095): the header or the impacts were
	// written, in order; purged are the ones deleted.
	indexed []domain.ChangeID
	purged  []domain.ChangeID
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

type published struct {
	subject string
	v       any
}

func (r *observedRepo) InTx(ctx context.Context, fn func(tx Tx) error) error {
	var out []published
	err := r.Repo.InTx(ctx, func(tx Tx) error {
		out = nil
		ot := &observedTx{Tx: tx}
		if err := fn(ot); err != nil {
			return err
		}
		var err error
		out, err = r.g.eventsOf(ctx, tx, ot)
		return err
	})
	if err == nil {
		for _, p := range out {
			_ = r.sink.Publish(ctx, p.subject, p.v)
		}
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
