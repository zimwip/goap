package graph

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/platform"
)

// A branch view reads a baseline (ADR 0157): its head when it names none, the one it names else; the node, its
// neighbourhood and the pages are the baseline's (BaselineGraph), whatever moved since.
func TestBranchView(t *testing.T) { forEachRepo(t, testBranchView) }

func testBranchView(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	before := f.base.ID
	// main moves on: REQ-1 gets a new version
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "PSP v2", BaselineID: before, OwnBranch: true}))
	mod := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "PSP v2"}))
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, mod.ID, edit{Properties: map[string]any{"title": "Use PSP v2"}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, mod.ID, "u", "ok"))
	after := must[domain.Baseline](t)(g.apply(ctx, c.ID, ""))

	for _, tc := range []struct {
		name  string
		view  View
		base  domain.BaselineID
		title string
	}{
		{"head", View{Namespace: f.req.Namespace, Branch: domain.MainBranch}, after.ID, "Use PSP v2"},
		{"pinned", View{Namespace: f.req.Namespace, Branch: domain.MainBranch, Baseline: before}, before, "Use PSP v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes, links, err := g.BaselineGraph(ctx, tc.base)
			if err != nil {
				t.Fatal(err)
			}
			nv := must[domain.NodeView](t)(g.ViewNode(ctx, tc.view, "", "REQ-1"))
			if nv.Properties["title"] != tc.title {
				t.Fatalf("REQ-1: %v, want %q", nv.Properties["title"], tc.title)
			}
			for _, n := range nodes {
				nb := must[Neighbourhood](t)(g.ViewNeighbourhood(ctx, tc.view, n.ID))
				want := must[Neighbourhood](t)(g.NodeNeighbourhood(ctx, tc.base, n.ID))
				if nb.Node.Version != n.Version || len(nb.Links) != len(want.Links) || len(nb.Nodes) != len(want.Nodes) {
					t.Fatalf("%s: v%d with %d links, want v%d with %d", n.Key, nb.Node.Version, len(nb.Links), n.Version, len(want.Links))
				}
				held := 0
				for _, l := range links {
					if l.From.ID == n.ID || l.To.ID == n.ID {
						held++
					}
				}
				if len(nb.Links) < held {
					t.Fatalf("%s: %d links, the baseline holds %d", n.Key, len(nb.Links), held)
				}
			}
			checkViewPages(t, g, tc.view, nodes)
		})
	}
	if _, err := g.ViewNode(ctx, View{}, f.req.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a read naming no view: %v", err)
	}
	if _, err := g.ViewNode(ctx, View{Change: c.ID, Namespace: "x"}, f.req.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a view naming a change and a branch: %v", err)
	}
	// what a view holds of some nodes (the index keeps the hits a view holds)
	ids := []domain.NodeID{f.req.ID, f.need.ID}
	st := must[ViewState](t)(g.ViewVersions(ctx, View{Namespace: f.req.Namespace, Branch: domain.MainBranch}, ids))
	if !st.MainHead || st.Baseline != after.ID || st.Versions[f.req.ID] != f.req.Version+1 || st.Versions[f.need.ID] != f.need.Version {
		t.Fatalf("head of main: %+v", st)
	}
	st = must[ViewState](t)(g.ViewVersions(ctx, View{Namespace: f.req.Namespace, Branch: domain.MainBranch, Baseline: before}, ids))
	if st.MainHead || st.Versions[f.req.ID] != f.req.Version {
		t.Fatalf("an older baseline: %+v", st)
	}
	d := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "next", BaselineID: after.ID}))
	must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, d.ID, NodeCheckout{Node: f.need.ID, Rationale: "draft"}))
	st = must[ViewState](t)(g.ViewVersions(ctx, View{Change: d.ID}, ids))
	if st.MainHead || st.Versions[f.need.ID] != 0 || st.Versions[f.req.ID] != f.req.Version+1 || st.Baseline != after.ID {
		t.Fatalf("a change: %+v", st)
	}
	if ds := must[[]domain.Node](t)(g.ViewDrafts(ctx, View{Change: d.ID}, "PAY", 10)); len(ds) != 1 || ds[0].Key != "NEED-1" || ds[0].Version != 0 {
		t.Fatalf("the drafts of a change: %+v", ds)
	}
	if ds := must[[]domain.Node](t)(g.ViewDrafts(ctx, View{Change: d.ID}, "nothing", 10)); len(ds) != 0 {
		t.Fatalf("no draft matches: %+v", ds)
	}
	// a branch is never assumed, and a baseline is read on its own branch only
	for _, v := range []View{{Baseline: before}, {Namespace: f.req.Namespace}, {Namespace: f.req.Namespace, Branch: "other", Baseline: before}} {
		if _, err := g.ViewNode(ctx, v, f.req.ID, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", v, err)
		}
	}
	if _, err := g.ViewNode(ctx, View{Namespace: f.req.Namespace, Branch: domain.MainBranch}, "", "REQ-404"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a key the view does not hold: %v", err)
	}
}

