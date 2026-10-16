package enginesvc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/lib/pgtest"
	"github.com/zimwip/goap/lib/platform"
)

// stores opens the process store in each dialect: SQLite always, PostgreSQL with GOAP_TEST_PG_DSN. Each opens the
// store of a replica, by its holder, on one database.
func stores(t *testing.T) map[string]func(holder string) SQLStore {
	t.Helper()
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := platform.MigrateSQLite(ctx, db, "engine", SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	out := map[string]func(string) SQLStore{"sqlite": func(h string) SQLStore { return SQLStore{DB: db, Holder: h} }}
	if os.Getenv("GOAP_TEST_PG_DSN") != "" {
		pg := stdlib.OpenDBFromPool(pgtest.Pool(t, Migrations))
		out["postgres"] = func(h string) SQLStore { return SQLStore{DB: pg, Dollar: true, Holder: h} }
	}
	return out
}

func TestSQLStore(t *testing.T) {
	for name, open := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := open("a")
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
			ps, err := s.List(ctx)
			if err != nil || len(ps) != 2 || ps[0].ID != "b" || ps[1].Status != engine.StatusWaiting {
				t.Fatalf("list: %+v %v", ps, err)
			}
			if p, _ := s.Get(ctx, "a"); p.Children["x#0:y"] != "c" || p.Vars["k"] != "v" {
				t.Fatalf("round trip: %+v", p)
			}
			if err := s.AppendProcessLog(ctx, "a", engine.ProcessLogEntry{Type: "intent.turn", Payload: map[string]any{"role": "user", "text": "hi"}}); err != nil {
				t.Fatal(err)
			}
			if p, _ := s.Get(ctx, "a"); len(p.Intent.Turns) != 1 || p.Intent.Turns[0].Text != "hi" {
				t.Fatalf("turns: %+v", p.Intent.Turns)
			}
		})
	}
}

func TestSQLStoreLocks(t *testing.T) {
	for name, open := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			a, b := open("a"), open("b")
			ttl := time.Minute
			if held, _, err := a.Acquire(ctx, "p", ttl); err != nil || !held {
				t.Fatalf("a acquires: %v %v", held, err)
			}
			if held, _, err := b.Acquire(ctx, "p", ttl); err != nil || held {
				t.Fatalf("b acquires a held lock: %v %v", held, err)
			}
			// a write by another replica than the holder is refused; the holder's goes through
			p := &engine.Process{ID: "p", Status: engine.StatusRunning, CreatedAt: time.Now()}
			if err := b.Put(ctx, p); !errors.Is(err, engine.ErrFenced) {
				t.Fatalf("b writes a's process: %v", err)
			}
			if err := a.Put(ctx, p); err != nil {
				t.Fatal(err)
			}
			// an interruption reaches the holder at its renewal
			if ok, err := b.Interrupt(ctx, "p", "bob"); err != nil || !ok {
				t.Fatalf("interrupt: %v %v", ok, err)
			}
			if ok, _ := a.Interrupt(ctx, "p", "self"); ok {
				t.Fatal("the holder interrupts itself")
			}
			if held, by, err := a.Acquire(ctx, "p", ttl); err != nil || !held || by != "bob" {
				t.Fatalf("renewal: %v %q %v", held, by, err)
			}
			// the holder is running and its lease is live: not an orphan
			if ids, err := b.Orphans(ctx, time.Now().Add(time.Second)); err != nil || len(ids) != 0 {
				t.Fatalf("orphans while held: %v %v", ids, err)
			}
			if err := a.Release(ctx, "p"); err != nil {
				t.Fatal(err)
			}
			if ids, err := b.Orphans(ctx, time.Now().Add(time.Second)); err != nil || len(ids) != 1 || ids[0] != "p" {
				t.Fatalf("orphans: %v %v", ids, err)
			}
			if ids, _ := b.Orphans(ctx, time.Now().Add(-time.Hour)); len(ids) != 0 {
				t.Fatalf("orphans written later: %v", ids)
			}
			// b takes it, interruption cleared; a lease that ends lets a take it back, and fences b out
			if held, by, err := b.Acquire(ctx, "p", 50*time.Millisecond); err != nil || !held || by != "" {
				t.Fatalf("b after release: %v %q %v", held, by, err)
			}
			time.Sleep(100 * time.Millisecond)
			if held, _, err := a.Acquire(ctx, "p", ttl); err != nil || !held {
				t.Fatalf("a after b's lease: %v %v", held, err)
			}
			if held, _, _ := b.Acquire(ctx, "p", ttl); held {
				t.Fatal("b renews a lock it lost")
			}
			if err := b.Put(ctx, p); !errors.Is(err, engine.ErrFenced) {
				t.Fatalf("b writes after losing its lease: %v", err)
			}
			if err := a.ReleaseAll(ctx); err != nil {
				t.Fatal(err)
			}
			if held, _, _ := b.Acquire(ctx, "p", ttl); !held {
				t.Fatal("b after release all")
			}
		})
	}
}
