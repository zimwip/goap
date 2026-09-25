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

	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/llm"
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

func TestServicePolicy(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
			svc := NewService(st, NewBox("k"), nil)
			svc.Now = func() time.Time { return now }
			if _, err := svc.SaveProvider(ctx, ProviderRecord{Name: "fake", Kind: "fake", Enabled: true}, "", false); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.SaveModel(ctx, ModelEntry{Provider: "fake", Model: "echo", Enabled: true, QuotaTokens: 10, QuotaPeriod: PeriodDay, Roles: []string{"methodologist"}}); err != nil {
				t.Fatal(err)
			}
			if err := svc.SaveAlias(ctx, AliasEntry{Alias: "default", Target: "fake/echo"}); err != nil {
				t.Fatal(err)
			}
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
			// fake reports no usage: record some by hand to reach the quota
			_ = st.AddUsage(ctx, "fake", "echo", PeriodKey(PeriodDay, now), 10)
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
			if _, err := svc.SaveModel(ctx, ModelEntry{Provider: "fake", Model: "echo", Enabled: false}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Complete(meth, req); !errors.Is(err, ErrModelDisabled) {
				t.Fatalf("disabled: %v", err)
			}
			if err := svc.DeleteProvider(ctx, "fake"); err != nil {
				t.Fatal(err)
			}
			if m, _ := st.ListModels(ctx); len(m) != 0 {
				t.Fatalf("models must go with the provider: %+v", m)
			}
			if a, _ := st.ListAliases(ctx); len(a) != 0 {
				t.Fatalf("aliases must go with the provider: %+v", a)
			}
		})
	}
}

func TestProviderKeyIsSealedAndWriteOnly(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()
	svc := NewService(st, NewBox("k"), nil)
	v, err := svc.SaveProvider(ctx, ProviderRecord{Name: "mistral", Kind: "mistral", Enabled: true}, "sk-secret-1234", false)
	if err != nil {
		t.Fatal(err)
	}
	if !v.HasKey || v.KeyHint != "••••1234" || v.BaseURL != "https://api.mistral.ai/v1" || v.Protocol != "openai" || !v.Active {
		t.Fatalf("unexpected view %+v", v)
	}
	rec, _, _ := st.GetProvider(ctx, "mistral")
	if rec.KeyEnc == "" || rec.KeyEnc == "sk-secret-1234" {
		t.Fatalf("key must be sealed: %q", rec.KeyEnc)
	}
	// update without key keeps it; clear removes it
	if v, _ = svc.SaveProvider(ctx, ProviderRecord{Name: "mistral", Kind: "mistral", Enabled: true}, "", false); !v.HasKey {
		t.Fatal("key must be kept")
	}
	if v, _ = svc.SaveProvider(ctx, ProviderRecord{Name: "mistral", Kind: "mistral", Enabled: true}, "", true); v.HasKey {
		t.Fatal("key must be cleared")
	}
	if _, err := svc.SaveProvider(ctx, ProviderRecord{Name: "Bad/Name", Kind: "fake"}, "", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("name validation: %v", err)
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
	svc := NewService(NewMemoryStore(), NewBox("k"), nil)
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

func TestBootstrapImportsOnce(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()
	svc := NewService(st, NewBox("k"), nil)
	secret := func(context.Context, string, string) (string, error) { return "", nil }
	if err := svc.Bootstrap(ctx, DefaultConfig(false), secret); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Router.Resolve("default"); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := st.GetModel(ctx, "fake", "echo"); !m.Enabled {
		t.Fatal("alias targets must be in the catalog")
	}
	_ = svc.DeleteAlias(ctx, "default")
	if err := svc.Bootstrap(ctx, DefaultConfig(false), secret); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Router.Resolve("default"); err == nil {
		t.Fatal("an administered store must not be re-seeded")
	}
}
