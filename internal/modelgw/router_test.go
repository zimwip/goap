package modelgw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zimwip/goap/pkg/llm"
)

func TestRouterAliasesAndOpenAICompatible(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("unexpected request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"model":"llama","choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer srv.Close()

	cfg := Config{
		Providers: map[string]ProviderConfig{"local": {Type: "openai", BaseURL: srv.URL + "/v1", APIKeyEnv: "TEST_KEY"}, "fake": {Type: "fake"}},
		Aliases:   map[string]string{"default": "local/llama", "fast": "fake/echo"},
	}
	secret := func(_ context.Context, _, env string) (string, error) {
		if env == "TEST_KEY" {
			return "k", nil
		}
		return "", nil
	}
	r, err := Build(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := r.Complete(context.Background(), llm.Request{System: "sys", JSON: true, Messages: []llm.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "local" || resp.Usage.OutputTokens != 2 || resp.Text != `{"ok":true}` {
		t.Fatalf("unexpected response %+v", resp)
	}
	if got["model"] != "llama" || got["response_format"] == nil || len(got["messages"].([]any)) != 2 {
		t.Fatalf("unexpected payload %v", got)
	}
	if resp, _ := r.Complete(context.Background(), llm.Request{Model: "fast", Messages: []llm.Message{{Role: "user", Content: "x"}}}); resp.Text != "echo: x" {
		t.Fatalf("fake: %+v", resp)
	}
	if _, err := r.Complete(context.Background(), llm.Request{Model: "nope"}); err == nil {
		t.Fatal("unknown alias must fail")
	}
	// anthropic without key is skipped, so an alias to it fails at resolution
	if _, _, err := r.Resolve("anthropic/claude-opus-5"); err == nil {
		t.Fatal("unconfigured provider must fail")
	}
}
