// Package credsvc keeps the local sign-in password of subjects that connect without an external identity
// provider (ADR 0040): one credential per subject, outside the graph, so a failed login never touches the
// audited graph/change log and no secret material ever becomes graph data. Store is a small interface so the
// concrete backend can move from its own SQL table (today) to being platform.Secrets/Vault-backed later,
// with no call-site change.
package credsvc

import (
	"cmp"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"slices"
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

// ErrExists is returned by Register when the subject already has a credential.
var ErrExists = errors.New("credential already exists")

// ErrNotFound is returned when a subject has no credential.
var ErrNotFound = errors.New("credential not found")

// Store keeps the password hashes. Every write of a fact takes the events of that fact (ADR 0136) and writes them in
// its outbox in the same transaction: a write refused or failed writes none. A Store the service emits from
// (Service.Emit) is an outbox.Store too, as MemoryStore and SQLStore are.
type Store interface {
	// Create inserts the hash of a new subject, with msgs. ErrExists if one is already there.
	Create(ctx context.Context, subject, hash string, msgs ...outbox.Message) error
	// Hash returns the stored hash of a subject. ErrNotFound if there is none.
	Hash(ctx context.Context, subject string) (string, error)
	// Set replaces the hash of a subject, with msgs. ErrNotFound if there is none.
	Set(ctx context.Context, subject, hash string, msgs ...outbox.Message) error
	// Exists reports whether a subject has a credential.
	Exists(ctx context.Context, subject string) (bool, error)

	// CreateSession records a new sign-in session (ADR 0045), with msgs, and forgets the sessions expired before
	// purgeBefore.
	CreateSession(ctx context.Context, sess Session, purgeBefore time.Time, msgs ...outbox.Message) error
	// Session returns a session. ErrNotFound if there is none.
	Session(ctx context.Context, id string) (Session, error)
	// RevokeSession ends a session (no error when it is unknown or already ended), with the events ended gives for
	// it (nil: none; not called with an empty list when nothing ended).
	RevokeSession(ctx context.Context, id string, at time.Time, ended Ended) error
	// RevokeSessions ends every session of a subject still open, with the events ended gives for them.
	RevokeSessions(ctx context.Context, subject string, at time.Time, ended Ended) error
}

// Ended gives the events of the sessions a revocation ends, written in its transaction; an error refuses the
// revocation. It is called only when at least one session ended.
type Ended func(sessions []Session) ([]outbox.Message, error)

func (f Ended) events(sessions []Session) ([]outbox.Message, error) {
	if f == nil || len(sessions) == 0 {
		return nil, nil
	}
	return f(sessions)
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
	out      outbox.Memory
}

var (
	_ Store        = (*MemoryStore)(nil)
	_ outbox.Store = (*MemoryStore)(nil)
)

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

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{hashes: map[string]string{}, sessions: map[string]Session{}}
}

func (s *MemoryStore) Create(_ context.Context, subject, hash string, msgs ...outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hashes[subject]; ok {
		return ErrExists
	}
	s.hashes[subject] = hash
	s.out.Append(msgs...)
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

func (s *MemoryStore) Set(_ context.Context, subject, hash string, msgs ...outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hashes[subject]; !ok {
		return ErrNotFound
	}
	s.hashes[subject] = hash
	s.out.Append(msgs...)
	return nil
}

func (s *MemoryStore) Exists(_ context.Context, subject string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.hashes[subject]
	return ok, nil
}

func (s *MemoryStore) CreateSession(_ context.Context, sess Session, purgeBefore time.Time, msgs ...outbox.Message) error {
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
	s.out.Append(msgs...)
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

func (s *MemoryStore) RevokeSession(_ context.Context, id string, at time.Time, ended Ended) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok && sess.Revoked.IsZero() {
		return s.revoke([]Session{sess}, at, ended)
	}
	return nil
}

func (s *MemoryStore) RevokeSessions(_ context.Context, subject string, at time.Time, ended Ended) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var open []Session
	for _, sess := range s.sessions {
		if sess.Subject == subject && sess.Revoked.IsZero() {
			open = append(open, sess)
		}
	}
	slices.SortFunc(open, func(a, b Session) int { return cmp.Or(a.Created.Compare(b.Created), cmp.Compare(a.ID, b.ID)) })
	return s.revoke(open, at, ended)
}

