package enginesvc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/intent"
)

func TestSQLiteStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "goap.db")
	db, err := platform.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := platform.MigrateSQLite(ctx, db, "engine", SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	s := SQLiteStore{DB: db}
	now := time.Now().UTC()
	for i, st := range []engine.Status{engine.StatusWaiting, engine.StatusRunning} {
		p := &engine.Process{ID: []string{"a", "b"}[i], Status: st, CreatedAt: now.Add(time.Duration(i) * time.Second),
			Children: map[string]string{"x#0:y": "c"}, Vars: map[string]any{"k": "v"}}
		if err := s.Put(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Get(ctx, "zz"); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("missing process: %v", err)
	}
	db.Close()

	// restart: the running process is marked interrupted, the waiting one resumes
	db, err = platform.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := platform.MigrateSQLite(ctx, db, "engine", SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	s = SQLiteStore{DB: db}
	if n, err := s.Interrupted(ctx); err != nil || n != 1 {
		t.Fatalf("interrupted %d %v", n, err)
	}
	ps, err := s.List(ctx)
	if err != nil || len(ps) != 2 || ps[0].ID != "b" || ps[0].Status != engine.StatusFailed || ps[1].Status != engine.StatusWaiting {
		t.Fatalf("list: %+v %v", ps, err)
	}
	if p, _ := s.Get(ctx, "a"); p.Children["x#0:y"] != "c" || p.Vars["k"] != "v" {
		t.Fatalf("round trip: %+v", p)
	}
}

func TestSQLiteStoreProcessLog(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "goap.db")
	db, err := platform.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := platform.MigrateSQLite(ctx, db, "engine", SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	s := SQLiteStore{DB: db}

	p := &engine.Process{ID: "p1", Status: engine.StatusClarifying, CreatedAt: time.Now().UTC()}
	p.Intent.Turns = []intent.Turn{{Role: "assistant", Text: "which goal?"}}
	if err := s.Put(ctx, p); err != nil {
		t.Fatal(err)
	}
	// Put must not persist Intent.Turns in the row body: a fresh read with no
	// process_log entries yet comes back empty (ADR 0031 — no full rewrite of
	// a growing dialogue).
	if got, err := s.Get(ctx, "p1"); err != nil || len(got.Intent.Turns) != 0 {
		t.Fatalf("expected no turns before any process_log entry, got %+v (%v)", got, err)
	}

	if err := s.AppendProcessLog(ctx, "p1",
		engine.ProcessLogEntry{Type: "intent.turn", Payload: map[string]any{"role": "assistant", "text": "which goal?"}},
		engine.ProcessLogEntry{Type: "intent.turn", Payload: map[string]any{"role": "user", "text": "prepare_change"}},
	); err != nil {
		t.Fatal(err)
	}
	entries, err := s.ListProcessLog(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Seq != 1 || entries[1].Seq != 2 {
		t.Fatalf("expected 2 ordered entries, got %+v", entries)
	}
	if entries[1].Payload["text"] != "prepare_change" {
		t.Fatalf("payload not round-tripped: %+v", entries[1])
	}

	// Get folds process_log back into Intent.Turns.
	got, err := s.Get(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Intent.Turns) != 2 || got.Intent.Turns[1].Text != "prepare_change" {
		t.Fatalf("expected turns folded from process_log, got %+v", got.Intent.Turns)
	}

	// process_log is scoped by process_id.
	if entries, err := s.ListProcessLog(ctx, "other"); err != nil || len(entries) != 0 {
		t.Fatalf("expected no entries for a different process, got %+v (%v)", entries, err)
	}
}
