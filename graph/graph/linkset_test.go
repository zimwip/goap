package graph

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

func TestLinkSets(t *testing.T) { forEachRepo(t, testLinkSets) }

// testLinkSets: the links of a version are a set (ADR 0141): an edit of the attributes shares the set of the version
// it was checked out from, a link added or removed writes a delta over it, a delta too deep writes the links whole, and
// the links read the same whatever way they are held, leaving and entering.
func testLinkSets(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	edits := []NodeEdit{{Key: "H", Type: "Need"}}
	for i := range 10 {
		key := fmt.Sprintf("T-%d", i)
		edits = append(edits, NodeEdit{Key: key, Type: "Need"})
		edits[0].Links = append(edits[0].Links, LinkEdit{Type: "refines", ToKey: key})
	}
	commit := func(e NodeEdit) domain.Node {
		t.Helper()
		if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: "edit", By: "t", Edits: []NodeEdit{e}}); err != nil {
			t.Fatal(err)
		}
		return must[domain.Node](t)(g.NodeByKey(ctx, "", "H"))
	}
	if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: "seed", By: "t", Edits: edits}); err != nil {
		t.Fatal(err)
	}
	h1 := must[domain.Node](t)(g.NodeByKey(ctx, "", "H"))
	t1 := must[domain.Node](t)(g.NodeByKey(ctx, "", "T-1"))
	t2 := must[domain.Node](t)(g.NodeByKey(ctx, "", "T-2"))
	ref := h1.Ref()
	h2 := commit(NodeEdit{Pre: &ref, Props: map[string]any{"title": "renamed"}})
	ref = h2.Ref()
	h3 := commit(NodeEdit{Pre: &ref, Links: []LinkEdit{{Type: "refines", To: &domain.NodeRef{ID: t1.ID, Version: t1.Version}, Props: map[string]any{"why": "again"}}}})
	var drop domain.LinkID
	for _, l := range must[[]domain.Link](t)(g.OutLinksOf(ctx, h3.Ref())) {
		if l.To.ID == t2.ID {
			drop = l.ID
		}
	}
	ref = h3.Ref()
	h4 := commit(NodeEdit{Pre: &ref, RemoveLinks: []domain.LinkID{drop}})

	set := func(n domain.Node) LinkSet {
		t.Helper()
		var s LinkSet
		if err := repo.InTx(ctx, func(tx Tx) (err error) { s, err = tx.LinkSetOf(ctx, n.Ref()); return }); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := set(h1); s != (LinkSet{Of: h1.Version}) {
		t.Fatalf("the first version holds its links whole: %+v", s)
	}
	if s := set(h2); s.Of != h1.Version || s.Base != 0 {
		t.Fatalf("an edit of the attributes shares the set of the version it was checked out from: %+v", s)
	}
	if s := set(h3); s.Of != h3.Version || s.Base != h1.Version || s.Depth != 1 {
		t.Fatalf("a link added writes a delta over the set the version held: %+v", s)
	}
	if s := set(h4); s.Of != h4.Version || s.Base != h3.Version || s.Depth != 2 {
		t.Fatalf("a link removed writes a delta: %+v", s)
	}
	out := func(n domain.Node) []domain.Link {
		t.Helper()
		ls := must[[]domain.Link](t)(g.OutLinksOf(ctx, n.Ref()))
		for _, l := range ls {
			if l.From != n.Ref() {
				t.Fatalf("a link of %s read as leaving %s", n.Ref(), l.From)
			}
		}
		return ls
	}
	for _, c := range []struct {
		n    domain.Node
		want int
	}{{h1, 10}, {h2, 10}, {h3, 11}, {h4, 10}} {
		if got := len(out(c.n)); got != c.want {
			t.Fatalf("v%d holds %d links, want %d", c.n.Version, got, c.want)
		}
	}
	if slices.ContainsFunc(out(h4), func(l domain.Link) bool { return l.ID == drop }) {
		t.Fatal("the dropped link is still held")
	}
	// entering T-2: the versions holding the link, the one that dropped it left out
	var froms []domain.Version
	if err := repo.InTx(ctx, func(tx Tx) error {
		in, err := tx.InLinks(ctx, t2.Ref())
		for _, l := range in {
			froms = append(froms, l.From.Version)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(froms)
	if !slices.Equal(froms, []domain.Version{h1.Version, h2.Version, h3.Version}) {
		t.Fatalf("T-2 is entered from H %v, want v%d..v%d", froms, h1.Version, h3.Version)
	}
	// a delta too deep: the links written whole
	h, whole := h4, false
	for i := range maxLinkDepth {
		ref := h.Ref()
		to := must[domain.Node](t)(g.NodeByKey(ctx, "", fmt.Sprintf("T-%d", i%10)))
		h = commit(NodeEdit{Pre: &ref, Links: []LinkEdit{{Type: "refines", To: &domain.NodeRef{ID: to.ID, Version: to.Version}, Props: map[string]any{"n": float64(i)}}}})
		if got := len(out(h)); got != 11+i {
			t.Fatalf("v%d holds %d links, want %d", h.Version, got, 11+i)
		}
		s := set(h)
		if s.Depth > maxLinkDepth {
			t.Fatalf("a set %d deltas deep", s.Depth)
		}
		whole = whole || s.Base == 0
	}
	if !whole {
		t.Fatal("a delta too deep writes the links whole")
	}
}
