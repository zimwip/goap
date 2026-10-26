package domainsvc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/outbox"
)

// Store persists domain versions (same lifecycle as methodologies): the database of the domain registry (SQLStore), or
// the MemoryStore.
type Store interface {
	// SaveDomain creates or replaces a draft (ErrImmutable otherwise). The write methods store the events given with
	// them in the outbox of the registry, in the same transaction (ADR 0136): a write refused writes none.
	SaveDomain(ctx context.Context, r def.Record, events ...outbox.Message) error
	// GetDomain returns a version; an empty version returns the latest published one.
	GetDomain(ctx context.Context, name, version string) (def.Record, error)
	// ListDomains returns every version, sorted by name then creation date.
	ListDomains(ctx context.Context) ([]def.Record, error)
	SetDomainStatus(ctx context.Context, name, version string, s def.Status, at time.Time, events ...outbox.Message) error
	// DeleteDomain removes a draft.
	DeleteDomain(ctx context.Context, name, version string, events ...outbox.Message) error
	// Protected returns the names of the protected domains (ADR 0167).
	Protected(ctx context.Context) (map[string]bool, error)
	// SetProtected protects a domain (on) or lifts its protection, whatever its versions (a name not stored yet
	// included: the versions written later are protected).
	SetProtected(ctx context.Context, name string, on bool, by string, at time.Time) error
}

func sortDomainRecords(rs []def.Record) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Domain.Name != rs[j].Domain.Name {
			return rs[i].Domain.Name < rs[j].Domain.Name
		}
		return rs[i].CreatedAt.Before(rs[j].CreatedAt)
	})
}

var (
	// ErrNotFound is returned for an unknown domain version.
	ErrNotFound = errors.New("domain not found")
	// ErrImmutable is returned when modifying a published or archived version, or a built-in domain.
	ErrImmutable = errors.New("domain version is not a draft")
)

// MemoryStore is an in-memory Store.
type MemoryStore struct {
	mu        sync.RWMutex
	m         map[string]def.Record // key name@version
	protected map[string]bool
	out       outbox.Memory
}

var _ outbox.Store = (*MemoryStore)(nil)

// Pending implements outbox.Store.
func (s *MemoryStore) Pending(ctx context.Context, limit int) ([]outbox.Message, error) {
	return s.out.Pending(ctx, limit)
}

// MarkPublished implements outbox.Store.
func (s *MemoryStore) MarkPublished(ctx context.Context, seqs []int64, at time.Time) error {
	return s.out.MarkPublished(ctx, seqs, at)
}

// PurgePublished implements outbox.Store.
func (s *MemoryStore) PurgePublished(ctx context.Context, before time.Time) error {
	return s.out.PurgePublished(ctx, before)
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[string]def.Record{}} }

func key(name, version string) string { return name + "@" + version }

func (s *MemoryStore) SaveDomain(_ context.Context, r def.Record, events ...outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]def.Record{}
	}
	k := key(r.Domain.Name, r.Domain.Version)
	if old, ok := s.m[k]; ok {
		if old.Status != def.StatusDraft {
			return fmt.Errorf("domain %s: %w", k, ErrImmutable)
		}
		r.CreatedAt = old.CreatedAt
	}
	s.m[k] = r
	s.out.Append(events...)
	return nil
}

func (s *MemoryStore) GetDomain(_ context.Context, name, version string) (def.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if version != "" {
		r, ok := s.m[key(name, version)]
		if !ok {
			return def.Record{}, fmt.Errorf("%s: %w", key(name, version), ErrNotFound)
		}
		return r, nil
	}
	var best def.Record
	found := false
	for _, r := range s.m {
		if r.Domain.Name == name && r.Status == def.StatusPublished && (!found || r.PublishedAt.After(best.PublishedAt)) {
			best, found = r, true
		}
	}
	if !found {
		return def.Record{}, fmt.Errorf("%s (published): %w", name, ErrNotFound)
	}
	return best, nil
}

func (s *MemoryStore) ListDomains(_ context.Context) ([]def.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]def.Record, 0, len(s.m))
	for _, r := range s.m {
		out = append(out, r)
	}
	sortDomainRecords(out)
	return out, nil
}

func (s *MemoryStore) SetDomainStatus(_ context.Context, name, version string, st def.Status, at time.Time, events ...outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(name, version)
	r, ok := s.m[k]
	if !ok {
		return fmt.Errorf("%s: %w", k, ErrNotFound)
	}
	r.Status, r.UpdatedAt = st, at
	if st == def.StatusPublished {
		r.PublishedAt = at
	}
	s.m[k] = r
	s.out.Append(events...)
	return nil
}

func (s *MemoryStore) DeleteDomain(_ context.Context, name, version string, events ...outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(name, version)
	r, ok := s.m[k]
	if !ok {
		return fmt.Errorf("%s: %w", k, ErrNotFound)
	}
	if r.Status != def.StatusDraft {
		return fmt.Errorf("domain %s: %w", k, ErrImmutable)
	}
	delete(s.m, k)
	s.out.Append(events...)
	return nil
}

func (s *MemoryStore) Protected(context.Context) (map[string]bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.protected))
	for n := range s.protected {
		out[n] = true
	}
	return out, nil
}

func (s *MemoryStore) SetProtected(_ context.Context, name string, on bool, _ string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.protected == nil {
		s.protected = map[string]bool{}
	}
	if on {
		s.protected[name] = true
	} else {
		delete(s.protected, name)
	}
	return nil
}
