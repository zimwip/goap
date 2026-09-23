package registrysvc

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/methodology"
)

// DomainRecord is a stored domain version.
type DomainRecord struct {
	Domain      methodology.Domain
	Status      Status
	CreatedAt   time.Time
	UpdatedAt   time.Time
	PublishedAt time.Time
	UpdatedBy   string
}

// DomainStore persists domain versions (same lifecycle as methodologies).
// The concrete stores implement both Store and DomainStore.
type DomainStore interface {
	// SaveDomain creates or replaces a draft (ErrImmutable otherwise).
	SaveDomain(ctx context.Context, r DomainRecord) error
	// GetDomain returns a version; an empty version returns the latest published one.
	GetDomain(ctx context.Context, name, version string) (DomainRecord, error)
	// ListDomains returns every version, sorted by name then creation date.
	ListDomains(ctx context.Context) ([]DomainRecord, error)
	SetDomainStatus(ctx context.Context, name, version string, s Status, at time.Time) error
	// DeleteDomain removes a draft.
	DeleteDomain(ctx context.Context, name, version string) error
}

func sortDomainRecords(rs []DomainRecord) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Domain.Name != rs[j].Domain.Name {
			return rs[i].Domain.Name < rs[j].Domain.Name
		}
		return rs[i].CreatedAt.Before(rs[j].CreatedAt)
	})
}

// memoryDomains is the domain half of MemoryStore.
type memoryDomains struct {
	mu sync.RWMutex
	m  map[string]DomainRecord
}

func (s *MemoryStore) SaveDomain(_ context.Context, r DomainRecord) error {
	s.dom.mu.Lock()
	defer s.dom.mu.Unlock()
	if s.dom.m == nil {
		s.dom.m = map[string]DomainRecord{}
	}
	k := key(r.Domain.Name, r.Domain.Version)
	if old, ok := s.dom.m[k]; ok {
		if old.Status != StatusDraft {
			return fmt.Errorf("domain %s: %w", k, ErrImmutable)
		}
		r.CreatedAt = old.CreatedAt
	}
	s.dom.m[k] = r
	return nil
}

func (s *MemoryStore) GetDomain(_ context.Context, name, version string) (DomainRecord, error) {
	s.dom.mu.RLock()
	defer s.dom.mu.RUnlock()
	if version != "" {
		r, ok := s.dom.m[key(name, version)]
		if !ok {
			return DomainRecord{}, fmt.Errorf("%s: %w", key(name, version), errDomainNotFound)
		}
		return r, nil
	}
	var best DomainRecord
	found := false
	for _, r := range s.dom.m {
		if r.Domain.Name == name && r.Status == StatusPublished && (!found || r.PublishedAt.After(best.PublishedAt)) {
			best, found = r, true
		}
	}
	if !found {
		return DomainRecord{}, fmt.Errorf("%s (published): %w", name, errDomainNotFound)
	}
	return best, nil
}

func (s *MemoryStore) ListDomains(_ context.Context) ([]DomainRecord, error) {
	s.dom.mu.RLock()
	defer s.dom.mu.RUnlock()
	out := make([]DomainRecord, 0, len(s.dom.m))
	for _, r := range s.dom.m {
		out = append(out, r)
	}
	sortDomainRecords(out)
	return out, nil
}

func (s *MemoryStore) SetDomainStatus(_ context.Context, name, version string, st Status, at time.Time) error {
	s.dom.mu.Lock()
	defer s.dom.mu.Unlock()
	k := key(name, version)
	r, ok := s.dom.m[k]
	if !ok {
		return fmt.Errorf("%s: %w", k, errDomainNotFound)
	}
	r.Status, r.UpdatedAt = st, at
	if st == StatusPublished {
		r.PublishedAt = at
	}
	s.dom.m[k] = r
	return nil
}

func (s *MemoryStore) DeleteDomain(_ context.Context, name, version string) error {
	s.dom.mu.Lock()
	defer s.dom.mu.Unlock()
	k := key(name, version)
	r, ok := s.dom.m[k]
	if !ok {
		return fmt.Errorf("%s: %w", k, errDomainNotFound)
	}
	if r.Status != StatusDraft {
		return fmt.Errorf("domain %s: %w", k, ErrImmutable)
	}
	delete(s.dom.m, k)
	return nil
}
