package graphsvc_test

import (
	"context"
	"testing"

	"github.com/zimwip/goap/internal/graphsvc"
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
