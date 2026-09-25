package graphsvc_test

import (
	"context"
	"testing"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

func TestSeedOrganisationNamespace(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	if _, err := graphsvc.SeedDemo(ctx, g); err != nil {
		t.Fatal(err)
	}
	unit, err := g.NodeByKey(ctx, "organisation", "ORG-CHECKOUT")
	if err != nil || unit.Namespace != "organisation" || unit.Type != "OrgUnit" {
		t.Fatalf("unit = %+v, %v", unit, err)
	}
	app, err := g.NodeByKey(ctx, "", "APP-1")
	if err != nil || app.Namespace != domain.DefaultNamespace {
		t.Fatalf("app = %+v, %v", app, err)
	}
	v, err := g.View(ctx, app.Ref())
	if err != nil {
		t.Fatal(err)
	}
	owned := false
	for _, l := range v.Out {
		if l.Type == "owner" && l.To.ID == unit.ID {
			owned = true
		}
	}
	if !owned {
		t.Fatalf("APP-1 must be owned by ORG-CHECKOUT across namespaces: %+v", v.Out)
	}
	uv, _ := g.View(ctx, unit.Ref())
	if len(uv.Out) != 1 || uv.Out[0].Type != "part_of" {
		t.Fatalf("unit hierarchy: %+v", uv.Out)
	}
}
