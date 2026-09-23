package enginesvc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/engine"
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
