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
	return &p, json.Unmarshal([]byte(body), &p)
}

// Put implements engine.Store.
func (s SQLiteStore) Put(ctx context.Context, p *engine.Process) error {
	body, err := json.Marshal(p)
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
	sortNewest(out)
	return out, nil
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
