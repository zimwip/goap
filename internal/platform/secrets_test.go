package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSecretsPut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if want := "/v1/secret/data/goap/modelgw/acme"; r.URL.Path != want {
			t.Fatalf("path = %s, want %s", r.URL.Path, want)
		}
		if r.Header.Get("X-Vault-Token") != "tok" {
			t.Fatalf("missing/wrong X-Vault-Token")
		}
		body, _ := io.ReadAll(r.Body)
		var decoded struct {
			Data map[string]string `json:"data"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if decoded.Data["api_key"] != "sk-raw" {
			t.Fatalf("posted field = %+v", decoded.Data)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &Secrets{Addr: srv.URL, Token: "tok", Mount: "secret", http: &http.Client{Timeout: 5 * time.Second}}
	if err := s.Put(context.Background(), "goap/modelgw/acme", "api_key", "sk-raw"); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func TestSecretsPutThenResolve(t *testing.T) {
	stored := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/secret/data/")
		switch r.Method {
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var decoded struct {
				Data map[string]string `json:"data"`
			}
			_ = json.Unmarshal(body, &decoded)
			for k, v := range decoded.Data {
				stored[path+"#"+k] = v
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			field := ""
			for k := range stored {
				if strings.HasPrefix(k, path+"#") {
					field = strings.TrimPrefix(k, path+"#")
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"data": map[string]any{field: stored[path+"#"+field]}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := &Secrets{Addr: srv.URL, Token: "tok", Mount: "secret", http: &http.Client{Timeout: 5 * time.Second}}
	if err := s.Put(context.Background(), "goap/modelgw/acme", "api_key", "sk-raw"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Resolve(context.Background(), "goap/modelgw/acme#api_key")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "sk-raw" {
		t.Fatalf("Resolve = %q, want sk-raw", got)
	}
}

func TestSecretsPutNoVault(t *testing.T) {
	s := &Secrets{}
	err := s.Put(context.Background(), "goap/modelgw/acme", "api_key", "sk-raw")
	if err == nil {
		t.Fatal("want an error when neither Vault nor a local fallback is configured")
	}
	if !strings.Contains(err.Error(), "env:") {
		t.Fatalf("error should point at env:VAR fallback, got %v", err)
	}
}

// TestSecretsLocalFallback covers the devlocal case (ADR 0010): no Vault running (Addr empty), a local
// directory configured instead (as cmd/goap-dev does from the SQLite database's directory).
func TestSecretsLocalFallback(t *testing.T) {
	s := &Secrets{Dir: t.TempDir()}
	if err := s.Put(context.Background(), "goap/modelgw/acme", "api_key", "sk-raw"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Resolve(context.Background(), "goap/modelgw/acme#api_key")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "sk-raw" {
		t.Fatalf("Resolve = %q, want sk-raw", got)
	}
	// a second provider's key does not clobber the first
	if err := s.Put(context.Background(), "goap/modelgw/other", "api_key", "sk-other"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := s.Resolve(context.Background(), "goap/modelgw/acme#api_key"); err != nil || got != "sk-raw" {
		t.Fatalf("Resolve after second Put = %q, %v", got, err)
	}
}
