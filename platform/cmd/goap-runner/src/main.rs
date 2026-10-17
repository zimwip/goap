//! Command goap-runner runs inside a sandbox (container, pod or isolated process). It executes script actions with
//! the DSL and reaches the platform only through the engine RuntimeService, with a per-job token.
use std::sync::Arc;
use std::time::Duration;

use goap_ctx::Ctx;
use goap_gen::runtime::v1::sandbox_service_router;
use goap_platform::{Server, env, fatal, logger};
use goap_sandbox::Runner;

#[tokio::main]
async fn main() {
    let ctx = Ctx::background();
    logger("goap-runner");
    let tel = goap_telemetry::setup(&ctx, "goap-runner");
    let mut srv = Server::new(&env("GOAP_HTTP_ADDR", ":8080"));
    // HTTP/1.1 client: the runtime endpoint may sit behind any proxy
    let hc = reqwest::Client::builder()
        .timeout(Duration::from_secs(600))
        .build()
        .unwrap_or_default();
    srv.mount(sandbox_service_router(
        Arc::new(Runner {
            http: Some(hc),
            options: goap_telemetry::client_options(),
        }),
        goap_telemetry::handler_options(),
    ));
    let res = srv.run().await;
    let _ = tokio::task::spawn_blocking(move || tel.shutdown()).await;
    if let Err(e) = res {
        fatal("server", e);
    }
}
