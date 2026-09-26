package modelgw

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/llmcfg"
)

func stores(t *testing.T) map[string]Store {
	t.Helper()
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := platform.MigrateSQLite(ctx, db, "modelgw", SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	return map[string]Store{"memory": NewMemoryStore(), "sqlite": SQLStore{DB: db}}
}

// newService returns a service reading its configuration from a fresh graph, which the caller edits with change.
func newService(store Store) (*Service, *graph.Graph) {
	g := graph.New(graph.NewMemory())
	return NewService(&llmcfg.Directory{Graph: g, TTL: 1}, store, (&platform.Secrets{}).Resolve, nil), g
}

func seed(t *testing.T, g *graph.Graph, provs []ProviderRecord, models []ModelEntry, aliases []AliasEntry) {
	t.Helper()
	if _, err := graphsvc.SeedModels(context.Background(), g, provs, models, aliases); err != nil {
		t.Fatal(err)
	}
}

// change commits edits on main as one change of the platform namespace.
func change(t *testing.T, g *graph.Graph, edits ...graph.NodeEdit) {
	t.Helper()
	ctx := context.Background()
	head, err := g.BranchHead(ctx, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit(ctx, graph.Commit{Namespace: llmcfg.NamespacePlatform, Title: "t", Intent: "t", Baseline: head.ID, By: "t", Edits: edits}); err != nil {
		t.Fatal(err)
	}
}

func updateNode(t *testing.T, g *graph.Graph, key string, props map[string]any) graph.NodeEdit {
	t.Helper()
	n, err := g.NodeByKey(context.Background(), llmcfg.NamespacePlatform, key)
	if err != nil {
		t.Fatal(err)
	}
	pre := n.Ref()
	return graph.NodeEdit{Pre: &pre, Props: props}
}

func deleteNode(t *testing.T, g *graph.Graph, key string) graph.NodeEdit {
	t.Helper()
	n, err := g.NodeByKey(context.Background(), llmcfg.NamespacePlatform, key)
	if err != nil {
		t.Fatal(err)
	}
	pre := n.Ref()
	return graph.NodeEdit{Pre: &pre, Retire: true}
}

func TestServicePolicy(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
			svc, g := newService(st)
			svc.Now = func() time.Time { return now }
			echo := ModelEntry{Provider: "fake", Model: "echo", Enabled: true, QuotaTokens: 10, QuotaPeriod: PeriodDay, Roles: []string{"methodologist"}}
			seed(t, g, []ProviderRecord{{Name: "fake", Kind: "fake", Protocol: "fake", Enabled: true}}, []ModelEntry{echo}, []AliasEntry{{Alias: "default", Target: "fake/echo"}})
			req := llm.Request{Messages: []llm.Message{{Role: "user", Content: "hi"}}}
			user := authz.With(ctx, authz.Principal{Subject: "u", Roles: []string{"contributor"}})
			if _, err := svc.Complete(user, req); !errors.Is(err, ErrForbidden) {
				t.Fatalf("role required: %v", err)
			}
			meth := authz.With(ctx, authz.Principal{Subject: "m", Roles: []string{"methodologist"}})
			if r, err := svc.Complete(meth, req); err != nil || r.Text != "echo: hi" {
				t.Fatalf("allowed: %v %+v", err, r)
			}
			if _, err := svc.Complete(ctx, req); err != nil { // internal caller
				t.Fatalf("internal: %v", err)
			}
			// fake reports no usage: record some by hand, under the key of the model node, to reach the quota
			_ = st.AddUsage(ctx, echo.Key(), PeriodKey(PeriodDay, now), 10)
			if _, err := svc.Complete(meth, req); !errors.Is(err, ErrQuotaExceeded) {
				t.Fatalf("quota: %v", err)
			}
			svc.Now = func() time.Time { return now.Add(24 * time.Hour) } // new period
			if _, err := svc.Complete(meth, req); err != nil {
				t.Fatalf("next day: %v", err)
			}
			if ms, as, err := svc.Available(user); err != nil || len(ms) != 0 || len(as) != 0 {
				t.Fatalf("contributor must see nothing: %v %+v %+v", err, ms, as)
			}
			if ms, as, err := svc.Available(meth); err != nil || len(ms) != 1 || len(as) != 1 {
				t.Fatalf("methodologist sees the model and its alias: %v %+v %+v", err, ms, as)
			}
			cat, aliases, err := svc.Catalog(ctx)
			if err != nil || len(cat) != 1 || len(aliases) != 1 || cat[0].Roles[0] != "methodologist" {
				t.Fatalf("catalog: %v %+v %+v", err, cat, aliases)
			}
			// the configuration is changed on the graph: the service follows
			change(t, g, updateNode(t, g, echo.Key(), map[string]any{"provider": "fake", "model": "echo", "enabled": false}))
			if _, err := svc.Complete(meth, req); !errors.Is(err, ErrModelDisabled) {
				t.Fatalf("disabled: %v", err)
			}
			change(t, g, deleteNode(t, g, llmcfg.ProviderKey("fake")))
			if _, err := svc.Complete(meth, req); !errors.Is(err, ErrInvalid) {
				t.Fatalf("the alias must go with its provider: %v", err)
			}
			if cat, aliases, _ := svc.Catalog(ctx); len(cat) != 0 || len(aliases) != 0 {
				t.Fatalf("models and aliases must go with the provider: %+v %+v", cat, aliases)
			}
		})
	}
}

