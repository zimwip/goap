package modelgw

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/llmcfg"
)

// Errors of the policy applied to completions and of the administration.
var (
	ErrForbidden     = errors.New("forbidden")
	ErrQuotaExceeded = errors.New("quota exceeded")
	ErrModelDisabled = errors.New("model not available")
	ErrInvalid       = errors.New("invalid")
	ErrNotFound      = errors.New("not found")
)

// Service is the gateway: what is configured (providers, catalog, quotas, access levels, aliases) is read
// from the graph, the Router serves the completions and the Store counts the token usage.
type Service struct {
	Config *llmcfg.Directory
	Store  Store
	Router *Router
	// Secrets resolves the reference of a provider's API key.
	Secrets func(ctx context.Context, ref string) (string, error)
	Log     *slog.Logger
	// HTTP is used to list the models of a provider.
	HTTP *http.Client
	Now  func() time.Time
	// Authz resolves the roles a caller holds on its project (ADR 0043) for the models restricted to some
	// roles; nil checks the roles the caller's principal carries.
	Authz authz.Authorizer
	// CallPrompts stores the exchange of the calls whose prompt no change log keeps (ADR 0089, GOAP_LLM_CALL_PROMPTS);
	// false keeps counters only. NewService sets it.
	CallPrompts bool

	mu     sync.RWMutex
	snap   *llmcfg.Snapshot
	active map[string]string // provider -> "" (loaded) | reason it is not
}

