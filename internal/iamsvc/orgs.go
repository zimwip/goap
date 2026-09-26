package iamsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/pkg/domain"
)

// Organization is a tenant: every change belongs to one.
type Organization struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

var (
	// ErrOrgNotFound is returned for an unknown organization.
	ErrOrgNotFound = errors.New("organization not found")
	// ErrOrgExists is returned when the id is already taken.
	ErrOrgExists = errors.New("organization already exists")
	// ErrOrgInvalid is returned for a malformed id or an empty name.
	ErrOrgInvalid = errors.New("invalid organization")
)

var orgID = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)
var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// NewOrganization validates and normalizes a creation request: an empty id is
// derived from the name.
func NewOrganization(id, name string) (Organization, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Organization{}, fmt.Errorf("name is required: %w", ErrOrgInvalid)
	}
	if id == "" {
		id = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	}
	if !orgID.MatchString(id) {
		return Organization{}, fmt.Errorf("id %q must be a slug (lowercase letters, digits, '-'): %w", id, ErrOrgInvalid)
	}
	return Organization{ID: id, Name: name}, nil
}

// OrgStore persists organizations.
type OrgStore interface {
	Create(ctx context.Context, o Organization) (Organization, error)
	Get(ctx context.Context, id string) (Organization, error)
	List(ctx context.Context) ([]Organization, error)
}

// MemoryOrgStore is an in-memory OrgStore holding the default organization.
type MemoryOrgStore struct {
	mu   sync.Mutex
	orgs map[string]Organization
}

// NewMemoryOrgStore returns a store seeded with the default organization.
func NewMemoryOrgStore() *MemoryOrgStore {
	return &MemoryOrgStore{orgs: map[string]Organization{
		domain.DefaultOrg: {ID: domain.DefaultOrg, Name: "Default organisation", CreatedAt: time.Now().UTC()},
	}}
}

func (s *MemoryOrgStore) Create(_ context.Context, o Organization) (Organization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orgs[o.ID]; ok {
		return Organization{}, fmt.Errorf("%s: %w", o.ID, ErrOrgExists)
	}
	o.CreatedAt = time.Now().UTC()
	s.orgs[o.ID] = o
	return o, nil
}

func (s *MemoryOrgStore) Get(_ context.Context, id string) (Organization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[id]
	if !ok {
		return Organization{}, fmt.Errorf("%s: %w", id, ErrOrgNotFound)
	}
	return o, nil
}

func (s *MemoryOrgStore) List(context.Context) ([]Organization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Organization, 0, len(s.orgs))
	for _, o := range s.orgs {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// PGOrgStore is an OrgStore on PostgreSQL.
type PGOrgStore struct{ Pool *pgxpool.Pool }

func (s PGOrgStore) Create(ctx context.Context, o Organization) (Organization, error) {
	err := s.Pool.QueryRow(ctx, `INSERT INTO organization (id, name) VALUES ($1, $2) RETURNING created_at`, o.ID, o.Name).Scan(&o.CreatedAt)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return Organization{}, fmt.Errorf("%s: %w", o.ID, ErrOrgExists)
	}
	return o, err
}

func (s PGOrgStore) Get(ctx context.Context, id string) (Organization, error) {
	var o Organization
	err := s.Pool.QueryRow(ctx, `SELECT id, name, created_at FROM organization WHERE id = $1`, id).Scan(&o.ID, &o.Name, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, fmt.Errorf("%s: %w", id, ErrOrgNotFound)
	}
	return o, err
}

func (s PGOrgStore) List(ctx context.Context) ([]Organization, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, name, created_at FROM organization ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Organization
	for rows.Next() {
		var o Organization
		if err := rows.Scan(&o.ID, &o.Name, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SQLiteOrgStore is an OrgStore on SQLite (local development mode).
type SQLiteOrgStore struct{ DB *sql.DB }

func (s SQLiteOrgStore) Create(ctx context.Context, o Organization) (Organization, error) {
	if _, err := s.Get(ctx, o.ID); err == nil {
		return Organization{}, fmt.Errorf("%s: %w", o.ID, ErrOrgExists)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO organization (id, name) VALUES (?, ?)`, o.ID, o.Name); err != nil {
		return Organization{}, err
	}
	return s.Get(ctx, o.ID)
}

func scanSQLiteOrg(sc interface{ Scan(...any) error }) (Organization, error) {
	var o Organization
	var ts string
	if err := sc.Scan(&o.ID, &o.Name, &ts); err != nil {
		return o, err
	}
	o.CreatedAt, _ = time.Parse(time.RFC3339Nano, ts)
	return o, nil
}

func (s SQLiteOrgStore) Get(ctx context.Context, id string) (Organization, error) {
	o, err := scanSQLiteOrg(s.DB.QueryRowContext(ctx, `SELECT id, name, created_at FROM organization WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return o, fmt.Errorf("%s: %w", id, ErrOrgNotFound)
	}
	return o, err
}

func (s SQLiteOrgStore) List(ctx context.Context) ([]Organization, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, created_at FROM organization ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Organization
	for rows.Next() {
		o, err := scanSQLiteOrg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
