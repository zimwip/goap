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
	"sync"
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

	// CreateSession records a new sign-in session (ADR 0045), and forgets the sessions expired before purgeBefore.
	CreateSession(ctx context.Context, sess Session, purgeBefore time.Time) error
	// Session returns a session. ErrNotFound if there is none.
	Session(ctx context.Context, id string) (Session, error)
	// RevokeSession ends a session (no error when it is unknown or already ended).
	RevokeSession(ctx context.Context, id string, at time.Time) error
	// RevokeSessions ends every session of a subject still open.
	RevokeSessions(ctx context.Context, subject string, at time.Time) error
}

// Session is a sign-in (ADR 0045): every token issued from it carries its id; it ends when its subject signs out
// (Revoked), changes their password, or it expires.
type Session struct {
	ID, Subject string
	Created     time.Time
	Expires     time.Time
	// Revoked is when the session was ended, zero while it is open.
	Revoked time.Time
}

// Active reports whether the session still accepts its tokens at t.
func (s Session) Active(t time.Time) bool { return s.Revoked.IsZero() && t.Before(s.Expires) }

// MemoryStore is an in-memory Store (tests, GOAP_STORE=memory).
type MemoryStore struct {
	mu       sync.Mutex
	hashes   map[string]string
	sessions map[string]Session
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{hashes: map[string]string{}, sessions: map[string]Session{}}
}

func (s *MemoryStore) Create(_ context.Context, subject, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hashes[subject]; ok {
		return ErrExists
	}
	s.hashes[subject] = hash
	return nil
}

func (s *MemoryStore) Hash(_ context.Context, subject string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hashes[subject]
	if !ok {
		return "", ErrNotFound
	}
	return h, nil
}

func (s *MemoryStore) Set(_ context.Context, subject, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hashes[subject]; !ok {
		return ErrNotFound
	}
	s.hashes[subject] = hash
	return nil
}

func (s *MemoryStore) Exists(_ context.Context, subject string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.hashes[subject]
	return ok, nil
}

func (s *MemoryStore) CreateSession(_ context.Context, sess Session, purgeBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, o := range s.sessions {
		if o.Expires.Before(purgeBefore) {
			delete(s.sessions, id)
		}
	}
	if _, ok := s.sessions[sess.ID]; ok {
		return ErrExists
	}
	s.sessions[sess.ID] = sess
	return nil
}

func (s *MemoryStore) Session(_ context.Context, id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	return sess, nil
}

func (s *MemoryStore) RevokeSession(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok && sess.Revoked.IsZero() {
		sess.Revoked = at
		s.sessions[id] = sess
	}
	return nil
}

func (s *MemoryStore) RevokeSessions(_ context.Context, subject string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.sessions {
		if sess.Subject == subject && sess.Revoked.IsZero() {
			sess.Revoked = at
			s.sessions[id] = sess
		}
	}
	return nil
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

// stamp is how the SQL store writes a time: RFC 3339 in UTC, which sorts as text (SQLite) and casts to timestamptz
// (PostgreSQL).
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// sqlTime scans a time written by stamp: text in SQLite, timestamptz in PostgreSQL; NULL is the zero time.
type sqlTime struct{ t *time.Time }

func (s sqlTime) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		*s.t = time.Time{}
	case time.Time:
		*s.t = x
	case string:
		return s.parse(x)
	case []byte:
		return s.parse(string(x))
	default:
		return fmt.Errorf("unexpected time %T", v)
	}
	return nil
}

func (s sqlTime) parse(v string) error {
	t, err := time.Parse(time.RFC3339Nano, v)
	*s.t = t
	return err
}

func (s SQLStore) CreateSession(ctx context.Context, sess Session, purgeBefore time.Time) error {
	if _, err := s.DB.ExecContext(ctx, s.q(`DELETE FROM auth_session WHERE expires_at < ?`), stamp(purgeBefore)); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, s.q(`INSERT INTO auth_session (id, subject, created_at, expires_at) VALUES (?, ?, ?, ?)`),
		sess.ID, sess.Subject, stamp(sess.Created), stamp(sess.Expires))
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return ErrExists
	}
	return err
}

func (s SQLStore) Session(ctx context.Context, id string) (Session, error) {
	sess := Session{ID: id}
	err := s.DB.QueryRowContext(ctx, s.q(`SELECT subject, created_at, expires_at, revoked_at FROM auth_session WHERE id = ?`), id).
		Scan(&sess.Subject, sqlTime{&sess.Created}, sqlTime{&sess.Expires}, sqlTime{&sess.Revoked})
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return sess, err
}

func (s SQLStore) RevokeSession(ctx context.Context, id string, at time.Time) error {
	_, err := s.DB.ExecContext(ctx, s.q(`UPDATE auth_session SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`), stamp(at), id)
	return err
}

func (s SQLStore) RevokeSessions(ctx context.Context, subject string, at time.Time) error {
	_, err := s.DB.ExecContext(ctx, s.q(`UPDATE auth_session SET revoked_at = ? WHERE subject = ? AND revoked_at IS NULL`), stamp(at), subject)
	return err
}
