//! Command credentials keeps the local sign-in password of subjects that connect without an external identity
//! provider (ADR 0040).
use std::sync::Arc;

use goap_credsvc::{Handler, MIGRATIONS, MemoryStore, Service, SqlStore, Store};
use goap_ctx::Ctx;
use goap_gen::credentials::v1::credentials_service_router;
use goap_platform::{Secrets, Server, env, fatal, logger, migrate, optional_postgres};

#[tokio::main]
async fn main() {
    let ctx = Ctx::background();
    logger("credentials");
    let tel = goap_telemetry::setup(&ctx, "credentials");
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
    // the gateway is the only caller: it presents a shared service credential; without one nobody may call
    let token = match Secrets::new()
        .get(
            &ctx,
            "goap/credentials#service_token",
            "GOAP_CREDENTIALS_TOKEN",
        )
        .await
    {
        Ok(t) if !t.is_empty() => t,
        // errors.Join(err, errors.New("not set"))
        Ok(_) => fatal(
            "credentials service token (GOAP_CREDENTIALS_TOKEN)",
            "not set",
        ),
        Err(e) => fatal(
            "credentials service token (GOAP_CREDENTIALS_TOKEN)",
            format!("{e}\nnot set"),
        ),
    };
    srv.mount(credentials_service_router(
        Arc::new(Handler::new(Arc::new(Service::new(store)), &token)),
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
