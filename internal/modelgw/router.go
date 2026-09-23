// Package modelgw is the model gateway: it routes completion requests to
// LLM providers by alias ("default", "fast") or "provider/model".
package modelgw

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/zimwip/goap/pkg/llm"
)

// Provider completes prompts on a given model.
type Provider interface {
	Complete(ctx context.Context, model string, req llm.Request) (llm.Response, error)
}

// Target is a provider model.
type Target struct {
	Provider string
	Model    string
}

func parseTarget(s string) (Target, bool) {
	p, m, ok := strings.Cut(s, "/")
	return Target{Provider: p, Model: m}, ok && p != "" && m != ""
}

// Router resolves aliases and dispatches to providers.
type Router struct {
	// Instrument wraps every provider call (OpenTelemetry GenAI spans and metrics).
	Instrument func(ctx context.Context, provider, model string, req llm.Request, call func(context.Context) (llm.Response, error)) (llm.Response, error)

	mu        sync.RWMutex
	providers map[string]Provider
	aliases   map[string]Target
}

// NewRouter returns an empty router.
func NewRouter() *Router {
	return &Router{providers: map[string]Provider{}, aliases: map[string]Target{}}
}

// AddProvider registers a provider.
func (r *Router) AddProvider(name string, p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = p
}

// SetAlias maps an alias to "provider/model".
func (r *Router) SetAlias(alias, target string) error {
	t, ok := parseTarget(target)
	if !ok {
		return fmt.Errorf("alias %s: target %q must be provider/model", alias, target)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aliases[alias] = t
	return nil
}

// Resolve returns the provider target of a model name.
func (r *Router) Resolve(model string) (Target, Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if model == "" {
		model = "default"
	}
	t, ok := r.aliases[model]
	if !ok {
		if t, ok = parseTarget(model); !ok {
			return Target{}, nil, fmt.Errorf("unknown model alias %q", model)
		}
	}
	p, ok := r.providers[t.Provider]
	if !ok {
		return Target{}, nil, fmt.Errorf("unknown provider %q", t.Provider)
	}
	return t, p, nil
}

// Complete implements llm.Client.
func (r *Router) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	t, p, err := r.Resolve(req.Model)
	if err != nil {
		return llm.Response{}, err
	}
	call := func(ctx context.Context) (llm.Response, error) {
		resp, err := p.Complete(ctx, t.Model, req)
		if resp.Provider == "" {
			resp.Provider = t.Provider
		}
		if resp.Model == "" {
			resp.Model = t.Model
		}
		return resp, err
	}
	var resp llm.Response
	if r.Instrument != nil {
		resp, err = r.Instrument(ctx, t.Provider, t.Model, req, call)
	} else {
		resp, err = call(ctx)
	}
	if err != nil {
		return resp, fmt.Errorf("%s/%s: %w", t.Provider, t.Model, err)
	}
	return resp, nil
}

// Aliases lists aliases, sorted.
func (r *Router) Aliases() ([]string, map[string]Target, []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.aliases))
	targets := map[string]Target{}
	for a, t := range r.aliases {
		names = append(names, a)
		targets[a] = t
	}
	sort.Strings(names)
	var providers []string
	for p := range r.providers {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	return names, targets, providers
}

// Config is the gateway configuration file.
type Config struct {
	Providers map[string]ProviderConfig `yaml:"providers"`
	Aliases   map[string]string         `yaml:"aliases"`
}

// ProviderConfig configures one provider.
type ProviderConfig struct {
	Type    string `yaml:"type"` // anthropic | openai | fake
	BaseURL string `yaml:"baseURL,omitempty"`
	// APIKeySecret is a Vault reference "<path>#<field>"; APIKeyEnv the fallback.
	APIKeySecret string `yaml:"apiKeySecret,omitempty"`
	APIKeyEnv    string `yaml:"apiKeyEnv,omitempty"`
}

// LoadConfig reads a YAML config file.
func LoadConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	err = yaml.Unmarshal(data, &c)
	return c, err
}

// DefaultConfig uses Anthropic when a key is available, the fake provider otherwise.
func DefaultConfig(hasAnthropicKey bool) Config {
	c := Config{Providers: map[string]ProviderConfig{
		"fake":      {Type: "fake"},
		"anthropic": {Type: "anthropic", APIKeySecret: "goap/modelgw#anthropic_api_key", APIKeyEnv: "ANTHROPIC_API_KEY"},
	}}
	if hasAnthropicKey {
		c.Aliases = map[string]string{"default": "anthropic/claude-opus-5", "fast": "anthropic/claude-haiku-4-5"}
	} else {
		c.Aliases = map[string]string{"default": "fake/echo", "fast": "fake/echo"}
	}
	return c
}

// SecretFunc resolves a secret (Vault reference, env fallback).
type SecretFunc func(ctx context.Context, ref, env string) (string, error)

// Build creates a router from a config.
func Build(ctx context.Context, c Config, secret SecretFunc) (*Router, error) {
	r := NewRouter()
	for name, pc := range c.Providers {
		key := ""
		if pc.APIKeySecret != "" || pc.APIKeyEnv != "" {
			k, err := secret(ctx, pc.APIKeySecret, pc.APIKeyEnv)
			if err != nil {
				return nil, fmt.Errorf("provider %s: %w", name, err)
			}
			key = k
		}
		switch pc.Type {
		case "anthropic":
			if key == "" {
				continue // not configured
			}
			r.AddProvider(name, NewAnthropic(key, pc.BaseURL))
		case "openai":
			r.AddProvider(name, &OpenAICompatible{BaseURL: pc.BaseURL, APIKey: key})
		case "fake":
			r.AddProvider(name, Fake{})
		default:
			return nil, fmt.Errorf("provider %s: unknown type %q", name, pc.Type)
		}
	}
	for a, t := range c.Aliases {
		if err := r.SetAlias(a, t); err != nil {
			return nil, err
		}
	}
	return r, nil
}