func TestProviderKeyIsAReference(t *testing.T) {
	ctx := context.Background()
	svc, g := newService(NewMemoryStore())
	seed(t, g, []ProviderRecord{{Name: "mistral", Kind: "mistral", Protocol: "openai", BaseURL: "https://api.mistral.ai/v1", Enabled: true, APIKeyRef: "env:GOAP_TEST_MISTRAL_KEY"}}, nil, nil)
	view := func() ProviderView {
		vs, err := svc.Providers(ctx)
		if err != nil || len(vs) != 1 {
			t.Fatalf("providers: %v %+v", err, vs)
		}
		return vs[0]
	}
	if v := view(); !v.HasKey || v.Active || v.Reason != "no API key" {
		t.Fatalf("without the key in its environment the provider is inactive: %+v", v)
	}
	t.Setenv("GOAP_TEST_MISTRAL_KEY", "sk-secret-1234")
	change(t, g, updateNode(t, g, llmcfg.ProviderKey("mistral"), map[string]any{"name": "mistral", "kind": "mistral", "protocol": "openai", "enabled": true, "apiKeyRef": "env:GOAP_TEST_MISTRAL_KEY", "baseURL": "https://api.mistral.ai/v1"}))
	if v := view(); !v.Active || v.APIKeyRef != "env:GOAP_TEST_MISTRAL_KEY" {
		t.Fatalf("with the key the provider is loaded: %+v", v)
	}
}

func TestDiscoverModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models": // anthropic
			if r.Header.Get("x-api-key") != "k" {
				http.Error(w, "no key", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-a","display_name":"Claude A"}]}`))
		case "/openai/models": // mistral-like
			if r.Header.Get("Authorization") != "Bearer k" {
				http.Error(w, "no key", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"mistral-large"},{"id":"mistral-small"}]}`))
		case "/gemini/models":
			if r.Header.Get("x-goog-api-key") != "k" {
				http.Error(w, "no key", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-pro","displayName":"Gemini Pro","supportedGenerationMethods":["generateContent"]},{"name":"models/embed","supportedGenerationMethods":["embedContent"]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	svc, _ := newService(NewMemoryStore())
	for _, c := range []struct {
		kind, base string
		want       []string
	}{
		{"anthropic", srv.URL, []string{"claude-a"}},
		{"mistral", srv.URL + "/openai", []string{"mistral-large", "mistral-small"}},
		{"google", srv.URL + "/gemini", []string{"gemini-pro"}},
	} {
		got, err := svc.Discover(ctx, ProviderRecord{Name: "p", Kind: c.kind, BaseURL: c.base}, "k")
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%s: %+v", c.kind, got)
		}
		for i, m := range got {
			if m.ID != c.want[i] {
				t.Fatalf("%s: %+v", c.kind, got)
			}
		}
	}
	if _, err := svc.Discover(ctx, ProviderRecord{Name: "p", Kind: "mistral", BaseURL: srv.URL + "/openai"}, "wrong"); err == nil {
		t.Fatal("bad key must fail")
	}
}

func TestGeminiComplete(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models/gemini-pro:generateContent" || r.Header.Get("x-goog-api-key") != "k" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"hello "},{"text":"world"}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2}}`))
	}))
	defer srv.Close()
	g := &Gemini{BaseURL: srv.URL, APIKey: "k"}
	resp, err := g.Complete(context.Background(), "gemini-pro", llm.Request{System: "sys", JSON: true, MaxTokens: 50,
		Messages: []llm.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello world" || resp.Usage.InputTokens != 4 || resp.Usage.OutputTokens != 2 {
		t.Fatalf("unexpected %+v", resp)
	}
	contents := got["contents"].([]any)
	if len(contents) != 3 || contents[1].(map[string]any)["role"] != "model" || got["systemInstruction"] == nil {
		t.Fatalf("unexpected payload %v", got)
	}
	if cfg := got["generationConfig"].(map[string]any); cfg["responseMimeType"] != "application/json" || cfg["maxOutputTokens"] != float64(50) {
		t.Fatalf("unexpected config %v", cfg)
	}
}

func TestDefaultConfigIsSeededOnce(t *testing.T) {
	ctx := context.Background()
	svc, g := newService(NewMemoryStore())
	provs, models, aliases, err := DefaultConfig(false).Objects()
	if err != nil {
		t.Fatal(err)
	}
	if seeded, err := graphsvc.SeedModels(ctx, g, provs, models, aliases); err != nil || !seeded {
		t.Fatalf("seed: %v %v", seeded, err)
	}
	if _, _, err := svc.Router.Resolve("default"); err == nil {
		t.Fatal("the router is built on first use")
	}
	if err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Router.Resolve("default"); err != nil {
		t.Fatal(err)
	}
	if cat, _, _ := svc.Catalog(ctx); len(cat) != 1 || !cat[0].Enabled {
		t.Fatalf("alias targets must be in the catalog: %+v", cat)
	}
	change(t, g, deleteNode(t, g, llmcfg.AliasKey("default")))
	if seeded, err := graphsvc.SeedModels(ctx, g, provs, models, aliases); err != nil || seeded {
		t.Fatalf("an administered graph must not be re-seeded: %v %v", seeded, err)
	}
	if err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Router.Resolve("default"); err == nil {
		t.Fatal("the deleted alias must be gone")
	}
}
