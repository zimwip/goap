package graph

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/lib/outbox"
)

type recSink struct {
	mu   sync.Mutex
	subj []string
	vals []any
}

// Deliver implements outbox.Sink: the payload decoded by its subject.
func (r *recSink) Deliver(ctx context.Context, m outbox.Message) error {
	var v any
	switch {
	case strings.HasPrefix(m.Subject, "goap.node."):
		var ev domain.NodeEvent
		_ = json.Unmarshal(m.Data, &ev)
		v = ev
	case strings.HasPrefix(m.Subject, "goap.commit."):
		var ev domain.CommitEvent
		_ = json.Unmarshal(m.Data, &ev)
		v = ev
	case strings.HasPrefix(m.Subject, "goap.change.") || strings.HasPrefix(m.Subject, "goap.changed."):
		var ev domain.ChangeEvent
		_ = json.Unmarshal(m.Data, &ev)
		v = ev
	default:
		v = m.Data
	}
	return r.Publish(ctx, m.Subject, v)
}

func (r *recSink) Publish(_ context.Context, subject string, v any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subj, r.vals = append(r.subj, subject), append(r.vals, v)
	return nil
}

func TestObserveNodeAndCommitEvents(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g := New(repo)
		mk := func(key, typ string, props map[string]any) domain.Node {
			n, err := importNode(ctx, g, newNode{Key: key, Type: typ, Properties: props})
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		g.Types = func() TypeCatalog {
			return testTypes{
				"Item":    {Search: []domain.SearchProperty{{Property: "title", Text: true, Facet: true}, {Property: "prio", Facet: true}}},
				"SubItem": {Extends: "Item", Search: []domain.SearchProperty{{Property: "notes", Text: true}}},
			}
		}
		if _, err := g.BranchHead(ctx, "", domain.MainBranch); err != nil {
			t.Fatal(err)
		}
		sink := &recSink{}
		g.Observe(sink)
		// a creation now commits as a change of its own (ADR 0049): besides the node's own NodeEvent, its
		// Apply (and the merge of its own branch into main) advance one or more commits, each a
		// CommitEvent — so the node's event is looked up among them, not assumed to be the only one.
		n := mk("ITEM-1", "SubItem", map[string]any{"title": "hello", "prio": "high", "notes": "long text", "other": 1})
		var ev domain.NodeEvent
		found := false
		for i, v := range sink.vals {
			if e, ok := v.(domain.NodeEvent); ok && e.ID == n.ID {
				if found {
					t.Fatalf("more than one NodeEvent for %s", n.ID)
				}
				ev, found = e, true
				if !strings.HasSuffix(sink.subj[i], "."+string(n.ID)+".written") {
					t.Fatalf("bad subject %s", sink.subj[i])
				}
			}
		}
		if !found {
			t.Fatalf("no NodeEvent for %s among %d events", n.ID, len(sink.vals))
		}
		if ev.ID != n.ID || ev.Type != "SubItem" {
			t.Fatalf("bad event %+v", ev)
		}
		if ev.Text["title"] != "hello" || ev.Text["notes"] != "long text" || len(ev.Text) != 2 {
			t.Fatalf("text = %v", ev.Text)
		}
		if ev.Facets["title"] != "hello" || ev.Facets["prio"] != "high" || len(ev.Facets) != 2 {
			t.Fatalf("facets = %v", ev.Facets)
		}
		// the creation advanced the head of main: a CommitEvent among the events
		advanced := 0
		for i, v := range sink.vals {
			if _, ok := v.(domain.CommitEvent); ok && sink.subj[i] == "goap.commit.main.advanced" {
				advanced++
			}
		}
		if advanced == 0 {
			t.Fatalf("no commit event: %v", sink.subj)
		}
	})
}

// The change.updated event of a write carries the position the write left the log of the change at, the one the writer
// gets in its watermark; a write of the header alone carries none (ADR 0153).
func TestChangeEventCarriesThePosition(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g := New(repo)
		base, err := g.BranchHead(ctx, "", domain.MainBranch)
		if err != nil {
			t.Fatal(err)
		}
		c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "positions", ForkCommit: base.ID})
		if err != nil {
			t.Fatal(err)
		}
		sink := &recSink{}
		g.Observe(sink)
		updated := func() []int64 {
			var out []int64
			for i, v := range sink.vals {
				if e, ok := v.(domain.ChangeEvent); ok && e.Type == "change.updated" && sink.subj[i] == "goap.changed."+string(c.ID) {
					out = append(out, e.Change.Seq)
				}
			}
			sink.vals, sink.subj = nil, nil
			return out
		}
		wctx, w := graphapi.WithWatermark(ctx)
		note := domain.ChangeItem{Type: domain.TypeArtifact, Target: "note", Data: map[string]any{"text": "one"}}
		if _, err := g.AddItems(wctx, c.ID, []domain.ChangeItem{note}); err != nil {
			t.Fatal(err)
		}
		if got := updated(); len(got) != 1 || got[0] != w.Of(c.ID) || got[0] <= 0 {
			t.Fatalf("the event carries %v, the write left the log at %d", got, w.Of(c.ID))
		}
		title := "renamed"
		if _, err := g.UpdateChange(ctx, c.ID, ChangePatch{Title: &title}); err != nil {
			t.Fatal(err)
		}
		for _, seq := range updated() {
			if seq > w.Of(c.ID) {
				t.Fatalf("a header write carries %d, past the last entry %d", seq, w.Of(c.ID))
			}
		}
	})
}
