package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/domain"
)

// forEachRepo runs f against the in-memory repository and, when
// GOAP_TEST_PG_DSN is set, against PostgreSQL in a fresh schema.
func forEachRepo(t *testing.T, f func(t *testing.T, repo Repo)) {
	t.Run("memory", func(t *testing.T) {
		repo := NewMemory()
		f(t, repo)
		checkImpactLogs(t, repo)
		checkLandings(t, repo)
	})
	t.Run("sqlite", func(t *testing.T) {
		ctx := context.Background()
		db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := platform.MigrateSQLite(ctx, db, "graph", SQLiteMigrations, "migrations_sqlite"); err != nil {
			t.Fatal(err)
		}
		repo := NewSQLite(db)
		f(t, repo)
		checkImpactLogs(t, repo)
		checkLandings(t, repo)
	})
	dsn := os.Getenv("GOAP_TEST_PG_DSN")
	if dsn == "" {
		return
	}
	t.Run("postgres", func(t *testing.T) {
		ctx := context.Background()
		schema := fmt.Sprintf("graph_test_%d", time.Now().UnixNano())
		admin, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
			t.Fatal(err)
		}
		defer admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") //nolint:errcheck
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		if err := platform.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), pool, Migrations, "migrations"); err != nil {
			t.Fatal(err)
		}
		repo := NewPostgres(pool)
		f(t, repo)
		checkImpactLogs(t, repo)
		checkLandings(t, repo)
	})
}

// checkImpactLogs replays the impact log of every change and compares it with the change_impact projection
// (ADR 0029): nothing may write the projection without an event.
func checkImpactLogs(t *testing.T, repo Repo) {
	t.Helper()
	if t.Failed() {
		return
	}
	norm := func(list []domain.ChangeImpact) string {
		out := make([]domain.ChangeImpact, len(list))
		for i, cn := range list {
			cn.CreatedAt = cn.CreatedAt.UTC().Truncate(time.Millisecond)
			cn.Reviews = slices.Clone(cn.Reviews)
			for j := range cn.Reviews {
				cn.Reviews[j].At = cn.Reviews[j].At.UTC().Truncate(time.Millisecond)
			}
			if len(cn.Reviews) == 0 {
				cn.Reviews = nil
			}
			if len(cn.DerivedFrom) == 0 {
				cn.DerivedFrom = nil
			}
			if len(cn.Items) == 0 {
				cn.Items = nil
			}
			out[i] = cn
		}
		b, _ := json.MarshalIndent(out, "", " ")
		return string(b)
	}
	err := repo.InTx(context.Background(), func(tx Tx) error {
		cs, err := tx.Changes(context.Background())
		if err != nil {
			return err
		}
		for _, c := range cs {
			stored, err := tx.ChangeImpacts(context.Background(), c.ID)
			if err != nil {
				return err
			}
			events, err := impactEvents(context.Background(), tx, c.ID)
			if err != nil {
				return err
			}
			if got, want := norm(domain.FoldImpacts(events)), norm(stored); got != want {
				t.Errorf("change %s: the replay of its %d events differs from the projection\nreplay: %s\nstored: %s", c.ID, len(events), got, want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
