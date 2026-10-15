package graph

import (
	"context"
	"testing"
)

// The nodes a commit creates link each other in any direction: a cycle is one change too.
func TestCommitLinksCreatedNodesInACycle(t *testing.T) {
	ctx := context.Background()
	g := New(NewMemory())
	res, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: "cycle", By: "test", Edits: []NodeEdit{
		{Key: "A", Type: "T", Links: []LinkEdit{{Type: "next", ToKey: "B"}}},
		{Key: "B", Type: "T", Links: []LinkEdit{{Type: "next", ToKey: "C"}}},
		{Key: "C", Type: "T", Links: []LinkEdit{{Type: "next", ToKey: "A"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Change == "" {
		t.Fatal("one change")
	}
	for _, k := range []string{"A", "B", "C"} {
		n, err := g.NodeByKey(ctx, "default", k)
		if err != nil {
			t.Fatal(err)
		}
		out, err := g.OutLinksOf(ctx, n.Ref())
		if err != nil || len(out) != 1 {
			t.Fatalf("%s links: %v %v", k, out, err)
		}
	}
	cs, _ := g.Changes(ctx)
	n := 0
	for _, c := range cs {
		if c.Title == "cycle" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("one change for the whole cycle, got %d", n)
	}
	if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: "bad", By: "test", Edits: []NodeEdit{{Key: "X", Type: "T", Links: []LinkEdit{{Type: "next", ToKey: "nope"}}}}}); err == nil {
		t.Fatal("a link to a node the commit does not create is refused")
	}
}
