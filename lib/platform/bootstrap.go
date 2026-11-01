package platform

import (
	"context"
	"database/sql"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// OptionalPostgres opens and migrates GOAP_DB_DSN when set. It returns nil
// when no DSN is configured (the service then uses in-memory storage).
func OptionalPostgres(ctx context.Context, log *slog.Logger, migrations fs.FS) *pgxpool.Pool {
	return OptionalPostgresWith(ctx, log, migrations, nil)
}

// OptionalPostgresWith is OptionalPostgres with session settings of the service's own (OpenPostgresWith).
func OptionalPostgresWith(ctx context.Context, log *slog.Logger, migrations fs.FS, settings map[string]string) *pgxpool.Pool {
	dsn := Env("GOAP_DB_DSN", "")
	if dsn == "" {
		log.Warn("GOAP_DB_DSN not set: using in-memory storage")
		return nil
	}
	pool, err := OpenPostgresWith(ctx, dsn, settings)
	if err != nil {
		Fatal(log, "postgres", err)
	}
	if migrations != nil {
		if err := Migrate(ctx, log, pool, migrations, "migrations"); err != nil {
			Fatal(log, "migrate", err)
		}
	}
	return pool
}

// OptionalEvents connects to GOAP_NATS_URL when set.
func OptionalEvents(ctx context.Context, log *slog.Logger) *Events {
	url := Env("GOAP_NATS_URL", "")
	if url == "" {
		log.Warn("GOAP_NATS_URL not set: events disabled")
		return nil
	}
	ev, err := ConnectEvents(ctx, log, url)
	if err != nil {
		Fatal(log, "nats", err)
	}
	return ev
}

// SQL is the database of a service: PostgreSQL (GOAP_DB_DSN) or a SQLite file (GOAP_SQLITE_PATH, the local stack of
// goap-local), its schema migrated.
type SQL struct {
	DB *sql.DB
	// Dollar tells PostgreSQL's placeholders ($1), SQLite's being ?: the Dollar field of the SQL stores.
	Dollar bool
	// Pool is the PostgreSQL pool, nil on SQLite.
	Pool *pgxpool.Pool
	path string
}

// SQLite tells a SQLite database.
func (s *SQL) SQLite() bool { return s.Pool == nil }

// Ping checks the database is reachable (a readiness check).
func (s *SQL) Ping(ctx context.Context) error { return s.DB.PingContext(ctx) }

// Reader opens a pool of read-only connections on a SQLite database (OpenSQLiteReader), nil on PostgreSQL.
func (s *SQL) Reader(ctx context.Context, conns int) (*sql.DB, error) {
	if !s.SQLite() {
		return nil, nil
	}
	return OpenSQLiteReader(ctx, s.path, conns)
}

// Close closes the database.
func (s *SQL) Close() {
	s.DB.Close()
	if s.Pool != nil {
		s.Pool.Close()
	}
}

// OptionalSQL opens and migrates the database of a service: GOAP_DB_DSN (PostgreSQL, migrations/ of pg), else
// GOAP_SQLITE_PATH (SQLite, migrations_sqlite/ of lite, recorded under component). It returns nil when neither is set
// (the service then keeps its data in memory).
func OptionalSQL(ctx context.Context, log *slog.Logger, component string, pg, lite fs.FS) *SQL {
	if Env("GOAP_DB_DSN", "") != "" {
		pool := OptionalPostgres(ctx, log, pg)
		return &SQL{DB: stdlib.OpenDBFromPool(pool), Dollar: true, Pool: pool}
	}
	path := Env("GOAP_SQLITE_PATH", "")
	if path == "" {
		log.Warn("GOAP_DB_DSN and GOAP_SQLITE_PATH not set: using in-memory storage")
		return nil
	}
	db, err := OpenSQLite(ctx, path)
	if err != nil {
		Fatal(log, "sqlite", err)
	}
	if err := MigrateSQLite(ctx, db, component, lite, "migrations_sqlite"); err != nil {
		Fatal(log, "migrate", err)
	}
	log.Info("storage", "sqlite", path)
	return &SQL{DB: db, path: path}
}
