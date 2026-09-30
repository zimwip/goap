// Package credsvc keeps the local sign-in password of subjects that connect without an external identity
// provider (ADR 0040): one credential per subject, outside the graph, so a failed login never touches the
// audited graph/change log and no secret material ever becomes graph data. Store is a small interface so the
// concrete backend can move from its own SQL table (today) to being platform.Secrets/Vault-backed later,
// with no call-site change.
package credsvc

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Migrations holds the PostgreSQL schema; SQLiteMigrations the local mode one.
//
//go:embed migrations/*.sql
var Migrations embed.FS

//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// ErrExists is returned by Register when the subject already has a credential.
var ErrExists = errors.New("credential already exists")

// ErrNotFound is returned when a subject has no credential.
var ErrNotFound = errors.New("credential not found")

// Store keeps the password hashes.
type Store interface {
	// Create inserts the hash of a new subject. ErrExists if one is already there.
	Create(ctx context.Context, subject, hash string) error
	// Hash returns the stored hash of a subject. ErrNotFound if there is none.
	Hash(ctx context.Context, subject string) (string, error)
	// Set replaces the hash of a subject. ErrNotFound if there is none.
	Set(ctx context.Context, subject, hash string) error
	// Exists reports whether a subject has a credential.
	Exists(ctx context.Context, subject string) (bool, error)
}

// MemoryStore is an in-memory Store (tests, GOAP_STORE=memory).
type MemoryStore struct{ hashes map[string]string }

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{hashes: map[string]string{}} }

func (s *MemoryStore) Create(_ context.Context, subject, hash string) error {
	if _, ok := s.hashes[subject]; ok {
		return ErrExists
	}
	s.hashes[subject] = hash
	return nil
}

func (s *MemoryStore) Hash(_ context.Context, subject string) (string, error) {
	h, ok := s.hashes[subject]
	if !ok {
		return "", ErrNotFound
	}
	return h, nil
}

func (s *MemoryStore) Set(_ context.Context, subject, hash string) error {
	if _, ok := s.hashes[subject]; !ok {
		return ErrNotFound
	}
	s.hashes[subject] = hash
	return nil
}

func (s *MemoryStore) Exists(_ context.Context, subject string) (bool, error) {
	_, ok := s.hashes[subject]
	return ok, nil
}

// SQLStore is the Store on database/sql, for SQLite (local mode) and PostgreSQL (through the pgx stdlib
// adapter, Dollar set).
type SQLStore struct {
	DB *sql.DB
	// Dollar rewrites "?" placeholders as $1, $2… (PostgreSQL).
	Dollar bool
}

var _ Store = SQLStore{}

func (s SQLStore) q(query string) string {
	if !s.Dollar {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s SQLStore) Create(ctx context.Context, subject, hash string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.DB.ExecContext(ctx, s.q(`INSERT INTO credential (subject, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)`), subject, hash, now, now)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return ErrExists
	}
	return err
}

func (s SQLStore) Hash(ctx context.Context, subject string) (string, error) {
	var hash string
	err := s.DB.QueryRowContext(ctx, s.q(`SELECT password_hash FROM credential WHERE subject = ?`), subject).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

func (s SQLStore) Set(ctx context.Context, subject, hash string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.DB.ExecContext(ctx, s.q(`UPDATE credential SET password_hash = ?, updated_at = ? WHERE subject = ?`), hash, now, subject)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s SQLStore) Exists(ctx context.Context, subject string) (bool, error) {
	var one int
	err := s.DB.QueryRowContext(ctx, s.q(`SELECT 1 FROM credential WHERE subject = ?`), subject).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
