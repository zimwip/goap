package graph

import (
	"context"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A graph keeps the head of a branch its views read in memory: its own commits move it at once, the commits of
// another graph sharing its store reach it by their commit events, which carry the header and the versions moved,
// so nothing is read again (heads.go); an event out of order leaves the head unknown, read at the next view.
func TestHeadsFollowCommitsAndEvents(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		writer, reader := New(repo), New(repo)
		reader.FollowHeads()
		sink := &recSink{}
		writer.Observe(sink)
		n, err := importNode(ctx, writer, newNode{Key: "ITEM-1", Type: "Item", Properties: map[string]any{"title": "one"}})
		if err != nil {
			t.Fatal(err)
		}
		main := View{Namespace: domain.DefaultNamespace, Branch: domain.MainBranch}
		version := func(g *Graph) domain.Version {
			t.Helper()
			nv, err := g.ViewNode(ctx, main, "", "ITEM-1")
			if err != nil {
				t.Fatal(err)
			}
			return nv.Node.Version
		}
		if v := version(reader); v != n.Version {
			t.Fatalf("reader reads v%d, want v%d", v, n.Version)
		}
		if v := version(writer); v != n.Version {
			t.Fatalf("writer reads v%d, want v%d", v, n.Version)
		}
		sink.mu.Lock()
		sink.subj, sink.vals = nil, nil
		sink.mu.Unlock()
		n2, err := importProps(ctx, writer, n.Ref(), map[string]any{"title": "two"})
		if err != nil {
			t.Fatal(err)
		}
		// the writer's own commit moved its head
		if v := version(writer); v != n2.Version {
			t.Fatalf("writer reads v%d after its commit, want v%d", v, n2.Version)
		}
		// the reader keeps its head until the events come
		if v := version(reader); v != n.Version {
			t.Fatalf("reader reads v%d before the events, want the head it keeps, v%d", v, n.Version)
		}
		var events []domain.CommitEvent
		sink.mu.Lock()
		for _, v := range sink.vals {
			if ev, ok := v.(domain.CommitEvent); ok && ev.Namespace == domain.DefaultNamespace && ev.Branch == domain.MainBranch {
				events = append(events, ev)
			}
		}
		sink.mu.Unlock()
		if len(events) == 0 {
			t.Fatal("no commit event of main")
		}
		for _, ev := range events {
			if ev.Seq == 0 || !ev.Whole {
				t.Fatalf("event %+v carries no order or not every node moved", ev)
			}
			reader.HeadEvent(ev)
			reader.HeadEvent(ev) // a second delivery changes nothing
		}
		if v := version(reader); v != n2.Version {
			t.Fatalf("reader reads v%d after the events, want v%d", v, n2.Version)
		}
		// an event not following the head held leaves it unknown: read again
		n3, err := importProps(ctx, writer, n.Ref(), map[string]any{"title": "three"})
		if err != nil {
			t.Fatal(err)
		}
		head, err := writer.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)
		if err != nil {
			t.Fatal(err)
		}
		reader.HeadEvent(domain.CommitEvent{ID: "unknown", Namespace: domain.DefaultNamespace, Branch: domain.MainBranch, Parent: "elsewhere", Seq: head.Seq + 1, Whole: true})
		if v := version(reader); v != n3.Version {
			t.Fatalf("reader reads v%d after a gap, want the head read again, v%d", v, n3.Version)
		}
	})
}
