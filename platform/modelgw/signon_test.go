package modelgw

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/llmcfg"
	"github.com/zimwip/goap/lib/typecat"
)

func envOf(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

// A provider is detected for each kind one of whose well-known variables is set, its key referenced by every variable of
// the kind; an empty variable is not a key.
func TestDetectProviders(t *testing.T) {
	got := DetectProviders(envOf(map[string]string{"OPENAI_KEY": "k", "MISTRAL_API_KEY": "m", "ANTHROPIC_API_KEY": " "}))
	if len(got) != 2 {
		t.Fatalf("detected = %+v", got)
	}
	if p := got[0]; p.Name != "openai" || p.Kind != "openai" || p.Protocol != "openai" || !p.Enabled || p.APIKeyRef != "env:OPENAI_API_KEY | env:OPENAI_KEY" {
		t.Fatalf("openai = %+v", p)
	}
	if p := got[1]; p.Name != "mistral" || p.APIKeyRef != "env:MISTRAL_API_KEY" {
		t.Fatalf("mistral = %+v", p)
	}
}

// At boot the gateway signs on to the providers whose key it finds, with no model; a kind the graph already holds a
// provider of (here the seeded anthropic one) is left alone, and a second start writes nothing.
func TestSignOn(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return typecat.Builtin() }
	detected := DetectProviders(envOf(map[string]string{"ANTHROPIC_API_KEY": "a", "MISTRAL_API_KEY": "m"}))
	if _, err := SignOn(ctx, g, detected); !errors.Is(err, ErrNotBooted) {
		t.Fatalf("before boot: %v", err)
	}
	provs, models, aliases, err := DefaultConfig(true).Objects()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: graphsvc.ModelsConfig{Providers: provs, Models: models, Aliases: aliases}}); err != nil {
		t.Fatal(err)
	}
	names, err := SignOn(ctx, g, detected)
	if err != nil || !slices.Equal(names, []string{"mistral"}) {
		t.Fatalf("sign on = %v, %v", names, err)
	}
	n, err := g.NodeByKey(ctx, llmcfg.NamespacePlatform, llmcfg.ProviderKey("mistral"))
	if err != nil || n.Properties["apiKeyRef"] != "env:MISTRAL_API_KEY" || n.Properties["enabled"] != true {
		t.Fatalf("mistral = %+v, %v", n, err)
	}
	head, _ := g.BranchHead(ctx, llmcfg.NamespacePlatform, domain.MainBranch)
	nodes, _, _ := g.CommitGraph(ctx, head.ID)
	for _, n := range nodes {
		if n.Type == llmcfg.NodeTypeModel && n.Properties["provider"] == "mistral" {
			t.Fatalf("a model was created: %+v", n)
		}
	}
	if names, err := SignOn(ctx, g, detected); err != nil || len(names) != 0 {
		t.Fatalf("second start = %v, %v", names, err)
	}
}
