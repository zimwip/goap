package graphsvc_test

import (
	"context"
	"testing"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

// typedGraph is a graph judged by the domains of the repository (ADR 0012).
func typedGraph(t *testing.T) *graph.Graph {
	t.Helper()
	ds, err := methodology.LoadDomains("../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	return g
}

// The seeds only write nodes and links the domains of the repository declare.
func TestSeedsFollowTheDomains(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if _, err := graphsvc.SeedDemo(ctx, g); err != nil {
		t.Fatal(err)
	}
	if again, err := graphsvc.SeedDemo(ctx, g); err != nil || again {
		t.Fatalf("the demo is seeded once: %v %v", again, err)
	}
	if _, err := graphsvc.SeedAccess(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	unit, err := g.NodeByKey(ctx, "organisation", "ORG-CHECKOUT")
	if err != nil || unit.Type != mcp.NodeTypeOrgUnit {
		t.Fatalf("unit = %+v, %v", unit, err)
	}
	app, err := g.NodeByKey(ctx, "alm", "APP-1")
	if err != nil || app.Type != "alm@Application" {
		t.Fatalf("app = %+v, %v", app, err)
	}
	v, err := g.View(ctx, app.Ref())
	if err != nil {
		t.Fatal(err)
	}
	owned := false
	for _, l := range v.Out {
		if l.Type == mcp.LinkOwner && l.To.ID == unit.ID {
			owned = true
		}
	}
	if !owned {
		t.Fatalf("APP-1 must be owned by ORG-CHECKOUT across namespaces: %+v", v.Out)
	}
	uv, _ := g.View(ctx, unit.Ref())
	if len(uv.Out) != 1 || uv.Out[0].Type != mcp.LinkPartOf {
		t.Fatalf("unit hierarchy: %+v", uv.Out)
	}
}

// SeedDemo creates ORG-ACME before the default organisation exists, through the raw write path that
// bypasses checkRequiredParent (ADR 0040): without LinkOrphanUnits it stays a second root beside
// ORG-DEFAULT, which is why the navigation tree would show it ahead of the default organisation.
func TestLinkOrphanUnits(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if _, err := graphsvc.SeedDemo(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	acme, err := g.NodeByKey(ctx, "organisation", "ORG-ACME")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := g.View(ctx, acme.Ref()); len(v.Out) != 0 {
		t.Fatalf("ORG-ACME should still be rootless before LinkOrphanUnits: %+v", v.Out)
	}
	if linked, err := graphsvc.LinkOrphanUnits(ctx, g); err != nil || !linked {
		t.Fatalf("first link = %v, %v", linked, err)
	}
	def, err := g.NodeByKey(ctx, "organisation", domain.DefaultOrg)
	if err != nil {
		t.Fatal(err)
	}
	acme, err = g.NodeByKey(ctx, "organisation", "ORG-ACME")
	if err != nil {
		t.Fatal(err)
	}
	v, err := g.View(ctx, acme.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Out) != 1 || v.Out[0].Type != mcp.LinkPartOf || v.Out[0].To.ID != def.ID {
		t.Fatalf("ORG-ACME must be part_of ORG-DEFAULT: %+v", v.Out)
	}
	if again, err := graphsvc.LinkOrphanUnits(ctx, g); err != nil || again {
		t.Fatalf("second link = %v, %v", again, err)
	}
}

// The built-in MCPs follow the code at every start; the instances of the default organisation are
// seeded once, so that removing one sticks (ADR 0028).
func TestSeedBuiltins(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	if seeded, err := graphsvc.SeedBuiltins(ctx, g); err != nil || !seeded {
		t.Fatalf("first seed = %v, %v", seeded, err)
	}
	if again, err := graphsvc.SeedBuiltins(ctx, g); err != nil || again {
		t.Fatalf("second seed = %v, %v", again, err)
	}
	commit := func(ns string, e graph.NodeEdit) {
		t.Helper()
		head, err := g.BranchHead(ctx, ns, domain.MainBranch)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.Commit(ctx, graph.Commit{Namespace: ns, Title: "edit", Intent: "edit", Baseline: head.ID, By: "test", Edits: []graph.NodeEdit{e}}); err != nil {
			t.Fatal(err)
		}
	}
	// an older platform: the MCP lacks a tool and says something else
	n, err := g.NodeByKey(ctx, mcp.NamespacePlatform, mcp.MCPKey(mcp.BuiltinGraph))
	if err != nil {
		t.Fatal(err)
	}
	pre := n.Ref()
	commit(mcp.NamespacePlatform, graph.NodeEdit{Pre: &pre, Props: map[string]any{"description": "old", "tools": []any{}}, Rationale: "older"})
	// an administrator removes the admin MCP from the default organisation
	a, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, mcp.AdapterKey(domain.DefaultOrg, mcp.BuiltinAdmin))
	if err != nil {
		t.Fatal(err)
	}
	apre := a.Ref()
	commit(mcp.NamespaceOrganisation, graph.NodeEdit{Pre: &apre, Retire: true, Rationale: "no admin tools"})

	if seeded, err := graphsvc.SeedBuiltins(ctx, g); err != nil || !seeded {
		t.Fatalf("resync = %v, %v", seeded, err)
	}
	head, _ := g.BranchHead(ctx, mcp.NamespacePlatform, domain.MainBranch)
	nodes, _, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.Key == mcp.MCPKey(mcp.BuiltinGraph) {
			d, err := mcp.DefFromProps(n.Properties)
			if err != nil || d.Description != mcp.BuiltinDefs()[0].Description || len(d.Tools) != len(mcp.BuiltinDefs()[0].Tools) {
				t.Fatalf("goap-graph not resynced: %+v %v", d, err)
			}
		}
	}
	orgHead, _ := g.BranchHead(ctx, mcp.NamespaceOrganisation, domain.MainBranch)
	orgNodes, _, _ := g.BaselineGraph(ctx, orgHead.ID)
	for _, n := range orgNodes {
		if n.Key == mcp.AdapterKey(domain.DefaultOrg, mcp.BuiltinAdmin) && !n.Deleted {
			t.Fatal("the removed instance was seeded again")
		}
	}
}

// The root project is seeded like ORG-DEFAULT, self-linked project_part_of, and seeding is idempotent.
func TestRootProjectSeeded(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	root, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, domain.DefaultProject)
	if err != nil || root.Type != access.NodeTypeProjectUnit {
		t.Fatalf("root project = %+v, %v", root, err)
	}
	v, err := g.View(ctx, root.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Out) != 1 || v.Out[0].Type != access.LinkProjectPartOf || v.Out[0].To.ID != root.ID {
		t.Fatalf("the root project must link project_part_of to itself: %+v", v.Out)
	}
	if again, err := graphsvc.SeedDefaults(ctx, g); err != nil || again {
		t.Fatalf("seeding twice must not touch the root project again: %v %v", again, err)
	}
}
