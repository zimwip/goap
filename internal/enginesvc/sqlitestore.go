package enginesvc

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/intent"
)

// SQLiteMigrations holds the process schema of the local development mode.
//
//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// SQLiteStore is an engine.Store keeping processes in SQLite (local
// development mode), so that runs and pending human tasks survive restarts.
type SQLiteStore struct{ DB *sql.DB }

var _ engine.Store = SQLiteStore{}

// Get implements engine.Store.
func (s SQLiteStore) Get(ctx context.Context, id string) (*engine.Process, error) {
	var body string
	err := s.DB.QueryRowContext(ctx, `SELECT body FROM process WHERE id = ?`, id).Scan(&body)
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
// folded back in by Get/List.
func (s SQLiteStore) Put(ctx context.Context, p *engine.Process) error {
	stripped := *p
	stripped.Intent.Turns = nil
	body, err := json.Marshal(&stripped)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO process (id, status, created_at, body) VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET status = excluded.status, body = excluded.body`,
		p.ID, string(p.Status), p.CreatedAt.UTC().Format(time.RFC3339Nano), string(body))
	return err
}

// List implements engine.Store (newest first).
func (s SQLiteStore) List(ctx context.Context) ([]*engine.Process, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT body FROM process`)
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
func (s SQLiteStore) fillIntentTurns(ctx context.Context, p *engine.Process) error {
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
func (s SQLiteStore) AppendProcessLog(ctx context.Context, processID string, entries ...engine.ProcessLogEntry) error {
	for _, e := range entries {
		payload, err := json.Marshal(e.Payload)
		if err != nil {
			return err
		}
		at := e.At
		if at.IsZero() {
			at = time.Now().UTC()
		}
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO process_log (process_id, type, payload, at) VALUES (?, ?, ?, ?)`,
			processID, e.Type, string(payload), at.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return nil
}

// ListProcessLog implements engine.Store (oldest first).
func (s SQLiteStore) ListProcessLog(ctx context.Context, processID string) ([]engine.ProcessLogEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT seq, type, payload, at FROM process_log WHERE process_id = ? ORDER BY seq`, processID)
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

// Interrupted marks the processes left running by a previous run (the local
// mode has no work queue to resume them) as failed.
func (s SQLiteStore) Interrupted(ctx context.Context) (int, error) {
	ps, err := s.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range ps {
		if p.Status != engine.StatusRunning {
			continue
		}
		p.Status, p.Error, p.UpdatedAt = engine.StatusFailed, "interrupted: the platform was restarted while the process was running", time.Now().UTC()
		if err := s.Put(ctx, p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func sortNewest(ps []*engine.Process) {
	slices.SortStableFunc(ps, func(a, b *engine.Process) int { return b.CreatedAt.Compare(a.CreatedAt) })
}
