package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
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
