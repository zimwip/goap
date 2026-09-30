package registrysvc

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/algo"
)

// AlgorithmRecord is an algorithm centralized in the platform-wide registry (ADR 0041).
type AlgorithmRecord struct {
	Algorithm algo.Algorithm
	// SourceDomain and SourceVersion name the domain version that first published this algorithm.
	SourceDomain, SourceVersion string
	CreatedAt, UpdatedAt        time.Time
}

// AlgorithmStore persists the platform-wide algorithm registry: the registry's database
// (SQLAlgorithmStore), or the MemoryStore. Unlike a domain version, an algorithm has no draft/published
// lifecycle: PublishDomain centralizes it here (Service.centralizeAlgorithms), refusing a name already
// claimed by a different definition.
type AlgorithmStore interface {
	// SaveAlgorithm creates or replaces the named algorithm.
	SaveAlgorithm(ctx context.Context, r AlgorithmRecord) error
	GetAlgorithm(ctx context.Context, name string) (AlgorithmRecord, error)
	// ListAlgorithms returns every algorithm, sorted by name.
	ListAlgorithms(ctx context.Context) ([]AlgorithmRecord, error)
}

// algorithmsEqual reports whether a and b are the same algorithm definition (a domain reusing an
// already-centralized name must declare it identically).
func algorithmsEqual(a, b algo.Algorithm) bool { return reflect.DeepEqual(a, b) }

// errAlgorithmNotFound is ErrNotFound with a message that names an algorithm.
var errAlgorithmNotFound error = algorithmNotFound{}

type algorithmNotFound struct{}

func (algorithmNotFound) Error() string        { return "algorithm not found" }
func (algorithmNotFound) Is(target error) bool { return target == ErrNotFound }

// memoryAlgorithms is the algorithm half of MemoryStore.
type memoryAlgorithms struct {
	mu sync.RWMutex
	m  map[string]AlgorithmRecord
}

func (s *MemoryStore) SaveAlgorithm(_ context.Context, r AlgorithmRecord) error {
	s.alg.mu.Lock()
	defer s.alg.mu.Unlock()
	if s.alg.m == nil {
		s.alg.m = map[string]AlgorithmRecord{}
	}
	if old, ok := s.alg.m[r.Algorithm.Name]; ok {
		r.CreatedAt = old.CreatedAt
	}
	s.alg.m[r.Algorithm.Name] = r
	return nil
}

func (s *MemoryStore) GetAlgorithm(_ context.Context, name string) (AlgorithmRecord, error) {
	s.alg.mu.RLock()
	defer s.alg.mu.RUnlock()
	r, ok := s.alg.m[name]
	if !ok {
		return AlgorithmRecord{}, fmt.Errorf("%s: %w", name, errAlgorithmNotFound)
	}
	return r, nil
}

func (s *MemoryStore) ListAlgorithms(_ context.Context) ([]AlgorithmRecord, error) {
	s.alg.mu.RLock()
	defer s.alg.mu.RUnlock()
	out := make([]AlgorithmRecord, 0, len(s.alg.m))
	for _, r := range s.alg.m {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Algorithm.Name < out[j].Algorithm.Name })
	return out, nil
}
