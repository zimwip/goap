// Package registrysvc stores methodologies as structured definitions and
// serves them over Connect. YAML is only an import/export format.
package registrysvc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/methodology"
)

// Status of a methodology version.
type Status string

const (
	StatusDraft     Status = "draft"
	StatusPublished Status = "published"
	StatusArchived  Status = "archived"
)

// Record is a stored methodology version.
type Record struct {
	Methodology methodology.Methodology
	Status      Status
	CreatedAt   time.Time
	UpdatedAt   time.Time
	PublishedAt time.Time
	UpdatedBy   string
}

// Store persists methodology versions.
type Store interface {
	// Save creates or replaces a draft. It fails with ErrImmutable when the
	// version exists and is not a draft.
	Save(ctx context.Context, r Record) error
	// Get returns a version; an empty version returns the latest published one.
	Get(ctx context.Context, name, version string) (Record, error)
	// List returns every version, sorted by name then creation date.
	List(ctx context.Context) ([]Record, error)
	SetStatus(ctx context.Context, name, version string, s Status, at time.Time) error
	// Delete removes a draft.
	Delete(ctx context.Context, name, version string) error
}

var (
	// ErrNotFound is returned for unknown methodologies.
	ErrNotFound = errors.New("methodology not found")
	// ErrImmutable is returned when modifying a published or archived version.
	ErrImmutable = errors.New("methodology version is not a draft")
)

// MemoryStore is an in-memory Store.
type MemoryStore struct {
	mu sync.RWMutex
	m  map[string]Record // key name@version
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[string]Record{}} }

func key(name, version string) string { return name + "@" + version }

func (s *MemoryStore) Save(_ context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(r.Methodology.Name, r.Methodology.Version)
	if old, ok := s.m[k]; ok {
		if old.Status != StatusDraft {
			return fmt.Errorf("%s: %w", k, ErrImmutable)
		}
		r.CreatedAt = old.CreatedAt
	}
	s.m[k] = r
	return nil
}

func (s *MemoryStore) Get(_ context.Context, name, version string) (Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if version != "" {
		r, ok := s.m[key(name, version)]
		if !ok {
			return Record{}, fmt.Errorf("%s: %w", key(name, version), ErrNotFound)
		}
		return r, nil
	}
	var best Record
	found := false
	for _, r := range s.m {
		if r.Methodology.Name == name && r.Status == StatusPublished && (!found || r.PublishedAt.After(best.PublishedAt)) {
			best, found = r, true
		}
	}
	if !found {
		return Record{}, fmt.Errorf("%s (published): %w", name, ErrNotFound)
	}
	return best, nil
}

func (s *MemoryStore) List(_ context.Context) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Record, 0, len(s.m))
	for _, r := range s.m {
		out = append(out, r)
	}
	sortRecords(out)
	return out, nil
}

func sortRecords(rs []Record) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Methodology.Name != rs[j].Methodology.Name {
			return rs[i].Methodology.Name < rs[j].Methodology.Name
		}
		return rs[i].CreatedAt.Before(rs[j].CreatedAt)
	})
}

func (s *MemoryStore) SetStatus(_ context.Context, name, version string, st Status, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(name, version)
	r, ok := s.m[k]
	if !ok {
		return fmt.Errorf("%s: %w", k, ErrNotFound)
	}
	r.Status = st
	r.UpdatedAt = at
	if st == StatusPublished {
		r.PublishedAt = at
	}
	s.m[k] = r
	return nil
}

func (s *MemoryStore) Delete(_ context.Context, name, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(name, version)
	r, ok := s.m[k]
	if !ok {
		return fmt.Errorf("%s: %w", k, ErrNotFound)
	}
	if r.Status != StatusDraft {
		return fmt.Errorf("%s: %w", k, ErrImmutable)
	}
	delete(s.m, k)
	return nil
}
