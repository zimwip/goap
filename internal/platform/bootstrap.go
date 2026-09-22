package platform

import (
	"context"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OptionalPostgres opens and migrates GOAP_DB_DSN when set. It returns nil
// when no DSN is configured (the service then uses in-memory storage).
func OptionalPostgres(ctx context.Context, log *slog.Logger, migrations fs.FS) *pgxpool.Pool {
	dsn := Env("GOAP_DB_DSN", "")
	if dsn == "" {
		log.Warn("GOAP_DB_DSN not set: using in-memory storage")
		return nil
	}
	pool, err := OpenPostgres(ctx, dsn)
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
