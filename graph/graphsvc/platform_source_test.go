package graphsvc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/lnk"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
)

// The platform graph's source is the bootstrap's: no change edits, retargets or retires it; the other sources stay free.
func TestPlatformSourceGuard(t *testing.T) {
	ctx := graphsvc.System(context.Background())
	g := typedGraph(t)
	g.Validators = []graph.NodeValidator{graphsvc.PlatformSourceValidator{}}
	content := lnk.Source{Name: "content", Kind: lnk.KindGraph, Location: "http://content", Namespaces: []string{"alm"}}
	if _, err := graphsvc.Boot(ctx, g, graphsvc.Options{Sources: []lnk.Source{content}}); err != nil {
		t.Fatal(err)
	}
	edit := func(name string, e graph.NodeEdit) error {
		n, err := g.NodeByKey(ctx, domain.NamespacePlatform, domain.SourceKey(name))
		if err != nil {
			t.Fatal(err)
		}
		pre := n.Ref()
		e.Pre = &pre
		return graphsvc.SeedChange(ctx, g, domain.NamespacePlatform, "edit", []graph.NodeEdit{e})
	}
	for what, e := range map[string]graph.NodeEdit{
		"retire":      {State: "retired"},
		"describe":    {Props: map[string]any{"description": "mine"}},
		"relocate":    {Props: map[string]any{"location": "http://elsewhere"}},
		"hold":        {Props: map[string]any{"namespaces": []any{"alm"}}},
		"change kind": {Props: map[string]any{"kind": "files"}},
	} {
		if err := edit(graphsvc.PlatformSource, e); !errors.Is(err, graph.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", what, err)
		}
	}
	if err := edit("content", graph.NodeEdit{Props: map[string]any{"description": "the content graph"}}); err != nil {
		t.Fatalf("edit content: %v", err)
	}
	if err := edit("content", graph.NodeEdit{State: "retired"}); err != nil {
		t.Fatalf("retire content: %v", err)
	}
	// created elsewhere than as this graph: refused
	g2 := typedGraph(t)
	g2.Validators = g.Validators
	if err := g2.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	bad := graphsvc.PlatformSourceOf()
	bad.Location = "http://elsewhere"
	err := graphsvc.SeedChange(ctx, g2, domain.NamespacePlatform, "bad", []graph.NodeEdit{
		graphsvc.SeedNode(domain.SourceKey(bad.Name), domain.TypeSource, graphsvc.SourceProps(bad))})
	if !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("platform source at a location: %v", err)
	}
}
