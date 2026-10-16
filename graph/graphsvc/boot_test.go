package graphsvc_test

import (
	"context"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/mcp"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graph/graphtest"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/adapter"
	"github.com/zimwip/goap/lib/llmcfg"
	"github.com/zimwip/goap/lib/mcpbuiltin"
)

func bootModels() graphsvc.ModelsConfig {
	return graphsvc.ModelsConfig{Providers: []llmcfg.Provider{{Name: "fake", Kind: "fake"}}}
}

// Boot writes the platform and nothing else: roots, floor policy, built-in MCPs and roles, models; a second call writes
// nothing; no demo data (the alm namespace stays empty) until the dev seed runs.
func TestBoot(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	r, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: bootModels()})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Access || !r.Builtins || !r.Models {
		t.Fatalf("report = %+v", r)
	}
	for _, c := range []struct{ ns, key string }{
		{access.NamespaceOrganisation, access.DefaultOrg},
		{access.NamespaceOrganisation, access.DefaultProject},
		{access.NamespaceOrganisation, access.PolicyKey(authz.FloorPolicies[0])},
		{domain.NamespacePlatform, mcp.MCPKey(mcpbuiltin.Graph)},
		{domain.NamespacePlatform, adapter.DefKey(mcpbuiltin.AdapterDefs()[0].Name)},
		{domain.NamespacePlatform, access.RoleKey(access.RoleAdmin)},
		{domain.NamespacePlatform, llmcfg.ProviderKey("fake")},
	} {
		if _, err := g.NodeByKey(ctx, c.ns, c.key); err != nil {
			t.Fatalf("%s/%s: %v", c.ns, c.key, err)
		}
	}
	// the document repository and the demo are not platform defaults
	if _, err := g.NodeByKey(ctx, domain.NamespacePlatform, mcp.MCPKey("document-repository")); err == nil {
		t.Fatal("Boot seeded the document-repository MCP")
	}
	if _, err := g.NodeByKey(ctx, "alm", "NEED-1"); err == nil {
		t.Fatal("Boot seeded the demo")
	}
	cs, _ := g.Changes(ctx)
	if again, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: bootModels()}); err != nil || again != (graphsvc.Report{}) {
		t.Fatalf("second boot = %+v, %v", again, err)
	}
	if after, _ := g.Changes(ctx); len(after) != len(cs) {
		t.Fatalf("a second boot wrote changes: %d -> %d", len(cs), len(after))
	}
	if err := demo(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := g.NodeByKey(ctx, "alm", "NEED-1"); err != nil {
		t.Fatalf("development data after Boot: %v", err)
	}
}

// demo stands for the development data a composition passes as Options.Dev (internal/devseed, which the graph's
// tests do not import).
func demo(ctx context.Context, g *graph.Graph) error {
	_, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "NEED-1", Type: "alm@Need", Properties: map[string]any{"title": "Pay online"}})
	return err
}

// Options.Dev runs after the platform steps.
func TestBootRunsDevLast(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	_, err := graphsvc.Boot(ctx, g, graphsvc.Options{Dev: func(ctx context.Context, g *graph.Graph) error {
		if _, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.PolicyKey(authz.FloorPolicies[0])); err != nil {
			return err
		}
		return demo(ctx, g)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.NodeByKey(ctx, "alm", "NEED-1"); err != nil {
		t.Fatal(err)
	}
}

// A composition that serves callers may not boot before its hooks are set.
func TestBootRequiresHooks(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Boot without hooks must panic when RequireHooks is set")
		}
	}()
	_, _ = graphsvc.Boot(context.Background(), typedGraph(t), graphsvc.Options{RequireHooks: true})
}
