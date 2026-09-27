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
		base := mk("D:x/nodetype/Item", NodeTypeNode, map[string]any{"name": "Item", "search": []any{
			map[string]any{"property": "title", "text": true, "facet": true},
			map[string]any{"property": "prio", "facet": true},
		}})
		sub := mk("D:x/nodetype/SubItem", NodeTypeNode, map[string]any{"name": "SubItem", "extends": "Item", "search": []any{
			map[string]any{"property": "notes", "text": true},
		}})
		if _, err := g.CreateBaseline(ctx, "B1", []domain.NodeRef{base.Ref(), sub.Ref()}); err != nil {
			t.Fatal(err)
		}
		sink := &recSink{}
		g.Observe(sink)
		n := mk("ITEM-1", "SubItem", map[string]any{"title": "hello", "prio": "high", "notes": "long text", "other": 1})
		if len(sink.vals) != 1 {
			t.Fatalf("want 1 event, got %d", len(sink.vals))
		}
		ev := sink.vals[0].(domain.NodeEvent)
		if ev.ID != n.ID || ev.Type != "SubItem" || !strings.HasSuffix(sink.subj[0], "."+string(n.ID)+".written") {
			t.Fatalf("bad event %+v on %s", ev, sink.subj[0])
		}
		if ev.Text["title"] != "hello" || ev.Text["notes"] != "long text" || len(ev.Text) != 2 {
			t.Fatalf("text = %v", ev.Text)
		}
		if ev.Facets["title"] != "hello" || ev.Facets["prio"] != "high" || len(ev.Facets) != 2 {
			t.Fatalf("facets = %v", ev.Facets)
		}
		sink.subj, sink.vals = nil, nil
		if _, err := g.CreateBaseline(ctx, "B2", []domain.NodeRef{base.Ref(), n.Ref()}); err != nil {
			t.Fatal(err)
		}
		if len(sink.vals) != 1 || sink.subj[0] != "goap.baseline.main.advanced" {
			t.Fatalf("baseline event: %v %v", sink.subj, sink.vals)
		}
		if be := sink.vals[0].(domain.BaselineEvent); be.Set[n.ID] != n.Version || len(be.Set) != 2 {
			t.Fatalf("set = %v", be.Set)
		}
	})
}
