package engine_test

import (
	"context"
	"testing"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

// A request without a baseline starts from the latest baseline of its methodology's namespace (the assistant only
// knows the methodology once the intent is identified).
func TestStartWithoutBaseline(t *testing.T) {
	ctx := authz.With(context.Background(), authz.Principal{Subject: "dev", Org: "acme", Roles: []string{"contributor"}})
	m, err := methodology.LoadFile("../../methodologies/sdlc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
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
	e := &engine.Engine{
		Graph:         g,
		Methodologies: engine.StaticMethodologies{cm.Name: cm},
		Intent:        intent.Resolver{Ranker: intent.Lexical{}},
		Store:         engine.NewMemoryStore(),
		Types:         func() def.TypeSet { return cat },
	}
	req := engine.StartRequest{Methodology: "sdlc", Agent: "delivery", Goal: "deliver", Intent: "Allow payment in 3 installments", ProjectID: testProject}

	if _, err := graphsvc.SeedDemo(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CreateNode(ctx, graph.NewNode{Namespace: "organisation", Key: testProject, Type: "organisation@ProjectUnit", Properties: map[string]any{"name": "Test"}}); err != nil {
		t.Fatal(err)
	}
	bs, err := g.Baselines(ctx, cm.Namespace)
	if err != nil || len(bs) == 0 {
		t.Fatalf("baselines of %s: %v, %v", cm.Namespace, bs, err)
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
	if p.BaselineID != latest.ID || c.BaselineID != latest.ID || c.Namespace != cm.Namespace {
		t.Fatalf("process baseline %s, change baseline %s (%s), want %s (%s)", p.BaselineID, c.BaselineID, c.Namespace, latest.ID, cm.Namespace)
	}
}
