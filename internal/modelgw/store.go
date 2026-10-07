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

// Store persists the state of the gateway, which is the quota counters and the ledger of calls (ADR 0089): what is
// configured (providers, models, aliases) is read from the graph. The counter is keyed by the key of the model node
// (llmcfg.ModelKey) and period and serves the admission only; it outlives the purge of the ledger.
type Store interface {
	Usage(ctx context.Context, model, period string) (int64, error)
	AddUsage(ctx context.Context, model, period string, tokens int64) error
	// AppendCall writes a row of the ledger and returns its seq (monotonic); x, when not nil, is stored with it (one step).
	AppendCall(ctx context.Context, c Call, x *Exchange) (int64, error)
	// Call returns the row of a seq, ErrNotFound when there is none (purged or never written).
	Call(ctx context.Context, seq int64) (Call, error)
	// Exchange returns the stored exchange of a seq, ErrNotFound when there is none.
	Exchange(ctx context.Context, seq int64) (Exchange, error)
	// Calls lists the calls of the filter ascending by seq (see Service.ListCalls) and whether more match.
	Calls(ctx context.Context, f UsageFilter) ([]Call, bool, error)
	Summary(ctx context.Context, f UsageFilter, group string) ([]SummaryRow, error)
	// PurgeCalls deletes the calls before t, with their exchanges, and returns how many calls.
	PurgeCalls(ctx context.Context, before time.Time) (int64, error)

	// The measured cost of the behaviours (ADR 0093, "Measured cost"); the tables are small (a behaviour by a model).
	BehaviorCosts(ctx context.Context) ([]BehaviorCost, error)
	// PutBehaviorCost inserts or replaces the cost of its key.
	PutBehaviorCost(ctx context.Context, c BehaviorCost) error
	DeleteBehaviorCosts(ctx context.Context, keys []CostKey) error
	ModelBaselines(ctx context.Context) ([]ModelBaseline, error)
	PutModelBaseline(ctx context.Context, b ModelBaseline) error
	DeleteModelBaselines(ctx context.Context, keys [][2]string) error
}

// MemoryStore is an in-memory Store (tests, GOAP_STORE=memory).
type MemoryStore struct {
	mu    sync.Mutex
	usage map[[2]string]int64
	calls []Call
	xs    map[int64]Exchange
	seq   int64
	costs map[CostKey]BehaviorCost
	bases map[[2]string]ModelBaseline
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{usage: map[[2]string]int64{}, xs: map[int64]Exchange{}, costs: map[CostKey]BehaviorCost{}, bases: map[[2]string]ModelBaseline{}}
}

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

func (s *MemoryStore) AppendCall(_ context.Context, c Call, x *Exchange) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	c.Seq = s.seq
	c.HasExchange = x != nil
	if x != nil {
		xc := *x
		xc.Seq = c.Seq
		s.xs[c.Seq] = xc
	}
	s.calls = append(s.calls, c)
	return c.Seq, nil
}

func (s *MemoryStore) Call(_ context.Context, seq int64) (Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c.Seq == seq {
			return c, nil
		}
	}
	return Call{}, ErrNotFound
}

func (s *MemoryStore) Exchange(_ context.Context, seq int64) (Exchange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if x, ok := s.xs[seq]; ok {
		return x, nil
	}
	return Exchange{}, ErrNotFound
}

func (s *MemoryStore) selected(f UsageFilter) []Call {
	var out []Call
	for _, c := range s.calls {
		if c.Seq > f.AfterSeq && f.match(c) {
			out = append(out, c)
		}
	}
	return out
}

func (s *MemoryStore) Calls(_ context.Context, f UsageFilter) ([]Call, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.selected(f)
	n := f.limit()
	if len(out) <= n {
		return out, false, nil
	}
	if f.AfterSeq > 0 {
		return out[:n], true, nil
	}
	return out[len(out)-n:], true, nil
}

func (s *MemoryStore) Summary(_ context.Context, f UsageFilter, group string) ([]SummaryRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return summarize(s.selected(f), group), nil
}

func (s *MemoryStore) PurgeCalls(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.calls[:0]
	var n int64
	for _, c := range s.calls {
		if c.At.Before(before) {
			n++
			delete(s.xs, c.Seq)
			continue
		}
		kept = append(kept, c)
	}
	s.calls = kept
	return n, nil
}

func (s *MemoryStore) BehaviorCosts(context.Context) ([]BehaviorCost, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]BehaviorCost, 0, len(s.costs))
	for _, c := range s.costs {
		out = append(out, c)
	}
	return out, nil
}

func (s *MemoryStore) PutBehaviorCost(_ context.Context, c BehaviorCost) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.costs[c.CostKey] = c
	return nil
}

func (s *MemoryStore) DeleteBehaviorCosts(_ context.Context, keys []CostKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.costs, k)
	}
	return nil
}

func (s *MemoryStore) ModelBaselines(context.Context) ([]ModelBaseline, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ModelBaseline, 0, len(s.bases))
	for _, b := range s.bases {
		out = append(out, b)
	}
	return out, nil
}

func (s *MemoryStore) PutModelBaseline(_ context.Context, b ModelBaseline) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bases[[2]string{b.Provider, b.Model}] = b
	return nil
}

func (s *MemoryStore) DeleteModelBaselines(_ context.Context, keys [][2]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.bases, k)
	}
	return nil
}
