// Package pgtest provides throw-away PostgreSQL schemas for tests
// (enabled by GOAP_TEST_PG_DSN).
package pgtest

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/internal/platform"
)

// Pool returns a pool on a fresh migrated schema, or skips the test when
// GOAP_TEST_PG_DSN is not set. The schema is dropped at the end of the test.
func Pool(t *testing.T, migrations fs.FS) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("GOAP_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("GOAP_TEST_PG_DSN not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if err := platform.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), pool, migrations, "migrations"); err != nil {
		t.Fatal(err)
	}
	return pool
}
