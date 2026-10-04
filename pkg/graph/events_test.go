package graph

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

type recSink struct {
	mu   sync.Mutex
	subj []string
	vals []any
}

func (r *recSink) Publish(_ context.Context, subject string, v any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subj, r.vals = append(r.subj, subject), append(r.vals, v)
	return nil
}

func TestObserveNodeAndBaselineEvents(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g := New(repo)
		mk := func(key, typ string, props map[string]any) domain.Node {
			n, err := g.CreateNode(ctx, NewNode{Key: key, Type: typ, Properties: props})
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
		// a creation now lands as a change of its own (ADR 0049): besides the node's own NodeEvent, its
		// Apply (and the merge of its own branch into main) advance one or more baselines, each a
		// BaselineEvent — so the node's event is looked up among them, not assumed to be the only one.
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
		// the creation advanced the head of main: a BaselineEvent among the events
		advanced := 0
		for i, v := range sink.vals {
			if _, ok := v.(domain.BaselineEvent); ok && sink.subj[i] == "goap.baseline.main.advanced" {
				advanced++
			}
		}
		if advanced == 0 {
			t.Fatalf("no baseline event: %v", sink.subj)
		}
	})
}
