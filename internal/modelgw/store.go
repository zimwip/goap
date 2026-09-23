package modelgw

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProviderRecord is a stored provider; the API key is sealed (see Box).
type ProviderRecord struct {
	Name     string
	Kind     string
	Protocol string
	BaseURL  string
	Enabled  bool
	KeyEnc   string
	KeyHint  string
}

// ModelEntry is a model of the catalog.
type ModelEntry struct {
	Provider    string
	Model       string
	DisplayName string
	Enabled     bool
	// QuotaTokens is the global token budget per QuotaPeriod (0: unlimited).
	QuotaTokens int64
	QuotaPeriod string // day | month | total
	// Roles may call the model (empty: every authenticated caller).
	Roles []string
}

// AliasEntry maps an alias to "provider/model".
type AliasEntry struct {
	Alias  string
	Target string
}

// Quota periods.
const (
	PeriodDay   = "day"
	PeriodMonth = "month"
	PeriodTotal = "total"
)

// PeriodKey is the usage bucket of t for a quota period.
func PeriodKey(period string, t time.Time) string {
	t = t.UTC()
	switch period {
	case PeriodDay:
		return t.Format("2006-01-02")
	case PeriodMonth:
		return t.Format("2006-01")
	}
	return PeriodTotal
}

// Store persists the gateway configuration and the token usage.
type Store interface {
	ListProviders(ctx context.Context) ([]ProviderRecord, error)
	GetProvider(ctx context.Context, name string) (ProviderRecord, bool, error)
	SaveProvider(ctx context.Context, p ProviderRecord) error
	// DeleteProvider also removes its models.
	DeleteProvider(ctx context.Context, name string) error

	ListModels(ctx context.Context) ([]ModelEntry, error)
	GetModel(ctx context.Context, provider, model string) (ModelEntry, bool, error)
	SaveModel(ctx context.Context, m ModelEntry) error
	DeleteModel(ctx context.Context, provider, model string) error

	ListAliases(ctx context.Context) ([]AliasEntry, error)
	SaveAlias(ctx context.Context, a AliasEntry) error
	DeleteAlias(ctx context.Context, alias string) error

	Usage(ctx context.Context, provider, model, period string) (int64, error)
	AddUsage(ctx context.Context, provider, model, period string, tokens int64) error
}

// MemoryStore is an in-memory Store (tests, GOAP_STORE=memory).
type MemoryStore struct {
	mu        sync.Mutex
	providers map[string]ProviderRecord
	models    map[[2]string]ModelEntry
	aliases   map[string]string
	usage     map[[3]string]int64
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{providers: map[string]ProviderRecord{}, models: map[[2]string]ModelEntry{}, aliases: map[string]string{}, usage: map[[3]string]int64{}}
}

func (s *MemoryStore) ListProviders(context.Context) ([]ProviderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ProviderRecord, 0, len(s.providers))
	for _, p := range s.providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *MemoryStore) GetProvider(_ context.Context, name string) (ProviderRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.providers[name]
	return p, ok, nil
}

func (s *MemoryStore) SaveProvider(_ context.Context, p ProviderRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providers[p.Name] = p
	return nil
}

func (s *MemoryStore) DeleteProvider(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.providers, name)
	for k := range s.models {
		if k[0] == name {
			delete(s.models, k)
		}
	}
	return nil
}

func (s *MemoryStore) ListModels(context.Context) ([]ModelEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ModelEntry, 0, len(s.models))
	for _, m := range s.models {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Provider+"/"+out[i].Model < out[j].Provider+"/"+out[j].Model
	})
	return out, nil
}

func (s *MemoryStore) GetModel(_ context.Context, provider, model string) (ModelEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.models[[2]string{provider, model}]
	return m, ok, nil
}

func (s *MemoryStore) SaveModel(_ context.Context, m ModelEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models[[2]string{m.Provider, m.Model}] = m
	return nil
}

func (s *MemoryStore) DeleteModel(_ context.Context, provider, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.models, [2]string{provider, model})
	return nil
}

func (s *MemoryStore) ListAliases(context.Context) ([]AliasEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AliasEntry, 0, len(s.aliases))
	for a, t := range s.aliases {
		out = append(out, AliasEntry{Alias: a, Target: t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, nil
}

func (s *MemoryStore) SaveAlias(_ context.Context, a AliasEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aliases[a.Alias] = a.Target
	return nil
}

func (s *MemoryStore) DeleteAlias(_ context.Context, alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.aliases, alias)
	return nil
}

func (s *MemoryStore) Usage(_ context.Context, provider, model, period string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage[[3]string{provider, model, period}], nil
}

func (s *MemoryStore) AddUsage(_ context.Context, provider, model, period string, tokens int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage[[3]string{provider, model, period}] += tokens
	return nil
}

func joinRoles(r []string) string { return strings.Join(r, ",") }

func splitRoles(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
