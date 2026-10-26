package devseed_test

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/mcp"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/adapter"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/typecat"
	"github.com/zimwip/goap/platform/devseed"
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

// The seeds only write nodes and links the domains of the repository declare.
func TestSeedsFollowTheDomains(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if _, err := devseed.Demo(ctx, g); err != nil {
		t.Fatal(err)
	}
	if again, err := devseed.Demo(ctx, g); err != nil || again {
		t.Fatalf("the demo is seeded once: %v %v", again, err)
	}
	if _, err := graphsvc.SeedAccess(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	unit, err := g.NodeByKey(ctx, "organisation", "ORG-CHECKOUT")
	if err != nil || unit.Type != access.NodeTypeOrgUnit {
		t.Fatalf("unit = %+v, %v", unit, err)
	}
	app, err := g.NodeByKey(ctx, "alm", "APP-1")
	if err != nil || app.Type != "alm@Application" {
		t.Fatalf("app = %+v, %v", app, err)
	}
	if app.Responsible.Key != unit.Key {
		t.Fatalf("APP-1 must be owned by ORG-CHECKOUT across namespaces: %+v", app)
	}
	uv, _ := g.View(ctx, unit.Ref())
	if len(uv.Out) != 1 || uv.Out[0].Type != access.LinkPartOf {
		t.Fatalf("unit hierarchy: %+v", uv.Out)
	}
}

// The demo seed runs after the bootstrap (ADR 0054): its organisation hangs under the root unit from the start, and
// every one of its writes is an applied change held by a unit and acting in a project.
func TestSeedDemoHangsUnderTheRoot(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if _, err := devseed.Demo(ctx, g); err != nil {
		t.Fatal(err)
	}
	def, err := g.NodeByKey(ctx, "organisation", access.DefaultOrg)
	if err != nil {
		t.Fatal(err)
	}
	acme, err := g.NodeByKey(ctx, "organisation", "ORG-ACME")
	if err != nil {
		t.Fatal(err)
	}
	v, err := g.View(ctx, acme.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Out) != 1 || v.Out[0].Type != access.LinkPartOf || v.Out[0].To.ID != def.ID {
		t.Fatalf("ORG-ACME must be part_of ORG-DEFAULT: %+v", v.Out)
	}
	if acme.Responsible.Key != def.Key || acme.ChangeID == "" || acme.Project.IsZero() {
		t.Fatalf("ORG-ACME is owned by the root unit, written by a change, created in a project: %+v", acme)
	}
	cs, err := g.Changes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.OwnerOrg.IsZero() || c.ProjectID.IsZero() {
			t.Fatalf("change %s %q names no unit or no project", c.ID, c.Title)
		}
	}
}

// The demo import is one change per namespace (the organisation, the alm data with its links), not one per node.
func TestSeedDemoIsOneChangePerNamespace(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if _, err := devseed.Demo(ctx, g); err != nil {
		t.Fatal(err)
	}
	cs, err := g.Changes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var imports []string
	for _, c := range cs {
		if strings.HasPrefix(c.Title, "Import ") {
			imports = append(imports, c.Title)
		}
	}
	if len(imports) != 2 {
		t.Fatalf("the demo import is 2 changes, got %v", imports)
	}
	cmp, err := g.NodeByKey(ctx, "alm", "CMP-1")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := g.OutLinksOf(ctx, cmp.Ref()); err != nil || len(out) == 0 {
		t.Fatalf("the links come with the import: %v %v", out, err)
	}
}

// The document repository is development data: seeded once, with its adapter definition, and a directory becomes the
// default organisation's repository only when asked.
func TestDocumentRepositoryAndLocalFS(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if seeded, err := devseed.DocumentRepository(ctx, g); err != nil || !seeded {
		t.Fatalf("first seed = %v, %v", seeded, err)
	}
	if seeded, err := devseed.DocumentRepository(ctx, g); err != nil || seeded {
		t.Fatalf("second seed = %v, %v", seeded, err)
	}
	for _, key := range []string{mcp.MCPKey("document-repository"), adapter.DefKey(devseed.LocalFSAdapterName)} {
		if _, err := g.NodeByKey(ctx, domain.NamespacePlatform, key); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	for range 2 {
		if err := devseed.LocalFS(ctx, g, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.NodeByKey(ctx, access.NamespaceOrganisation, adapter.Key(access.DefaultOrg, "document-repository")); err != nil {
		t.Fatal(err)
	}
}

// The demo project hangs under the root project, is bound to the demo organisation by an assignment, and the alm data
// of the demo acts in it.
func TestSeedDemoProjectBoundToTheOrganisation(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if _, err := devseed.Demo(ctx, g); err != nil {
		t.Fatal(err)
	}
	project, err := g.NodeByKey(ctx, access.NamespaceOrganisation, devseed.DemoProject)
	if err != nil || project.Type != access.NodeTypeProjectUnit {
		t.Fatalf("project = %+v, %v", project, err)
	}
	asg, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.AssignmentKey("ORG-ACME", devseed.DemoProject))
	if err != nil {
		t.Fatal(err)
	}
	acme, _ := g.NodeByKey(ctx, access.NamespaceOrganisation, "ORG-ACME")
	out, err := g.OutLinksOf(ctx, asg.Ref())
	if err != nil {
		t.Fatal(err)
	}
	to := map[string]domain.NodeID{}
	for _, l := range out {
		to[l.Type] = l.To.ID
	}
	if to[access.LinkAssignsOrg] != acme.ID || to[access.LinkAssignsProject] != project.ID {
		t.Fatalf("the assignment binds ORG-ACME to the demo project: %+v", out)
	}
	need, err := g.NodeByKey(ctx, "alm", "NEED-1")
	if err != nil {
		t.Fatal(err)
	}
	if need.Project.Key != project.Key {
		t.Fatalf("the alm data acts in the demo project: %q", need.Project)
	}
}

// A graph holding the demo of before the demo project gains the project, and nothing else is written again.
func TestSeedDemoAddsTheProjectToAnOlderDemo(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][3]string{{"ORG-ACME", "company", ""}, {"ORG-DIGITAL", "direction", "ORG-ACME"}, {"ORG-CHECKOUT", "team", "ORG-DIGITAL"},
		{"ORG-CRM", "team", "ORG-DIGITAL"}, {"ORG-FINANCE", "team", "ORG-DIGITAL"}, {"ORG-SECURITY", "team", "ORG-DIGITAL"}} {
		if err := graphsvc.SeedUnit(ctx, g, u[0], u[0], u[1], u[2]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := devseed.Demo(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.AssignmentKey("ORG-ACME", devseed.DemoProject)); err != nil {
		t.Fatal(err)
	}
	if again, err := devseed.Demo(ctx, g); err != nil || again {
		t.Fatalf("seeded once: %v %v", again, err)
	}
}
