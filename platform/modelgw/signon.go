package modelgw

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/lib/llmcfg"
)

// KnownKeys are the well-known environment variables holding the API key of a provider kind, in the order they are
// tried: a gateway started with one of them set signs on to the provider of that kind (SignOn).
var KnownKeys = []struct {
	Kind string
	Vars []string
}{
	{Kind: "anthropic", Vars: []string{"ANTHROPIC_API_KEY"}},
	{Kind: "openai", Vars: []string{"OPENAI_API_KEY", "OPENAI_KEY"}},
	{Kind: "mistral", Vars: []string{"MISTRAL_API_KEY"}},
	{Kind: "google", Vars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}},
}

// DetectProviders returns a provider for each kind of KnownKeys one of whose variables is set: named after its kind, on
// the kind's protocol and default address, enabled, its key referenced as env:<VAR> (every variable of the kind,
// alternatives, so the one set wins). The key itself is never read into the provider.
func DetectProviders(lookup func(string) (string, bool)) []llmcfg.Provider {
	var out []llmcfg.Provider
	for _, k := range KnownKeys {
		set := false
		refs := make([]string, len(k.Vars))
		for i, v := range k.Vars {
			refs[i] = "env:" + v
			if s, ok := lookup(v); ok && strings.TrimSpace(s) != "" {
				set = true
			}
		}
		kind, ok := LookupKind(k.Kind)
		if !set || !ok {
			continue
		}
		out = append(out, llmcfg.Provider{Name: kind.ID, Kind: kind.ID, Protocol: kind.Protocol, BaseURL: kind.DefaultBaseURL, Enabled: true,
			APIKeyRef: strings.Join(refs, " | ")})
	}
	return out
}

// SignOnGraph is what SignOn needs of the graph: *graph.Graph in process, *graphclient.Client over RPC.
type SignOnGraph interface {
	BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
	Structures(ctx context.Context) (domain.Structures, error)
	Commit(ctx context.Context, in graphapi.Commit) (graphapi.CommitResult, error)
}

// ErrNotBooted is returned by SignOn while the platform bootstrap has not seeded the model configuration yet (the
// protected aliases are its last model step): signing on before it would keep SeedModels from seeding.
var ErrNotBooted = errors.New("the model configuration is not seeded yet")

// SignOnPrincipal is the identity the gateway signs on to providers with.
var SignOnPrincipal = authz.System("modelgw")

// SignOn creates, in one change, the provider of each detected kind the graph holds no provider of (whatever its name,
// state or key: a provider an administrator configured, disabled or retired stays so). It creates no model and no alias:
// the administrator picks the models of a provider in the settings. It returns the names of the providers created.
func SignOn(ctx context.Context, g SignOnGraph, detected []llmcfg.Provider) ([]string, error) {
	if len(detected) == 0 {
		return nil, nil
	}
	if authz.From(ctx).Anonymous() {
		ctx = authz.With(ctx, SignOnPrincipal)
	}
	head, err := g.BranchHead(ctx, llmcfg.NamespacePlatform, domain.MainBranch)
	if errors.Is(err, graphapi.ErrNotFound) {
		return nil, ErrNotBooted
	} else if err != nil {
		return nil, err
	}
	nodes, _, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		return nil, err
	}
	kinds, keys, booted := map[string]bool{}, map[string]bool{}, false
	for _, n := range nodes {
		if n.Namespace != llmcfg.NamespacePlatform {
			continue
		}
		keys[n.Key] = true
		switch {
		case n.Type == llmcfg.NodeTypeProvider:
			if k, _ := n.Properties["kind"].(string); k != "" {
				kinds[k] = true
			}
		case n.Key == llmcfg.AliasKey(llmcfg.AssistantAlias):
			booted = true
		}
	}
	if !booted {
		return nil, ErrNotBooted
	}
	var names []string
	var edits []graphapi.NodeEdit
	for _, p := range detected {
		key := llmcfg.ProviderKey(p.Name)
		if kinds[p.Kind] || keys[key] {
			continue
		}
		names = append(names, p.Name)
		edits = append(edits, graphapi.NodeEdit{Key: key, Type: llmcfg.NodeTypeProvider, Props: p.Props(), Rationale: "API key found in " + p.APIKeyRef})
	}
	if len(edits) == 0 {
		return nil, nil
	}
	sort.Strings(names)
	st, err := g.Structures(ctx)
	if err != nil {
		return nil, err
	}
	title := "Providers signed on from the environment: " + strings.Join(names, ", ")
	_, err = g.Commit(ctx, graphapi.Commit{Namespace: llmcfg.NamespacePlatform, Title: title, Intent: title, By: "modelgw.signon",
		Edits: edits, ProjectID: st.Project().Root})
	if errors.Is(err, graphapi.ErrNothingToCommit) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return names, nil
}
