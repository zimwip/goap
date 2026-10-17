//! Command preferences keeps the personal preferences of the users, outside the graph (ADR 0038).
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_gen::preferences::v1::preferences_service_router;
use goap_platform::{Server, env, fatal, logger, migrate, optional_postgres};
use goap_prefssvc::{Handler, MIGRATIONS, MemoryStore, Service, SqlStore, Store};

#[tokio::main]
async fn main() {
    let ctx = Ctx::background();
    logger("preferences");
    let tel = goap_telemetry::setup(&ctx, "preferences");
    let mut srv = Server::new(&env("GOAP_HTTP_ADDR", ":8080"));
    let mut store: Arc<dyn Store> = Arc::new(MemoryStore::new());
    let pool = optional_postgres(&ctx, None).await;
    if let Some(pool) = &pool {
        if let Err(e) = migrate(&ctx, pool, &MIGRATIONS, ".").await {
            fatal("migrate", e);
        }
        store = Arc::new(SqlStore::Postgres(pool.clone()));
        let p = pool.clone();
        srv.readiness(move |_ctx| {
            let p = p.clone();
            async move { sqlx::query("SELECT 1").execute(&p).await.map(|_| ()) }
        });
    }
    srv.mount(preferences_service_router(
        Arc::new(Handler::new(Arc::new(Service::new(store)))),
        goap_telemetry::handler_options(),
    ));
    let res = srv.run().await;
    if let Some(pool) = pool {
        pool.close().await;
    }
    let _ = tokio::task::spawn_blocking(move || tel.shutdown()).await;
    if let Err(e) = res {
        fatal("server", e);
    }
}
