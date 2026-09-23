package modelgw

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/llm"
)

// Errors of the policy applied to completions and of the administration.
var (
	ErrForbidden     = errors.New("forbidden")
	ErrQuotaExceeded = errors.New("quota exceeded")
	ErrModelDisabled = errors.New("model not available")
	ErrInvalid       = errors.New("invalid")
	ErrNotFound      = errors.New("not found")
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// Service is the administered gateway: providers, catalog, quotas and
// access levels live in the Store, the Router serves the completions.
type Service struct {
	Store  Store
	Router *Router
	Box    *Box
	Log    *slog.Logger
	// HTTP is used to list the models of a provider.
	HTTP *http.Client
	Now  func() time.Time

	mu     sync.RWMutex
	active map[string]string // provider -> "" (loaded) | reason it is not
}

// NewService returns a service; call Reload (or Bootstrap) before use.
func NewService(store Store, box *Box, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{Store: store, Router: NewRouter(), Box: box, Log: log, HTTP: &http.Client{Timeout: 20 * time.Second}, Now: time.Now, active: map[string]string{}}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Bootstrap imports a file / environment configuration when the store has no
// provider yet, then loads the router.
func (s *Service) Bootstrap(ctx context.Context, cfg Config, secret SecretFunc) error {
	provs, err := s.Store.ListProviders(ctx)
	if err != nil {
		return err
	}
	if len(provs) == 0 {
		imported := map[string]bool{}
		for name, pc := range cfg.Providers {
			key := ""
			if pc.APIKeySecret != "" || pc.APIKeyEnv != "" {
				if key, err = secret(ctx, pc.APIKeySecret, pc.APIKeyEnv); err != nil {
					return fmt.Errorf("provider %s: %w", name, err)
				}
			}
			kind := pc.Type
			switch pc.Type {
			case "openai":
				kind = "openai-compatible"
			case "anthropic":
				if key == "" {
					continue // not configured
				}
			}
			k, ok := LookupKind(kind)
			if !ok {
				return fmt.Errorf("provider %s: unknown type %q", name, pc.Type)
			}
			rec := ProviderRecord{Name: name, Kind: k.ID, Protocol: k.Protocol, BaseURL: pc.BaseURL, Enabled: true, KeyHint: ""}
			if key != "" {
				if rec.KeyEnc, err = s.Box.Seal(key); err != nil {
					return err
				}
				rec.KeyHint = KeyHint(key)
			}
			if err := s.Store.SaveProvider(ctx, rec); err != nil {
				return err
			}
			imported[name] = true
		}
		for alias, target := range cfg.Aliases {
			t, ok := parseTarget(target)
			if !ok || !imported[t.Provider] {
				continue
			}
			if err := s.Store.SaveAlias(ctx, AliasEntry{Alias: alias, Target: target}); err != nil {
				return err
			}
			if _, found, err := s.Store.GetModel(ctx, t.Provider, t.Model); err != nil {
				return err
			} else if !found {
				if err := s.Store.SaveModel(ctx, ModelEntry{Provider: t.Provider, Model: t.Model, DisplayName: t.Model, Enabled: true, QuotaPeriod: PeriodMonth}); err != nil {
					return err
				}
			}
		}
	}
	return s.Reload(ctx)
}

// Reload rebuilds the router from the store.
func (s *Service) Reload(ctx context.Context) error {
	provs, err := s.Store.ListProviders(ctx)
	if err != nil {
		return err
	}
	aliases, err := s.Store.ListAliases(ctx)
	if err != nil {
		return err
	}
	built := map[string]Provider{}
	active := map[string]string{}
	for _, p := range provs {
		if !p.Enabled {
			active[p.Name] = "disabled"
			continue
		}
		spec, err := s.spec(p, "")
		if err == nil {
			var proto Protocol
			proto, err = protocolOf(spec.Protocol)
			if err == nil {
				var prov Provider
				if prov, err = proto.New(spec); err == nil {
					built[p.Name] = prov
					active[p.Name] = ""
					continue
				}
			}
		}
		active[p.Name] = err.Error()
		s.Log.Warn("provider not loaded", "provider", p.Name, "reason", err)
	}
	targets := map[string]Target{}
	for _, a := range aliases {
		if t, ok := parseTarget(a.Target); ok {
			targets[a.Alias] = t
		}
	}
	s.Router.Replace(built, targets)
	s.mu.Lock()
	s.active = active
	s.mu.Unlock()
	return nil
}

func protocolOf(id string) (Protocol, error) {
	p, ok := LookupProtocol(id)
	if !ok {
		return Protocol{}, fmt.Errorf("unknown protocol %q", id)
	}
	return p, nil
}

// spec builds the runtime provider spec of a record; overrideKey (when set)
// replaces the stored key.
func (s *Service) spec(p ProviderRecord, overrideKey string) (ProviderSpec, error) {
	key := overrideKey
	if key == "" {
		var err error
		if key, err = s.Box.Open(p.KeyEnc); err != nil {
			return ProviderSpec{}, err
		}
	}
	return ProviderSpec{Name: p.Name, Kind: p.Kind, Protocol: p.Protocol, BaseURL: p.BaseURL, APIKey: key}, nil
}

// ---- completions ---------------------------------------------------------

// Complete resolves the model, applies the catalog policy (availability,
// required roles, global quota) and calls the provider. Callers without
// identity are trusted internal services and bypass the role check only.
func (s *Service) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	t, _, err := s.Router.Resolve(req.Model)
	if err != nil {
		return llm.Response{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	m, ok, err := s.Store.GetModel(ctx, t.Provider, t.Model)
	if err != nil {
		return llm.Response{}, err
	}
	if !ok || !m.Enabled {
		return llm.Response{}, fmt.Errorf("%w: %s/%s is not in the platform catalog or is disabled", ErrModelDisabled, t.Provider, t.Model)
	}
	if p := authz.From(ctx); !p.Anonymous() && len(m.Roles) > 0 && !slices.Contains(p.Roles, "admin") {
		if !slices.ContainsFunc(m.Roles, func(r string) bool { return slices.Contains(p.Roles, r) }) {
			return llm.Response{}, fmt.Errorf("%w: %s/%s requires one of the roles %s", ErrForbidden, t.Provider, t.Model, strings.Join(m.Roles, ", "))
		}
	}
	period := PeriodKey(m.QuotaPeriod, s.now())
	if m.QuotaTokens > 0 {
		used, err := s.Store.Usage(ctx, t.Provider, t.Model, period)
		if err != nil {
			return llm.Response{}, err
		}
		if used >= m.QuotaTokens {
			return llm.Response{}, fmt.Errorf("%w: %s/%s used %d of %d tokens (%s)", ErrQuotaExceeded, t.Provider, t.Model, used, m.QuotaTokens, m.QuotaPeriod)
		}
	}
	resp, err := s.Router.Complete(ctx, req)
	if tokens := int64(resp.Usage.InputTokens + resp.Usage.OutputTokens); tokens > 0 {
		if uerr := s.Store.AddUsage(context.WithoutCancel(ctx), t.Provider, t.Model, period, tokens); uerr != nil {
			s.Log.Error("usage not recorded", "model", t.Provider+"/"+t.Model, "err", uerr)
		}
	}
	return resp, err
}

// ---- administration ------------------------------------------------------

// ProviderView is a provider as shown to administrators.
type ProviderView struct {
	ProviderRecord
	HasKey bool
	Active bool
	Reason string // why it is not active
}

func (s *Service) view(p ProviderRecord) ProviderView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reason, known := s.active[p.Name]
	return ProviderView{ProviderRecord: p, HasKey: p.KeyEnc != "", Active: known && reason == "", Reason: reason}
}

// Providers lists the configured providers.
func (s *Service) Providers(ctx context.Context) ([]ProviderView, error) {
	recs, err := s.Store.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ProviderView, len(recs))
	for i, r := range recs {
		out[i] = s.view(r)
	}
	return out, nil
}

// SaveProvider creates or updates a provider. apiKey empty keeps the stored key.
func (s *Service) SaveProvider(ctx context.Context, in ProviderRecord, apiKey string, clearKey bool) (ProviderView, error) {
	if !nameRe.MatchString(in.Name) {
		return ProviderView{}, fmt.Errorf("%w: provider name must be 1-40 characters of a-z, 0-9, - or _", ErrInvalid)
	}
	if k, ok := LookupKind(in.Kind); ok {
		if in.Protocol == "" {
			in.Protocol = k.Protocol
		}
		if in.BaseURL == "" {
			in.BaseURL = k.DefaultBaseURL
		}
	}
	if _, ok := LookupProtocol(in.Protocol); !ok {
		return ProviderView{}, fmt.Errorf("%w: unknown protocol %q", ErrInvalid, in.Protocol)
	}
	if in.Kind == "" {
		in.Kind = "openai-compatible"
	}
	if in.BaseURL != "" && !strings.HasPrefix(in.BaseURL, "http://") && !strings.HasPrefix(in.BaseURL, "https://") {
		return ProviderView{}, fmt.Errorf("%w: base URL must start with http:// or https://", ErrInvalid)
	}
	old, _, err := s.Store.GetProvider(ctx, in.Name)
	if err != nil {
		return ProviderView{}, err
	}
	in.KeyEnc, in.KeyHint = old.KeyEnc, old.KeyHint
	switch {
	case apiKey != "":
		if in.KeyEnc, err = s.Box.Seal(apiKey); err != nil {
			return ProviderView{}, err
		}
		in.KeyHint = KeyHint(apiKey)
	case clearKey:
		in.KeyEnc, in.KeyHint = "", ""
	}
	if err := s.Store.SaveProvider(ctx, in); err != nil {
		return ProviderView{}, err
	}
	if err := s.Reload(ctx); err != nil {
		return ProviderView{}, err
	}
	return s.view(in), nil
}

// DeleteProvider removes a provider, its models and the aliases targeting it.
func (s *Service) DeleteProvider(ctx context.Context, name string) error {
	aliases, err := s.Store.ListAliases(ctx)
	if err != nil {
		return err
	}
	for _, a := range aliases {
		if t, ok := parseTarget(a.Target); ok && t.Provider == name {
			if err := s.Store.DeleteAlias(ctx, a.Alias); err != nil {
				return err
			}
		}
	}
	if err := s.Store.DeleteProvider(ctx, name); err != nil {
		return err
	}
	return s.Reload(ctx)
}

// Discovered is a model reported by a provider.
type Discovered struct {
	ModelInfo
	Registered bool
}

// Discover asks a provider for its models. The spec need not be saved; an
// empty apiKey falls back on the stored key of the provider of that name.
func (s *Service) Discover(ctx context.Context, in ProviderRecord, apiKey string) ([]Discovered, error) {
	stored, exists, err := s.Store.GetProvider(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	if k, ok := LookupKind(in.Kind); ok {
		if in.Protocol == "" {
			in.Protocol = k.Protocol
		}
		if in.BaseURL == "" {
			in.BaseURL = k.DefaultBaseURL
		}
	}
	if apiKey == "" && exists {
		if apiKey, err = s.Box.Open(stored.KeyEnc); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	proto, err := protocolOf(in.Protocol)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	spec := ProviderSpec{Name: in.Name, Kind: in.Kind, Protocol: in.Protocol, BaseURL: in.BaseURL, APIKey: apiKey}
	models, err := proto.List(ctx, s.HTTP, spec)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	have := map[string]bool{}
	if exists {
		entries, err := s.Store.ListModels(ctx)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Provider == in.Name {
				have[e.Model] = true
			}
		}
	}
	out := make([]Discovered, 0, len(models))
	for _, m := range models {
		out = append(out, Discovered{ModelInfo: m, Registered: have[m.ID]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// CatalogEntry is a catalog model with its usage in the current period.
type CatalogEntry struct {
	ModelEntry
	Used int64
}

// Catalog lists the catalog and the aliases.
func (s *Service) Catalog(ctx context.Context) ([]CatalogEntry, []AliasEntry, error) {
	models, err := s.Store.ListModels(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make([]CatalogEntry, len(models))
	for i, m := range models {
		used, err := s.Store.Usage(ctx, m.Provider, m.Model, PeriodKey(m.QuotaPeriod, s.now()))
		if err != nil {
			return nil, nil, err
		}
		out[i] = CatalogEntry{ModelEntry: m, Used: used}
	}
	aliases, err := s.Store.ListAliases(ctx)
	return out, aliases, err
}

// SaveModel creates or updates a catalog entry.
func (s *Service) SaveModel(ctx context.Context, m ModelEntry) (CatalogEntry, error) {
	if m.Model == "" {
		return CatalogEntry{}, fmt.Errorf("%w: model id required", ErrInvalid)
	}
	if _, ok, err := s.Store.GetProvider(ctx, m.Provider); err != nil {
		return CatalogEntry{}, err
	} else if !ok {
		return CatalogEntry{}, fmt.Errorf("%w: provider %q", ErrNotFound, m.Provider)
	}
	if m.QuotaTokens < 0 {
		return CatalogEntry{}, fmt.Errorf("%w: quota must be positive (0: unlimited)", ErrInvalid)
	}
	switch m.QuotaPeriod {
	case "":
		m.QuotaPeriod = PeriodMonth
	case PeriodDay, PeriodMonth, PeriodTotal:
	default:
		return CatalogEntry{}, fmt.Errorf("%w: quota period must be day, month or total", ErrInvalid)
	}
	var roles []string
	for _, r := range m.Roles {
		if r = strings.TrimSpace(r); r != "" && !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	m.Roles = roles
	if m.DisplayName == "" {
		m.DisplayName = m.Model
	}
	if err := s.Store.SaveModel(ctx, m); err != nil {
		return CatalogEntry{}, err
	}
	used, err := s.Store.Usage(ctx, m.Provider, m.Model, PeriodKey(m.QuotaPeriod, s.now()))
	return CatalogEntry{ModelEntry: m, Used: used}, err
}

// DeleteModel removes a model from the catalog (and the aliases targeting it).
func (s *Service) DeleteModel(ctx context.Context, provider, model string) error {
	aliases, err := s.Store.ListAliases(ctx)
	if err != nil {
		return err
	}
	for _, a := range aliases {
		if a.Target == provider+"/"+model {
			if err := s.Store.DeleteAlias(ctx, a.Alias); err != nil {
				return err
			}
		}
	}
	if err := s.Store.DeleteModel(ctx, provider, model); err != nil {
		return err
	}
	return s.Reload(ctx)
}

// SaveAlias maps an alias to a catalog model.
func (s *Service) SaveAlias(ctx context.Context, a AliasEntry) error {
	if !nameRe.MatchString(a.Alias) {
		return fmt.Errorf("%w: alias must be 1-40 characters of a-z, 0-9, - or _", ErrInvalid)
	}
	t, ok := parseTarget(a.Target)
	if !ok {
		return fmt.Errorf("%w: target must be provider/model", ErrInvalid)
	}
	if _, found, err := s.Store.GetModel(ctx, t.Provider, t.Model); err != nil {
		return err
	} else if !found {
		return fmt.Errorf("%w: %s is not in the catalog", ErrNotFound, a.Target)
	}
	if err := s.Store.SaveAlias(ctx, a); err != nil {
		return err
	}
	return s.Reload(ctx)
}

// DeleteAlias removes an alias.
func (s *Service) DeleteAlias(ctx context.Context, alias string) error {
	if err := s.Store.DeleteAlias(ctx, alias); err != nil {
		return err
	}
	return s.Reload(ctx)
}
