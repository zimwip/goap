package graph_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
)

func TestChangesPage(t *testing.T) { forEachRepo(t, testChangesPage) }

// testChangesPage: the pages of the changes come newest first, each change once, the ones the caller may not see left
// out before a page is cut, and the filter's project and text narrow them.
func testChangesPage(t *testing.T, g *graph.Graph, first domain.Change) {
	ctx := context.Background()
	for i := range 7 {
		if _, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: fmt.Sprintf("p%d", i), BaselineID: first.BaselineID}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := g.ListChanges(ctx, graph.ChangesFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want := len(all) - 1 // the bootstrap's, the first, p0..p6, one hidden
	hidden := func(c domain.Change) bool { return c.Title != "p3" }
	var titles []string
	seen := map[domain.ChangeID]bool{}
	cursor, pages := "", 0
	for ; ; pages++ {
		p, err := g.ChangesPage(ctx, graph.ChangesFilter{HeadersOnly: true}, 3, cursor, hidden)
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != want {
			t.Fatalf("total %d, want %d", p.Total, want)
		}
		if len(p.Changes) != 3 && p.Next != "" {
			t.Fatalf("a page of 3 that is not the last holds %d changes", len(p.Changes))
		}
		for _, c := range p.Changes {
			if seen[c.ID] {
				t.Fatalf("change %s on two pages", c.Title)
			}
			seen[c.ID] = true
			titles = append(titles, c.Title)
		}
		if p.Next == "" {
			break
		}
		cursor = p.Next
	}
	if len(titles) != want || pages != (want-1)/3 {
		t.Fatalf("changes by pages of 3: %v in %d pages", titles, pages+1)
	}
	newest := map[string]int{}
	for i, c := range titles {
		newest[c] = i
	}
	for _, a := range all {
		for _, b := range all {
			if ia, ok := newest[a.Title]; ok {
				if ib, ok := newest[b.Title]; ok && a.CreatedAt.After(b.CreatedAt) && ia > ib {
					t.Fatalf("%s is newer than %s but comes after it", a.Title, b.Title)
				}
			}
		}
	}
	p, err := g.ChangesPage(ctx, graph.ChangesFilter{Query: "P5", Project: "PROJ-ROOT", HeadersOnly: true}, 0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 1 || len(p.Changes) != 1 || p.Changes[0].Title != "p5" || p.Next != "" {
		t.Fatalf("query p5: %+v", p)
	}
	if p, _ := g.ChangesPage(ctx, graph.ChangesFilter{Project: "PROJ-OTHER"}, 0, "", nil); p.Total != 0 {
		t.Fatalf("another project: %d changes", p.Total)
	}
	full, err := g.ChangesPage(ctx, graph.ChangesFilter{Query: "p1"}, 5, "", nil)
	if err != nil || len(full.Changes) != 1 || full.Changes[0].Title != "p1" {
		t.Fatalf("whole change of p1: %+v, %v", full, err)
	}
}
