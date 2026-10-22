package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
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
