// Package registrysvc stores methodology definitions and serves them over Connect.
package registrysvc

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/internal/rpcerr"
)

// Migrations holds the registry schema.
//
//go:embed migrations/*.sql
var Migrations embed.FS

// Goal summarizes a goal.
type Goal struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Record is a published methodology version.
type Record struct {
	Name        string
	Version     string
	Description string
	Source      string
	Goals       []Goal
	PublishedAt time.Time
}

// Store persists records. Get with an empty version returns the latest one.
type Store interface {
	Put(ctx context.Context, r Record) error
	Get(ctx context.Context, name, version string) (Record, error)
	List(ctx context.Context) ([]Record, error)
}

// ErrExists is returned when publishing another source under an existing version.
var ErrExists = errors.New("methodology version already published with a different source")

// MemoryStore is an in-memory Store.
type MemoryStore struct {
	mu sync.RWMutex
	m  map[string][]Record
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[string][]Record{}} }

func (s *MemoryStore) Put(_ context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.m[r.Name] {
		if old.Version == r.Version {
			if old.Source == r.Source {
				return nil
			}
			return ErrExists
		}
	}
	s.m[r.Name] = append(s.m[r.Name], r)
	return nil
}

func (s *MemoryStore) Get(_ context.Context, name, version string) (Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rs := s.m[name]
	if len(rs) == 0 {
		return Record{}, fmt.Errorf("methodology %s: %w", name, rpcerr.ErrNotFound)
	}
	if version == "" {
		return rs[len(rs)-1], nil
	}
	for _, r := range rs {
		if r.Version == version {
			return r, nil
		}
	}
	return Record{}, fmt.Errorf("methodology %s@%s: %w", name, version, rpcerr.ErrNotFound)
}

func (s *MemoryStore) List(_ context.Context) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Record
	for _, rs := range s.m {
		out = append(out, rs[len(rs)-1])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// PostgresStore is a PostgreSQL Store.
type PostgresStore struct{ Pool *pgxpool.Pool }

func (s PostgresStore) Put(ctx context.Context, r Record) error {
	goals, _ := json.Marshal(r.Goals)
	tag, err := s.Pool.Exec(ctx, `INSERT INTO methodology (name, version, description, source, goals, published_at)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (name, version) DO NOTHING`,
		r.Name, r.Version, r.Description, r.Source, goals, r.PublishedAt)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	old, err := s.Get(ctx, r.Name, r.Version)
	if err != nil {
		return err
	}
	if old.Source != r.Source {
		return ErrExists
	}
	return nil
}

const cols = `name, version, description, source, goals, published_at`

func scan(row pgx.Row) (Record, error) {
	var r Record
	var goals []byte
	if err := row.Scan(&r.Name, &r.Version, &r.Description, &r.Source, &goals, &r.PublishedAt); err != nil {
		return r, err
	}
	_ = json.Unmarshal(goals, &r.Goals)
	return r, nil
}

func (s PostgresStore) Get(ctx context.Context, name, version string) (Record, error) {
	q := `SELECT ` + cols + ` FROM methodology WHERE name = $1 AND ($2 = '' OR version = $2) ORDER BY published_at DESC LIMIT 1`
	r, err := scan(s.Pool.QueryRow(ctx, q, name, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, fmt.Errorf("methodology %s@%s: %w", name, version, rpcerr.ErrNotFound)
	}
	return r, err
}

func (s PostgresStore) List(ctx context.Context) ([]Record, error) {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT ON (name) `+cols+` FROM methodology ORDER BY name, published_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
