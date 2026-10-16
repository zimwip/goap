package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Secrets reads secrets from HashiCorp Vault (KV v2) and falls back to
// environment variables. A secret reference is "<path>#<field>", e.g.
// "goap/modelgw#anthropic_api_key".
type Secrets struct {
	Addr  string
	Token string
	Mount string
	http  *http.Client
	// Dir is a local fallback store (one JSON file, dir/secrets.json) used by Put/Resolve when Vault is not
	// configured (Addr == ""): a devlocal convenience (ADR 0010) so an admin can still have a raw key vaulted
	// for them without running a real Vault. Empty: no local fallback, Put then fails and Resolve leaves the
	// reference unresolved, same as before Dir existed.
	Dir string
	mu  sync.Mutex
}

// NewSecrets configures Vault from VAULT_ADDR / VAULT_TOKEN. Vault is
// disabled when VAULT_ADDR is empty.
func NewSecrets() *Secrets {
	return &Secrets{Addr: os.Getenv("VAULT_ADDR"), Token: os.Getenv("VAULT_TOKEN"), Mount: Env("VAULT_KV_MOUNT", "secret"),
		http: &http.Client{Timeout: 5 * time.Second}}
}

// Get returns the secret, looking at Vault first then at envFallback.
func (s *Secrets) Get(ctx context.Context, ref, envFallback string) (string, error) {
	if s.Addr != "" && ref != "" {
		v, err := s.vault(ctx, ref)
		if err == nil && v != "" {
			return v, nil
		}
		if envFallback == "" {
			return "", err
		}
	}
	return os.Getenv(envFallback), nil
}

func (s *Secrets) vault(ctx context.Context, ref string) (string, error) {
	path, field, ok := strings.Cut(ref, "#")
	if !ok {
		return "", fmt.Errorf("secret ref %q: expected <path>#<field>", ref)
	}
	url := fmt.Sprintf("%s/v1/%s/data/%s", strings.TrimRight(s.Addr, "/"), s.Mount, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Vault-Token", s.Token)
	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vault %s: %s", path, resp.Status)
	}
	var body struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	v, _ := body.Data.Data[field].(string)
	return v, nil
}

// Put writes value under field of path: in Vault (KV v2) when configured (Addr != ""), else in the local
// fallback store (Dir != ""). Returns a clear error when neither is configured, so the caller can fall back
// to env:VAR.
func (s *Secrets) Put(ctx context.Context, path, field, value string) error {
	if s.Addr == "" {
		if s.Dir == "" {
			return fmt.Errorf("vault is not configured (VAULT_ADDR): store the key in an environment variable instead (env:VAR)")
		}
		return s.localPut(path, field, value)
	}
	url := fmt.Sprintf("%s/v1/%s/data/%s", strings.TrimRight(s.Addr, "/"), s.Mount, path)
	body, err := json.Marshal(struct {
		Data map[string]string `json:"data"`
	}{Data: map[string]string{field: value}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", s.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("vault %s: %s", path, resp.Status)
	}
	return nil
}

// Resolve resolves a secret reference: "env:<VAR>" reads the environment, anything else is a Vault
// reference "<path>#<field>". Alternatives separated by "|" are tried in order and the first
// non-empty value wins ("goap/modelgw#anthropic_api_key | env:ANTHROPIC_API_KEY").
func (s *Secrets) Resolve(ctx context.Context, ref string) (string, error) {
	var lastErr error
	for _, alt := range strings.Split(ref, "|") {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			continue
		}
		var v string
		if name, ok := strings.CutPrefix(alt, "env:"); ok {
			v = os.Getenv(name)
		} else if s.Addr != "" {
			var err error
			if v, err = s.vault(ctx, alt); err != nil {
				lastErr = err
			}
		} else if s.Dir != "" {
			var err error
			if v, err = s.localGet(alt); err != nil {
				lastErr = err
			}
		}
		if v != "" {
			return v, nil
		}
	}
	return "", lastErr
}

// localPath is the devlocal fallback store: one JSON file, path -> field -> value.
func (s *Secrets) localPath() string { return filepath.Join(s.Dir, "secrets.json") }

func (s *Secrets) localGet(ref string) (string, error) {
	path, field, ok := strings.Cut(ref, "#")
	if !ok {
		return "", fmt.Errorf("secret ref %q: expected <path>#<field>", ref)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.readLocalFile()
	if err != nil {
		return "", err
	}
	return store[path][field], nil
}

func (s *Secrets) localPut(path, field, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.readLocalFile()
	if err != nil {
		return err
	}
	if store[path] == nil {
		store[path] = map[string]string{}
	}
	store[path][field] = value
	b, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.localPath(), b, 0o600)
}

func (s *Secrets) readLocalFile() (map[string]map[string]string, error) {
	store := map[string]map[string]string{}
	b, err := os.ReadFile(s.localPath())
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, &store); err != nil {
		return nil, err
	}
	return store, nil
}
