package enginesvc

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/intent"
)

// Migrations holds the PostgreSQL process schema; SQLiteMigrations the local mode one.
//
//go:embed migrations/*.sql
var Migrations embed.FS

// SQLiteMigrations holds the process schema of the local development mode.
//
//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// SQLStore is an engine.Store keeping processes in SQLite (local development mode) or PostgreSQL (through the pgx
// stdlib adapter, Dollar set), so that runs and pending human tasks survive restarts. It is also the lock service
// of the engine replicas sharing it (engine.Locker, ADR 0124) and tells the processes none of them works on
// (engine.OrphanFinder).
type SQLStore struct {
	DB *sql.DB
	// Dollar rewrites "?" placeholders as $1, $2… (PostgreSQL).
	Dollar bool
	// Holder names this replica in the locks it takes (NewHolder); its writes to a process whose lock another
	// replica holds are refused (engine.ErrFenced).
	Holder string
}

var (
	_ engine.Store        = SQLStore{}
	_ engine.Locker       = SQLStore{}
	_ engine.OrphanFinder = SQLStore{}
)

// NewHolder is a replica identity for SQLStore.Holder: host, process and a random part.
func NewHolder() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), uuid.NewString()[:8])
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

func millis(t time.Time) int64 { return t.UnixMilli() }

// Get implements engine.Store.
func (s SQLStore) Get(ctx context.Context, id string) (*engine.Process, error) {
	var body string
	err := s.DB.QueryRowContext(ctx, s.q(`SELECT body FROM process WHERE id = ?`), id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s: %w", id, engine.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var p engine.Process
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		return nil, err
	}
	if err := s.fillIntentTurns(ctx, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Put implements engine.Store. Intent.Turns is stripped from the stored body:
// it only ever grows, and re-serializing the whole (possibly long) dialogue
// on every turn would rewrite the row for no reason (ADR 0031) — each turn
// is instead appended to process_log by the engine (AppendProcessLog) and
// folded back in by Get/List. The write is refused (engine.ErrFenced) while
// another replica holds the lock of the process (ADR 0124).
func (s SQLStore) Put(ctx context.Context, p *engine.Process) error {
	stripped := *p
	stripped.Intent.Turns = nil
	body, err := json.Marshal(&stripped)
	if err != nil {
		return err
	}
	now := millis(time.Now())
	res, err := s.DB.ExecContext(ctx, s.q(`INSERT INTO process (id, status, created_at, updated_at, body)
		SELECT CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS BIGINT), CAST(? AS TEXT)
		WHERE NOT EXISTS (SELECT 1 FROM engine_lock WHERE key = ? AND holder <> ? AND lease_until > ?)
		ON CONFLICT (id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at, body = excluded.body`),
		p.ID, string(p.Status), p.CreatedAt.UTC().Format(time.RFC3339Nano), now, string(body), p.ID, s.Holder, now)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%s: %w", p.ID, engine.ErrFenced)
	}
	return nil
}

// Acquire implements engine.Locker.
func (s SQLStore) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, string, error) {
	now := time.Now()
	var interrupt string
	err := s.DB.QueryRowContext(ctx, s.q(`INSERT INTO engine_lock (key, holder, lease_until, interrupt) VALUES (?, ?, ?, '')
		ON CONFLICT (key) DO UPDATE SET holder = excluded.holder, lease_until = excluded.lease_until,
			interrupt = CASE WHEN engine_lock.holder = excluded.holder THEN engine_lock.interrupt ELSE '' END
		WHERE engine_lock.holder = excluded.holder OR engine_lock.lease_until <= ?
		RETURNING interrupt`), key, s.Holder, millis(now.Add(ttl)), millis(now)).Scan(&interrupt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil // another replica's lease runs
	}
	if err != nil {
		return false, "", err
	}
	return true, interrupt, nil
}

// Release implements engine.Locker.
func (s SQLStore) Release(ctx context.Context, key string) error {
	_, err := s.DB.ExecContext(ctx, s.q(`DELETE FROM engine_lock WHERE key = ? AND holder = ?`), key, s.Holder)
	return err
}

// Interrupt implements engine.Locker.
func (s SQLStore) Interrupt(ctx context.Context, key, by string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, s.q(`UPDATE engine_lock SET interrupt = ? WHERE key = ? AND holder <> ? AND lease_until > ?`),
		by, key, s.Holder, millis(time.Now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ReleaseAll drops every lock: for the local mode only, the one process working on its database file, at start,
// so that what the previous run left is resumed at once rather than once its leases end.
func (s SQLStore) ReleaseAll(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM engine_lock`)
	return err
}

// Orphans implements engine.OrphanFinder.
func (s SQLStore) Orphans(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT id FROM process p WHERE status = ? AND updated_at < ?
		AND NOT EXISTS (SELECT 1 FROM engine_lock l WHERE l.key = p.id AND l.lease_until > ?)`),
		string(engine.StatusRunning), millis(before), millis(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// List implements engine.Store (newest first).
func (s SQLStore) List(ctx context.Context) ([]*engine.Process, error) {
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT body FROM process`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*engine.Process
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var p engine.Process
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, p := range out {
		if err := s.fillIntentTurns(ctx, p); err != nil {
			return nil, err
		}
	}
	sortNewest(out)
	return out, nil
}

// fillIntentTurns reconstructs p.Intent.Turns from process_log, since Put no
// longer persists it in the process row's body.
func (s SQLStore) fillIntentTurns(ctx context.Context, p *engine.Process) error {
	entries, err := s.ListProcessLog(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type != "intent.turn" {
			continue
		}
		p.Intent.Turns = append(p.Intent.Turns, intent.Turn{
			Role: fmt.Sprint(e.Payload["role"]),
			Text: fmt.Sprint(e.Payload["text"]),
		})
	}
	return nil
}

// AppendProcessLog implements engine.Store.
func (s SQLStore) AppendProcessLog(ctx context.Context, processID string, entries ...engine.ProcessLogEntry) error {
	for _, e := range entries {
		payload, err := json.Marshal(e.Payload)
		if err != nil {
			return err
		}
		at := e.At
		if at.IsZero() {
			at = time.Now().UTC()
		}
		if _, err := s.DB.ExecContext(ctx, s.q(`INSERT INTO process_log (process_id, type, payload, at) VALUES (?, ?, ?, ?)`),
			processID, e.Type, string(payload), at.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return nil
}

// ListProcessLog implements engine.Store (oldest first).
func (s SQLStore) ListProcessLog(ctx context.Context, processID string) ([]engine.ProcessLogEntry, error) {
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT seq, type, payload, at FROM process_log WHERE process_id = ? ORDER BY seq`), processID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []engine.ProcessLogEntry
	for rows.Next() {
		var (
			e       engine.ProcessLogEntry
			payload string
			at      string
		)
		if err := rows.Scan(&e.Seq, &e.Type, &payload, &at); err != nil {
			return nil, err
		}
		e.ProcessID = processID
		if payload != "" {
			if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
				return nil, err
			}
		}
		if e.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func sortNewest(ps []*engine.Process) {
	slices.SortStableFunc(ps, func(a, b *engine.Process) int { return b.CreatedAt.Compare(a.CreatedAt) })
}
