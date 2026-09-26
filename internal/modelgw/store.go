package modelgw

import (
	"context"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/llmcfg"
)

// The configuration is graph data (pkg/llmcfg); these are the shapes the service works with.
type (
	ProviderRecord = llmcfg.Provider
	ModelEntry     = llmcfg.Model
	AliasEntry     = llmcfg.Alias
)

// Quota periods.
const (
	PeriodDay   = llmcfg.PeriodDay
	PeriodMonth = llmcfg.PeriodMonth
	PeriodTotal = llmcfg.PeriodTotal
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

// Store persists the token usage, the only state of the gateway: what is configured (providers, models,
// aliases) is read from the graph. Usage is keyed by the key of the model node (llmcfg.ModelKey).
type Store interface {
	Usage(ctx context.Context, model, period string) (int64, error)
	AddUsage(ctx context.Context, model, period string, tokens int64) error
}

// MemoryStore is an in-memory Store (tests, GOAP_STORE=memory).
type MemoryStore struct {
	mu    sync.Mutex
	usage map[[2]string]int64
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{usage: map[[2]string]int64{}} }

func (s *MemoryStore) Usage(_ context.Context, model, period string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage[[2]string{model, period}], nil
}

func (s *MemoryStore) AddUsage(_ context.Context, model, period string, tokens int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage[[2]string{model, period}] += tokens
	return nil
}