// NewService returns a service; the router is built from the graph on first use.
func NewService(config *llmcfg.Directory, store Store, secrets func(ctx context.Context, ref string) (string, error), log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{Config: config, Store: store, Router: NewRouter(), Secrets: secrets, Log: log, HTTP: &http.Client{Timeout: 20 * time.Second}, Now: time.Now, CallPrompts: true, active: map[string]string{}}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// sync returns the configuration at the head of the graph, rebuilding the router when it moved. When the
// graph cannot be read the last configuration keeps serving; without any, the error is returned.
func (s *Service) sync(ctx context.Context) (*llmcfg.Snapshot, error) {
	snap, err := s.Config.Snapshot(ctx)
	if snap == nil {
		return nil, err
	}
	s.mu.RLock()
	same := s.snap == snap
	s.mu.RUnlock()
	if same {
		return snap, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap == snap {
		return snap, nil
	}
	s.load(ctx, snap)
	return snap, nil
}

// Reload rebuilds the router from the graph now.
func (s *Service) Reload(ctx context.Context) error {
	_, err := s.sync(ctx)
	return err
}

// load builds the router of a snapshot; the caller holds the lock. A provider that cannot be built
// (unknown protocol, no API key) is reported as inactive, not as an error.
func (s *Service) load(ctx context.Context, snap *llmcfg.Snapshot) {
	built := map[string]Provider{}
	active := map[string]string{}
	for _, p := range snap.Providers {
		if !p.Enabled {
			active[p.Name] = "disabled"
			continue
		}
		prov, err := s.build(ctx, p, "")
		if err != nil {
			active[p.Name] = err.Error()
			s.Log.Warn("provider not loaded", "provider", p.Name, "reason", err)
			continue
		}
		built[p.Name] = prov
		active[p.Name] = ""
	}
	targets := map[string]Target{}
	models := map[string]bool{}
	for _, m := range snap.Models {
		models[m.Provider+"/"+m.Model] = true
	}
	for _, a := range snap.Aliases {
		if t, ok := parseTarget(a.Target); ok && models[a.Target] { // a protected alias may point to nothing
			targets[a.Alias] = t
		}
	}
	for _, pr := range snap.Problems {
		s.Log.Warn("model configuration", "problem", pr)
	}
	s.Router.Replace(built, targets)
	s.snap, s.active = snap, active
}

func protocolOf(id string) (Protocol, error) {
	p, ok := LookupProtocol(id)
	if !ok {
		return Protocol{}, fmt.Errorf("unknown protocol %q", id)
	}
	return p, nil
}

// key resolves the API key of a provider from its reference.
func (s *Service) key(ctx context.Context, p ProviderRecord) (string, error) {
	if p.APIKeyRef == "" || s.Secrets == nil {
		return "", nil
	}
	return s.Secrets(ctx, p.APIKeyRef)
}

// build creates the runtime provider of a record; overrideKey (when set) replaces the referenced key.
func (s *Service) build(ctx context.Context, p ProviderRecord, overrideKey string) (Provider, error) {
	key := overrideKey
	if key == "" {
		var err error
		if key, err = s.key(ctx, p); err != nil {
			return nil, fmt.Errorf("API key: %w", err)
		}
	}
	if k, ok := LookupKind(p.Kind); ok && k.KeyRequired && key == "" {
		return nil, errors.New("no API key")
	}
	proto, err := protocolOf(p.Protocol)
	if err != nil {
		return nil, err
	}
	return proto.New(ProviderSpec{Name: p.Name, Kind: p.Kind, Protocol: p.Protocol, BaseURL: p.BaseURL, APIKey: key})
}

// ---- completions ---------------------------------------------------------

// Complete resolves the model, applies the catalog policy (availability,
// required roles, global quota) and calls the provider. Callers without
// identity are trusted internal services and bypass the role check only.
// Every call, refused ones included, is a row of the ledger (ADR 0089).
func (s *Service) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	rec := s.begin(ctx, KindComplete, req.Model)
	rec.request(req.System, req.Messages)
	t, m, period, err := s.admit(ctx, req.Model)
	if err != nil {
		rec.finish(ctx, t.Provider, t.Model, 0, 0, err)
		return llm.Response{}, err
	}
	resp, err := s.Router.Complete(ctx, req)
	rec.answer(resp.Text)
	s.recordUsage(ctx, t, m, period, int64(resp.Usage.InputTokens+resp.Usage.OutputTokens))
	rec.finish(ctx, t.Provider, t.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens, err)
	return resp, err
}

// Embed embeds texts on the embedding model of the platform (alias "embed" by default) under the same
// catalog policy as Complete: the model must be enabled, the caller allowed, the quota not exhausted.
func (s *Service) Embed(ctx context.Context, req llm.EmbedRequest) (llm.EmbedResponse, error) {
	if req.Model == "" {
		req.Model = llm.EmbedAlias
	}
	rec := s.begin(ctx, KindEmbed, req.Model)
	rec.requestEmbed(req.Texts)
	t, m, period, err := s.admit(ctx, req.Model)
	if err != nil {
		rec.finish(ctx, t.Provider, t.Model, 0, 0, err)
		return llm.EmbedResponse{}, err
	}
	resp, err := s.Router.Embed(ctx, req)
	if len(resp.Vectors) > 0 {
		rec.answer(fmt.Sprintf("%d vectors of %d dimensions", len(resp.Vectors), len(resp.Vectors[0])))
	}
	s.recordUsage(ctx, t, m, period, int64(resp.Tokens))
	rec.finish(ctx, t.Provider, t.Model, resp.Tokens, 0, err)
	return resp, err
}

// admit resolves a model and applies the catalog policy (availability, required roles, global quota).
func (s *Service) admit(ctx context.Context, model string) (Target, ModelEntry, string, error) {
	snap, err := s.sync(ctx)
	if err != nil {
		return Target{}, ModelEntry{}, "", err
	}
	t, _, err := s.Router.Resolve(model)
	if err != nil {
		return Target{}, ModelEntry{}, "", fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	m, ok := findModel(snap, t.Provider, t.Model)
	if !ok || !m.Enabled {
		return t, m, "", fmt.Errorf("%w: %s/%s is not in the platform catalog or is disabled", ErrModelDisabled, t.Provider, t.Model)
	}
	if !s.allowed(ctx, authz.From(ctx), m) {
		return t, m, "", fmt.Errorf("%w: %s/%s requires one of the roles %s", ErrForbidden, t.Provider, t.Model, strings.Join(m.Roles, ", "))
	}
	period := PeriodKey(m.QuotaPeriod, s.now())
	if m.QuotaTokens > 0 {
		used, err := s.Store.Usage(ctx, m.Key(), period)
		if err != nil {
			return t, m, period, err
		}
		if used >= m.QuotaTokens {
			return t, m, period, fmt.Errorf("%w: %s/%s used %d of %d tokens (%s)", ErrQuotaExceeded, t.Provider, t.Model, used, m.QuotaTokens, m.QuotaPeriod)
		}
	}
	return t, m, period, nil
}

func (s *Service) recordUsage(ctx context.Context, t Target, m ModelEntry, period string, tokens int64) {
	if tokens > 0 {
		if err := s.Store.AddUsage(context.WithoutCancel(ctx), m.Key(), period, tokens); err != nil {
			s.Log.Error("usage not recorded", "model", t.Provider+"/"+t.Model, "err", err)
		}
	}
}

// allowed applies the access level of a catalog model to a caller. Callers without identity are trusted internal
// services. The roles a model requires are held on the caller's project (ADR 0043): resolved by Authz (resource
// model, act use) when set.
func (s *Service) allowed(ctx context.Context, p authz.Principal, m ModelEntry) bool {
	if p.Anonymous() || len(m.Roles) == 0 || slices.Contains(p.Roles, "admin") {
		return true
	}
	if s.Authz != nil {
		ok, err := s.Authz.Authorize(ctx, authz.Request{Subject: p, Action: "use",
			Resource: authz.Resource{Type: "model", Name: m.Key(), ProjectID: p.Project, Roles: m.Roles}})
		return err == nil && ok
	}
	return slices.ContainsFunc(m.Roles, func(r string) bool { return slices.Contains(p.Roles, r) })
}

func findModel(snap *llmcfg.Snapshot, provider, model string) (ModelEntry, bool) {
	for _, m := range snap.Models {
		if m.Provider == provider && m.Model == model {
			return m, true
		}
	}
	return ModelEntry{}, false
}

// Available lists the models the caller may use (enabled catalog models of a
// loaded provider, with the required role) and the aliases pointing to them.
func (s *Service) Available(ctx context.Context) ([]ModelEntry, []AliasEntry, error) {
	snap, err := s.sync(ctx)
	if err != nil {
		return nil, nil, err
	}
	p := authz.From(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	ok := map[string]bool{}
	var models []ModelEntry
	for _, m := range snap.Models {
		if reason, known := s.active[m.Provider]; m.Enabled && known && reason == "" && s.allowed(ctx, p, m) {
			models = append(models, m)
			ok[m.Provider+"/"+m.Model] = true
		}
	}
	var out []AliasEntry
	for _, a := range snap.Aliases {
		if ok[a.Target] {
			out = append(out, a)
		}
	}
	return models, out, nil
}

// ---- administration (read-only: the configuration is changed through changes on the graph) ------

// ProviderView is a provider as shown to administrators.
type ProviderView struct {
	ProviderRecord
	// HasKey: the provider references an API key.
	HasKey bool
	Active bool
	Reason string // why it is not active
}

// Providers lists the configured providers.
func (s *Service) Providers(ctx context.Context) ([]ProviderView, error) {
	snap, err := s.sync(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProviderView, len(snap.Providers))
	for i, p := range snap.Providers {
		reason, known := s.active[p.Name]
		out[i] = ProviderView{ProviderRecord: p, HasKey: p.APIKeyRef != "", Active: known && reason == "", Reason: reason}
	}
	return out, nil
}

// Discovered is a model reported by a provider.
type Discovered struct {
	ModelInfo
	Registered bool
}

// Discover asks a provider for its models. The spec need not exist on the graph; an empty apiKey resolves
// the key from the reference of the spec, or of the provider of that name.
func (s *Service) Discover(ctx context.Context, in ProviderRecord, apiKey string) ([]Discovered, error) {
	snap, err := s.sync(ctx)
	if err != nil {
		return nil, err
	}
	var stored ProviderRecord
	exists := false
	for _, p := range snap.Providers {
		if p.Name == in.Name {
			stored, exists = p, true
		}
	}
	if k, ok := LookupKind(in.Kind); ok {
		if in.Protocol == "" {
			in.Protocol = k.Protocol
		}
		if in.BaseURL == "" {
			in.BaseURL = k.DefaultBaseURL
		}
	}
	if in.APIKeyRef == "" && exists {
		in.APIKeyRef = stored.APIKeyRef
	}
	if apiKey == "" {
		if apiKey, err = s.key(ctx, in); err != nil {
			return nil, fmt.Errorf("%w: API key: %w", ErrInvalid, err)
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
	for _, e := range snap.Models {
		if e.Provider == in.Name {
			have[e.Model] = true
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
	snap, err := s.sync(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make([]CatalogEntry, len(snap.Models))
	for i, m := range snap.Models {
		used, err := s.Store.Usage(ctx, m.Key(), PeriodKey(m.QuotaPeriod, s.now()))
		if err != nil {
			return nil, nil, err
		}
		out[i] = CatalogEntry{ModelEntry: m, Used: used}
	}
	return out, snap.Aliases, nil
}
