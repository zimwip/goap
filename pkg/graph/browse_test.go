package graph

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestBrowseBaseline(t *testing.T) { forEachRepo(t, testBrowseBaseline) }

func testBrowseBaseline(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)

	var refs []domain.NodeRef
	for i := range 12 {
		n, err := importNode(ctx, g, newNode{Key: fmt.Sprintf("REQ-%02d", i), Type: "Req", Properties: map[string]any{"title": fmt.Sprintf("req %d", i)}})
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, n.Ref())
	}
	test, err := importNode(ctx, g, newNode{Key: "TEST-1", Type: "Test", Properties: map[string]any{"title": "login works"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importNode(ctx, g, newNode{Key: "TEST-2", Type: "Test"}); err != nil {
		t.Fatal(err)
	}
	verifies, err := importLink(ctx, g, "verifies", test.Ref(), refs[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	// req 0 changes after the link: the incoming link now targets an earlier version (suspect)
	req0, err := importProps(ctx, g, refs[0], map[string]any{"title": "req 0 bis"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importLink(ctx, g, "depends", req0.Ref(), refs[1], nil); err != nil {
		t.Fatal(err)
	}
	if req0, err = g.Node(ctx, domain.NodeRef{ID: req0.ID}); err != nil {
		t.Fatal(err)
	}
	b, err := g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}

	page, err := g.BaselineNodes(ctx, b.ID, NodeQuery{Type: "Req", Offset: 10, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 12 || len(page.Nodes) != 2 || page.Nodes[0].Key != "REQ-10" {
		t.Fatalf("page = total %d, %d nodes", page.Total, len(page.Nodes))
	}
	if len(page.Types) != 2 || page.Types[0] != (TypeCount{"Req", 12}) || page.Types[1] != (TypeCount{"Test", 2}) {
		t.Fatalf("types = %v", page.Types)
	}
	page, err = g.BaselineNodes(ctx, b.ID, NodeQuery{Text: "LOGIN"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Nodes[0].Key != "TEST-1" || len(page.Types) != 1 {
		t.Fatalf("text filter = %+v", page)
	}

	nb, err := g.NodeNeighbourhood(ctx, b.ID, req0.ID)
	if err != nil {
		t.Fatal(err)
	}
	if nb.Node.Version != req0.Version || len(nb.Nodes) != 2 || len(nb.Links) != 2 {
		t.Fatalf("neighbourhood = v%d, %d nodes, %d links", nb.Node.Version, len(nb.Nodes), len(nb.Links))
	}
	keys := map[string]bool{}
	for _, n := range nb.Nodes {
		keys[n.Key] = true
	}
	if !keys["TEST-1"] || !keys["REQ-01"] || keys["TEST-2"] {
		t.Fatalf("neighbours = %v", keys)
	}
	if len(nb.Suspect) != 1 || nb.Suspect[0] != verifies.ID {
		t.Fatalf("suspect = %v", nb.Suspect)
	}
	// seen from the test, the suspect link leads to the baseline's version of req 0
	nb, err = g.NodeNeighbourhood(ctx, b.ID, test.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nb.Nodes) != 1 || nb.Nodes[0].Version != req0.Version || len(nb.Suspect) != 1 {
		t.Fatalf("neighbourhood of the test = %+v", nb)
	}
	if _, err := g.NodeNeighbourhood(ctx, b.ID, "missing"); err == nil {
		t.Fatal("unknown node: no error")
	}

	// the namespace of the test, and the one of the roots of the organisation and the projects (ADR 0054)
	ns, err := g.Namespaces(ctx)
	if err != nil || !slices.Equal(ns, []string{domain.DefaultNamespace, "organisation"}) {
		t.Fatalf("namespaces = %v, %v", ns, err)
	}
}
