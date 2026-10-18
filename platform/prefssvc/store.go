// Package prefssvc keeps the personal preferences of the users outside the graph (ADR 0038): one JSON document per
// user, read and written by that user alone, saved as they change it.
package prefssvc

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/lib/outbox"
)

// Migrations holds the PostgreSQL schema; SQLiteMigrations the local mode one.
//
//go:embed migrations/*.sql
var Migrations embed.FS

//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// Prefs is the preference document of a user.
type Prefs = map[string]any

// Store keeps the documents.
type Store interface {
	// Get returns the document of a subject (empty until one is set).
	Get(ctx context.Context, subject string) (Prefs, error)
	// Update reads the document of a subject, lets fn change it and stores the result, atomically, with the events fn
	// returns (ADR 0136: written in the outbox in the same transaction; an error of fn writes nothing).
	Update(ctx context.Context, subject string, fn func(Prefs) (Prefs, []outbox.Message, error)) (Prefs, error)
	// Delete forgets the document of a subject; the events msgs are written with it, only when there was a document
	// to forget.
	Delete(ctx context.Context, subject string, msgs ...outbox.Message) error
}

// OutboxTable is the outbox table of the preferences (ADR 0136), named apart from the other services' since the local
// mode keeps every service in one database.
const OutboxTable = "preferences_outbox"

// MemoryStore is an in-memory Store (tests, GOAP_STORE=memory).
type MemoryStore struct {
	mu   sync.Mutex
	docs map[string]Prefs
	out  outbox.Memory
}

var (
	_ Store        = (*MemoryStore)(nil)
	_ outbox.Store = (*MemoryStore)(nil)
)

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{docs: map[string]Prefs{}} }

func (s *MemoryStore) Get(_ context.Context, subject string) (Prefs, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.docs[subject]), nil
}

func (s *MemoryStore) Update(_ context.Context, subject string, fn func(Prefs) (Prefs, []outbox.Message, error)) (Prefs, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, msgs, err := fn(maps.Clone(s.docs[subject]))
	if err != nil {
		return nil, err
	}
	s.docs[subject] = maps.Clone(out)
	s.out.Append(msgs...)
	return out, nil
}

func (s *MemoryStore) Delete(_ context.Context, subject string, msgs ...outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.docs[subject]; !ok {
		return nil
	}
	delete(s.docs, subject)
	s.out.Append(msgs...)
	return nil
}

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

// SQLStore is the Store on database/sql, for SQLite (local mode) and PostgreSQL (through the pgx stdlib adapter,
// Dollar set).
type SQLStore struct {
	DB *sql.DB
	// Dollar rewrites "?" placeholders as $1, $2… (PostgreSQL).
	Dollar bool
}

var (
	_ Store        = SQLStore{}
	_ outbox.Store = SQLStore{}
)

func (s SQLStore) outbox() outbox.SQL {
	return outbox.SQL{DB: s.DB, Postgres: s.Dollar, Table: OutboxTable}
}

// Pending implements outbox.Store.
func (s SQLStore) Pending(ctx context.Context, limit int) ([]outbox.Message, error) {
	return s.outbox().Pending(ctx, limit)
}

// MarkPublished implements outbox.Store.
func (s SQLStore) MarkPublished(ctx context.Context, seqs []int64, at time.Time) error {
	return s.outbox().MarkPublished(ctx, seqs, at)
}

// PurgePublished implements outbox.Store.
func (s SQLStore) PurgePublished(ctx context.Context, before time.Time) error {
	return s.outbox().PurgePublished(ctx, before)
}

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

func decode(raw string) (Prefs, error) {
	out := Prefs{}
	if raw == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("stored preferences: %w", err)
	}
	return out, nil
}

func (s SQLStore) Get(ctx context.Context, subject string) (Prefs, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, s.q(`SELECT prefs FROM user_preference WHERE subject = ?`), subject).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Prefs{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decode(raw)
}

func (s SQLStore) Update(ctx context.Context, subject string, fn func(Prefs) (Prefs, []outbox.Message, error)) (Prefs, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	lock := ""
	if s.Dollar {
		lock = " FOR UPDATE" // PostgreSQL: two writers of the same user do not lose each other's keys
	}
	var raw string
	switch err := tx.QueryRowContext(ctx, s.q(`SELECT prefs FROM user_preference WHERE subject = ?`+lock), subject).Scan(&raw); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	}
	cur, err := decode(raw)
	if err != nil {
		return nil, err
	}
	out, msgs, err := fn(cur)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO user_preference (subject, prefs, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (subject) DO UPDATE SET prefs = excluded.prefs, updated_at = excluded.updated_at`), subject, string(b), now); err != nil {
		return nil, err
	}
	if err := s.outbox().Append(ctx, tx, msgs...); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (s SQLStore) Delete(ctx context.Context, subject string, msgs ...outbox.Message) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, s.q(`DELETE FROM user_preference WHERE subject = ?`), subject)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		if err := s.outbox().Append(ctx, tx, msgs...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
