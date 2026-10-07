package graphsvc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llmcfg"
)

func aliasOf(t *testing.T, g *graph.Graph, name string) (llmcfg.Alias, domain.Node) {
	t.Helper()
	n, err := g.NodeByKey(context.Background(), domain.NamespacePlatform, llmcfg.AliasKey(name))
	if err != nil {
		t.Fatalf("alias %s: %v", name, err)
	}
	a, err := llmcfg.AliasFromProps(n.Properties)
	if err != nil {
		t.Fatal(err)
	}
	return a, n
}

func modelsWithAliases() graphsvc.ModelsConfig {
	return graphsvc.ModelsConfig{
		Providers: []llmcfg.Provider{{Name: "fake", Kind: "fake", Protocol: "fake", Enabled: true}},
		Models: []llmcfg.Model{
			{Provider: "fake", Model: "echo", Enabled: true},
			{Provider: "fake", Model: "small", Enabled: true},
		},
		Aliases: []llmcfg.Alias{{Alias: "default", Target: "fake/echo"}, {Alias: "fast", Target: "fake/small"}},
	}
}

// Boot gives every install the protected aliases: the assistant like default, the helper like fast; a second boot writes nothing.
func TestBootSeedsProtectedAliases(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	r, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: modelsWithAliases()})
	if err != nil || !r.Aliases {
		t.Fatalf("boot = %+v, %v", r, err)
	}
	if a, _ := aliasOf(t, g, llmcfg.AssistantAlias); !a.Protected || a.Target != "fake/echo" {
		t.Fatalf("assistant = %+v", a)
	}
	if a, _ := aliasOf(t, g, llmcfg.HelperAlias); !a.Protected || a.Target != "fake/small" {
		t.Fatalf("helper = %+v", a)
	}
	cs, _ := g.Changes(ctx)
	if again, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: modelsWithAliases()}); err != nil || again != (graphsvc.Report{}) {
		t.Fatalf("second boot = %+v, %v", again, err)
	}
	if after, _ := g.Changes(ctx); len(after) != len(cs) {
		t.Fatalf("a second boot wrote changes: %d -> %d", len(cs), len(after))
	}
}

// With no model configured the aliases exist, target nothing, and the snapshot keeps them without a problem.
func TestProtectedAliasesWithoutModels(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if _, err := graphsvc.Boot(ctx, g, graphsvc.Options{}); err != nil {
		t.Fatal(err)
	}
	a, n := aliasOf(t, g, llmcfg.AssistantAlias)
	if !a.Protected || a.Target != "" {
		t.Fatalf("assistant = %+v", a)
	}
	s := llmcfg.BuildSnapshot("", []domain.Node{n}, nil)
	if len(s.Problems) != 0 || len(s.Aliases) != 1 {
		t.Fatalf("snapshot = %+v", s)
	}
}

// A graph that predates the aliases (models seeded, no protected alias) gets them at the next boot.
func TestProtectedAliasesOnExistingGraph(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := modelsWithAliases()
	if _, err := graphsvc.SeedModels(ctx, g, cfg.Providers, cfg.Models, cfg.Aliases); err != nil {
		t.Fatal(err)
	}
	if _, err := g.NodeByKey(ctx, domain.NamespacePlatform, llmcfg.AliasKey(llmcfg.AssistantAlias)); err == nil {
		t.Fatal("alias present before the seed")
	}
	if r, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: cfg}); err != nil || !r.Aliases {
		t.Fatalf("boot = %+v, %v", r, err)
	}
	if a, _ := aliasOf(t, g, llmcfg.HelperAlias); !a.Protected || a.Target != "fake/small" {
		t.Fatalf("helper = %+v", a)
	}
}

// A change may retarget a protected alias, never retire it, rename it or drop its flag.
func TestProtectedAliasGuard(t *testing.T) {
	ctx := graphsvc.System(context.Background())
	g := typedGraph(t)
	g.Validators = []graph.NodeValidator{llmcfg.ProtectedAliasValidator{}}
	if _, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: modelsWithAliases()}); err != nil {
		t.Fatal(err)
	}
	edit := func(e graph.NodeEdit) error {
		_, n := aliasOf(t, g, llmcfg.AssistantAlias)
		pre := n.Ref()
		e.Pre = &pre
		return graphsvc.SeedChange(ctx, g, domain.NamespacePlatform, "edit", []graph.NodeEdit{e})
	}
	for name, e := range map[string]graph.NodeEdit{
		"retire":      {State: "retired"},
		"rename":      {Props: map[string]any{"alias": "other"}},
		"flag clear":  {Props: map[string]any{"protected": nil}},
		"flag false":  {Props: map[string]any{"protected": false}},
		"rename+move": {Props: map[string]any{"alias": "x"}, State: "retired"},
	} {
		if err := edit(e); !errors.Is(err, graph.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if err := edit(graph.NodeEdit{Props: map[string]any{"target": "fake/small"}}); err != nil {
		t.Fatalf("retarget: %v", err)
	}
	if a, n := aliasOf(t, g, llmcfg.AssistantAlias); a.Target != "fake/small" || !a.Protected || n.State == "retired" {
		t.Fatalf("assistant = %+v (%s)", a, n.State)
	}
	// a plain alias stays free
	if err := graphsvc.SeedChange(ctx, g, domain.NamespacePlatform, "plain", []graph.NodeEdit{
		graphsvc.SeedNode(llmcfg.AliasKey("mine"), llmcfg.NodeTypeAlias, llmcfg.Alias{Alias: "mine", Target: "fake/echo"}.Props())}); err != nil {
		t.Fatal(err)
	}
	_, n := aliasOf(t, g, "mine")
	pre := n.Ref()
	if err := graphsvc.SeedChange(ctx, g, domain.NamespacePlatform, "retire", []graph.NodeEdit{{Pre: &pre, State: "retired"}}); err != nil {
		t.Fatalf("retire a plain alias: %v", err)
	}
	// the platform names cannot be created unprotected
	g2 := typedGraph(t)
	g2.Validators = g.Validators
	if err := g2.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	err := graphsvc.SeedChange(ctx, g2, domain.NamespacePlatform, "bad", []graph.NodeEdit{
		graphsvc.SeedNode(llmcfg.AliasKey("helper"), llmcfg.NodeTypeAlias, llmcfg.Alias{Alias: "helper", Target: "fake/echo"}.Props())})
	if !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("unprotected helper: %v", err)
	}
}