// revoke ends the open sessions with their events, none of them when the events cannot be built (s.mu held).
func (s *MemoryStore) revoke(open []Session, at time.Time, ended Ended) error {
	for i := range open {
		open[i].Revoked = at
	}
	msgs, err := ended.events(open)
	if err != nil {
		return err
	}
	for _, sess := range open {
		s.sessions[sess.ID] = sess
	}
	s.out.Append(msgs...)
	return nil
}

// SQLStore is the Store on database/sql, for SQLite (local mode) and PostgreSQL (through the pgx stdlib
// adapter, Dollar set).
type SQLStore struct {
	DB *sql.DB
	// Dollar rewrites "?" placeholders as $1, $2… (PostgreSQL).
	Dollar bool
}

var (
	_ Store        = SQLStore{}
	_ outbox.Store = SQLStore{}
)

// OutboxTable is the outbox of the SQL store (ADR 0136), named apart from the outboxes of the other services sharing a
// database in the local mode.
const OutboxTable = "credentials_outbox"

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

// inTx runs fn in a transaction, committed when it succeeds.
func (s SQLStore) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
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

func (s SQLStore) Create(ctx context.Context, subject, hash string, msgs ...outbox.Message) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.q(`INSERT INTO credential (subject, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)`), subject, hash, now, now)
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrExists
		}
		if err != nil {
			return err
		}
		return s.outbox().Append(ctx, tx, msgs...)
	})
}

func (s SQLStore) Hash(ctx context.Context, subject string) (string, error) {
	var hash string
	err := s.DB.QueryRowContext(ctx, s.q(`SELECT password_hash FROM credential WHERE subject = ?`), subject).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

func (s SQLStore) Set(ctx context.Context, subject, hash string, msgs ...outbox.Message) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, s.q(`UPDATE credential SET password_hash = ?, updated_at = ? WHERE subject = ?`), hash, now, subject)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return s.outbox().Append(ctx, tx, msgs...)
	})
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

func (s SQLStore) CreateSession(ctx context.Context, sess Session, purgeBefore time.Time, msgs ...outbox.Message) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM auth_session WHERE expires_at < ?`), stamp(purgeBefore)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, s.q(`INSERT INTO auth_session (id, subject, created_at, expires_at) VALUES (?, ?, ?, ?)`),
			sess.ID, sess.Subject, stamp(sess.Created), stamp(sess.Expires))
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrExists
		}
		if err != nil {
			return err
		}
		return s.outbox().Append(ctx, tx, msgs...)
	})
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

func (s SQLStore) RevokeSession(ctx context.Context, id string, at time.Time, ended Ended) error {
	return s.revoke(ctx, `id = ?`, id, at, ended)
}

func (s SQLStore) RevokeSessions(ctx context.Context, subject string, at time.Time, ended Ended) error {
	return s.revoke(ctx, `subject = ?`, subject, at, ended)
}

// revoke ends the open sessions matching where (one placeholder, arg), with their events, in one transaction: the
// UPDATE returns the sessions it ended, so two revocations racing never both report one.
func (s SQLStore) revoke(ctx context.Context, where string, arg any, at time.Time, ended Ended) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, s.q(`UPDATE auth_session SET revoked_at = ? WHERE `+where+` AND revoked_at IS NULL
			RETURNING id, subject, created_at, expires_at`), stamp(at), arg)
		if err != nil {
			return err
		}
		var done []Session
		for rows.Next() {
			sess := Session{Revoked: at}
			if err := rows.Scan(&sess.ID, &sess.Subject, sqlTime{&sess.Created}, sqlTime{&sess.Expires}); err != nil {
				_ = rows.Close()
				return err
			}
			done = append(done, sess)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		slices.SortFunc(done, func(a, b Session) int { return cmp.Or(a.Created.Compare(b.Created), cmp.Compare(a.ID, b.ID)) })
		msgs, err := ended.events(done)
		if err != nil {
			return err
		}
		return s.outbox().Append(ctx, tx, msgs...)
	})
}
