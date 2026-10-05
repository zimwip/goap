package graphsvc_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zimwip/goap/internal/devseed"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/adapter"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llmcfg"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/mcpbuiltin"
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
	if _, err := devseed.Demo(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := g.NodeByKey(ctx, "alm", "NEED-1"); err != nil {
		t.Fatalf("the demo after Boot: %v", err)
	}
}

// Options.Dev runs after the platform steps.
func TestBootRunsDevLast(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	_, err := graphsvc.Boot(ctx, g, graphsvc.Options{Dev: func(ctx context.Context, g *graph.Graph) error {
		if _, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.PolicyKey(authz.FloorPolicies[0])); err != nil {
			return err
		}
		_, err := devseed.Demo(ctx, g)
		return err
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

// Both compositions boot through graphsvc.Boot: neither spells the platform seeds by hand.
func TestCompositionsUseBoot(t *testing.T) {
	for _, dir := range []string{"../../cmd/goap-dev", "../../cmd/graph"} {
		// a composition may spread its wiring over several files of its package (cmd/goap-dev, ADR 0073)
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: no source (%v)", dir, err)
		}
		path, src := dir, ""
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src += string(b)
		}
		if !strings.Contains(src, "graphsvc.Boot(") {
			t.Errorf("%s does not call graphsvc.Boot", path)
		}
		for _, seed := range []string{"SeedAccess", "SeedBuiltins", "SeedModels", "SeedDefaults", "SeedDemo"} {
			if strings.Contains(src, "graphsvc."+seed) {
				t.Errorf("%s calls graphsvc.%s directly", path, seed)
			}
		}
	}
}
