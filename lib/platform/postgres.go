package platform

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultPostgresConns is the size of a pool of PostgreSQL connections a DSN gives none.
const DefaultPostgresConns = 8

// OpenPostgres connects to PostgreSQL. The DSN should set search_path to the
// service schema (dev: one database, one schema per service).
func OpenPostgres(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.Tracer = otelpgx.NewTracer()
	if !strings.Contains(dsn, "pool_max_conns") {
		// more connections than this gave nothing measured (the writers of main wait on its lock, not on the pool);
		// pool_max_conns in the DSN sets another size
		cfg.MaxConns = DefaultPostgresConns
	}
	var pool *pgxpool.Pool
	for attempt := 1; ; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				return pool, nil
			}
			pool.Close()
		}
		if attempt >= 20 {
			return nil, fmt.Errorf("connect postgres: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// PostgresReader opens a pool of read-only connections next to pool, on the same database and settings: every
// statement runs in a read-only transaction of its own (default_transaction_read_only), so a store serves the reads of
// a read-only context (ReadOnly) there with no BEGIN and COMMIT around them, and a write tried there fails (SQLSTATE
// 25006, IsPostgresReadOnly) rather than landing outside a transaction.
func PostgresReader(ctx context.Context, pool *pgxpool.Pool) (*pgxpool.Pool, error) {
	cfg := pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	read, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := read.Ping(ctx); err != nil {
		read.Close()
		return nil, fmt.Errorf("postgres readers: %w", err)
	}
	return read, nil
}

// IsPostgresReadOnly tells the error of a write on a read-only connection (SQLSTATE 25006, read_only_sql_transaction).
func IsPostgresReadOnly(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "25006"
}

// Migrate applies the *.sql files of dir (lexicographic order) that were not
// applied yet, under an advisory lock so that replicas can start together.
func Migrate(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, migrations fs.FS, dir string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext(current_schema()))`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext(current_schema()))`) //nolint:errcheck
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, dir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, e.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := fs.ReadFile(migrations, dir+"/"+e.Name())
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES ($1)`, e.Name())
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		log.Info("migration applied", "name", e.Name())
	}
	return nil
}
