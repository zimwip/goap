package graph

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/internal/platform"
)

// forEachRepo runs f against the in-memory repository and, when
// GOAP_TEST_PG_DSN is set, against PostgreSQL in a fresh schema.
func forEachRepo(t *testing.T, f func(t *testing.T, repo Repo)) {
	t.Run("memory", func(t *testing.T) { f(t, NewMemory()) })
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
		f(t, NewSQLite(db))
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
		f(t, NewPostgres(pool))
	})
}
