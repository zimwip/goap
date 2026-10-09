package graphsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/zimwip/goap/gen/goap/change/v1/changev1connect"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/adapter"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/mcpbuiltin"
	"github.com/zimwip/goap/pkg/typecat"
)

// typedGraph is a graph judged by the domains of the repository (ADR 0012).
func typedGraph(t *testing.T) *graph.Graph {
	t.Helper()
	ds, err := def.LoadDomains("../../domains")
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

// The built-in MCPs follow the code at every start; the instances of the default organisation are
// seeded once, so that removing one sticks (ADR 0028).
func TestSeedBuiltins(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if err := g.Bootstrap(ctx); err != nil {
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
		if _, err := g.Commit(ctx, graph.Commit{ProjectID: "PROJ-ROOT", Namespace: ns, Title: "edit", Intent: "edit", Baseline: head.ID, By: "test", Edits: []graph.NodeEdit{e}}); err != nil {
			t.Fatal(err)
		}
	}
	// an older platform: the MCP lacks a tool and says something else
	n, err := g.NodeByKey(ctx, domain.NamespacePlatform, mcp.MCPKey(mcpbuiltin.Graph))
	if err != nil {
		t.Fatal(err)
	}
	pre := n.Ref()
	commit(domain.NamespacePlatform, graph.NodeEdit{Pre: &pre, Props: map[string]any{"description": "old", "tools": []any{}}, Rationale: "older"})
	// an administrator disables the admin MCP for the default organisation (a node is never removed by a change, ADR 0076)
	a, err := g.NodeByKey(ctx, access.NamespaceOrganisation, adapter.Key(access.DefaultOrg, mcpbuiltin.Admin))
	if err != nil {
		t.Fatal(err)
	}
	apre := a.Ref()
	commit(access.NamespaceOrganisation, graph.NodeEdit{Pre: &apre, Props: map[string]any{"disabled": true}, Rationale: "no admin tools"})

	if seeded, err := graphsvc.SeedBuiltins(ctx, g); err != nil || !seeded {
		t.Fatalf("resync = %v, %v", seeded, err)
	}
	head, _ := g.BranchHead(ctx, domain.NamespacePlatform, domain.MainBranch)
	nodes, _, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.Key == mcp.MCPKey(mcpbuiltin.Graph) {
			d, err := mcp.DefFromProps(n.Properties)
			if err != nil || d.Description != mcpbuiltin.Defs()[0].Description || len(d.Tools) != len(mcpbuiltin.Defs()[0].Tools) {
				t.Fatalf("goap-graph not resynced: %+v %v", d, err)
			}
		}
	}
	orgHead, _ := g.BranchHead(ctx, access.NamespaceOrganisation, domain.MainBranch)
	orgNodes, _, _ := g.BaselineGraph(ctx, orgHead.ID)
	for _, n := range orgNodes {
		if n.Key == adapter.Key(access.DefaultOrg, mcpbuiltin.Admin) && n.Properties["disabled"] != true {
			t.Fatalf("the disabled instance was seeded again: %+v", n.Properties)
		}
	}
}

// The root project is seeded like ORG-DEFAULT, self-linked project_part_of, and seeding is idempotent.
func TestRootProjectSeeded(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	root, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.DefaultProject)
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
	before, _ := g.Changes(ctx)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrapping twice must not fail: %v", err)
	}
	if after, _ := g.Changes(ctx); len(after) != len(before) {
		t.Fatalf("bootstrapping twice must not write again: %d -> %d changes", len(before), len(after))
	}
}

// The services reading the organisation learn its structures from the graph service (ADR 0054): the client returns
// what the graph's catalogue tags, a User being a unit.
func TestStructuresThroughTheService(t *testing.T) {
	g := typedGraph(t)
	path, handler := graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.Handle(changev1connect.NewChangeServiceHandler(&graphsvc.Handler{Graph: g}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	got, err := graphsvc.NewClient(srv.Client(), srv.URL).Structures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, typecat.Builtin().Structures()) {
		t.Fatalf("structures = %+v", got)
	}
}