// BenchmarkViewReads measures the reads of ADR 0157 on SQLite over a tree of 8421 nodes, in a branch view and in the
// view of a change holding 20 drafts, against the whole reads they replace (BaselineGraph, ChangeGraph).
func BenchmarkViewReads(b *testing.B) {
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(b.TempDir(), "goap.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if err := platform.MigrateSQLite(ctx, db, "graph", SQLiteMigrations, "migrations_sqlite"); err != nil {
		b.Fatal(err)
	}
	g := New(NewSQLite(db))
	root := tree(b, g, 3, 20, 0)
	head, err := g.BranchHead(ctx, "", domain.MainBranch)
	if err != nil {
		b.Fatal(err)
	}
	c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "bench", BaselineID: head.ID, OwnBranch: true})
	if err != nil {
		b.Fatal(err)
	}
	for i := range 20 {
		key := fmt.Sprintf("R.%d", i)
		imp, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Key: key, Rationale: "bench"})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := g.edit(ctx, c.ID, imp.ID, edit{Properties: map[string]any{"title": "edited " + key}}); err != nil {
			b.Fatal(err)
		}
	}
	mid, err := g.NodeByKey(ctx, "", "R.3")
	if err != nil {
		b.Fatal(err)
	}
	views := []struct {
		name string
		v    View
	}{{"branch", View{Namespace: root.Namespace, Branch: domain.MainBranch}}, {"change", View{Change: c.ID}}}
	for _, vw := range views {
		b.Run(vw.name+"/ViewNode", func(b *testing.B) {
			for b.Loop() {
				if _, err := g.ViewNode(ctx, vw.v, "", "R.3.7"); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(vw.name+"/ViewNeighbourhood", func(b *testing.B) {
			for b.Loop() {
				if nb, err := g.ViewNeighbourhood(ctx, vw.v, mid.ID); err != nil || len(nb.Links) != 21 {
					b.Fatal(len(nb.Links), err)
				}
			}
		})
		b.Run(vw.name+"/ViewNodes-page", func(b *testing.B) {
			for b.Loop() {
				if _, err := g.ViewNodes(ctx, vw.v, NodeQuery{Text: "R.3.7", Limit: 50}); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(vw.name+"/ViewNodesOfType", func(b *testing.B) {
			for b.Loop() {
				if ns, err := g.ViewNodesOfType(ctx, vw.v, "Item"); err != nil || len(ns) != 8421 {
					b.Fatal(len(ns), err)
				}
			}
		})
	}
	b.Run("branch/BaselineGraph", func(b *testing.B) {
		for b.Loop() {
			if _, _, err := g.BaselineGraph(ctx, head.ID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("change/ChangeGraph", func(b *testing.B) {
		for b.Loop() {
			if _, _, err := g.ChangeGraph(ctx, c.ID, ""); err != nil {
				b.Fatal(err)
			}
		}
	})
}
