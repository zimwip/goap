package enginee2e

import (
	"context"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/intent"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graph/graphtest"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/typecat"
	"github.com/zimwip/goap/platform/devseed"
)

// A request without a baseline starts from the latest baseline of its methodology's namespace (the assistant only
// knows the methodology once the intent is identified).
func TestStartWithoutBaseline(t *testing.T) {
	ctx := authz.With(context.Background(), authz.Principal{Subject: "dev", Org: "acme", Roles: []string{"contributor"}})
	m, err := methodology.LoadFile("../../../methodologies/sdlc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	ds, err := def.LoadDomains("../../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	e := &engine.Engine{
		Graph:         g,
		Methodologies: engine.StaticMethodologies{cm.Definition().Name: cm},
		Intent:        intent.Resolver{Ranker: intent.Lexical{}},
		Store:         engine.NewMemoryStore(),
		Types:         func() def.TypeSet { return cat },
	}
	req := engine.StartRequest{Methodology: "sdlc", Agent: "delivery", Goal: "deliver", Intent: "Allow payment in 3 installments", ProjectID: testProject}

	if _, err := devseed.Demo(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := graphtest.Project(ctx, g, testProject, "Test"); err != nil {
		t.Fatal(err)
	}
	bs, err := g.Baselines(ctx, cm.Definition().Namespace)
	if err != nil || len(bs) == 0 {
		t.Fatalf("baselines of %s: %v, %v", cm.Definition().Namespace, bs, err)
	}
	latest := bs[len(bs)-1]
	p, err := e.Start(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.Change(ctx, p.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	if p.BaselineID != latest.ID || c.BaselineID != latest.ID || c.Namespace != cm.Definition().Namespace {
		t.Fatalf("process baseline %s, change baseline %s (%s), want %s (%s)", p.BaselineID, c.BaselineID, c.Namespace, latest.ID, cm.Definition().Namespace)
	}
}
