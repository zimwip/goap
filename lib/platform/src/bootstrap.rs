//! The optional dependencies of a service.
use goap_ctx::Ctx;
use include_dir::Dir;
use sqlx::PgPool;

use crate::{Events, env, fatal, migrate, open_postgres};

/// Opens and migrates `GOAP_DB_DSN` when set. None when no DSN is
/// configured (the service then uses in-memory storage); `migrations` holds the `*.sql` files at its root, as every
/// crate's embedded `MIGRATIONS` does. A failure is fatal.
pub async fn optional_postgres(ctx: &Ctx, migrations: Option<&Dir<'_>>) -> Option<PgPool> {
    let dsn = env("GOAP_DB_DSN", "");
    if dsn.is_empty() {
        tracing::warn!("GOAP_DB_DSN not set: using in-memory storage");
        return None;
    }
    let pool = match open_postgres(ctx, &dsn).await {
        Ok(p) => p,
        Err(e) => fatal("postgres", e),
    };
    if let Some(m) = migrations
        && let Err(e) = migrate(ctx, &pool, m, ".").await
    {
        fatal("migrate", e);
    }
    Some(pool)
}

/// Connects to `GOAP_NATS_URL` when set (None: events disabled). A failure is fatal.
pub async fn optional_events(ctx: &Ctx) -> Option<Events> {
    let url = env("GOAP_NATS_URL", "");
    if url.is_empty() {
        tracing::warn!("GOAP_NATS_URL not set: events disabled");
        return None;
    }
    match Events::connect(ctx, &url).await {
        Ok(e) => Some(e),
        Err(e) => fatal("nats", e),
    }
}
