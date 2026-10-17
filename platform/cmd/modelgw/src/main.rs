//! Command modelgw is the LLM model gateway.

use std::sync::Arc;
use std::time::Duration;

use goap_access::{Authorizer, Directory};
use goap_ctx::Ctx;
use goap_gen::model::v1::model_service_router;
use goap_modelgw::{
    AnthropicProxy, BoxError, DEFAULT_CALL_RETENTION_DAYS, DEFAULT_COST_TTL, Handler, Instrument,
    InstrumentEmbed, MemoryStore, SecretsFn, Service, SqlStore, Store, VaultFn,
};
use goap_platform::{
    Secrets, Server, env, env_bool, env_int, fatal, h2c_client, logger, migrate, optional_postgres,
};

const DAY: Duration = Duration::from_secs(24 * 60 * 60);

/// A graph client at `url`, its calls traced when `traced` (Go's `graphclient.NewClient(hc, url, opts...)`).
fn graph(url: &str, traced: bool) -> Arc<goap_graphclient::Client> {
    let mut b = goap_graphclient::Client::builder(url).http(h2c_client());
    if traced {
        b = b.interceptors(&goap_telemetry::client_options());
    }
    Arc::new(goap_graphclient::Client::from_connect(b.build()))
}

/// The provider API keys are references resolved by the secrets of the platform.
fn resolve(secrets: Arc<Secrets>) -> SecretsFn {
    Arc::new(move |ctx: Ctx, reference: String| {
        let secrets = secrets.clone();
        Box::pin(async move {
            secrets
                .resolve(&ctx, &reference)
                .await
                .map_err(|e| Box::new(e) as BoxError)
        })
    })
}

/// A raw API key written to the backing store of the secrets.
fn vault(secrets: Arc<Secrets>) -> VaultFn {
    Arc::new(
        move |ctx: Ctx, path: String, field: String, value: String| {
            let secrets = secrets.clone();
            Box::pin(async move {
                secrets
                    .put(&ctx, &path, &field, &value)
                    .await
                    .map_err(|e| Box::new(e) as BoxError)
            })
        },
    )
}

/// The OpenTelemetry GenAI instrumentation of the provider calls.
fn instrumentation() -> (Instrument, InstrumentEmbed) {
    let genai = Arc::new(goap_telemetry::GenAI::new());
    let g = genai.clone();
    let complete: Instrument = Arc::new(move |ctx, provider, model, req, call| {
        let g = g.clone();
        Box::pin(async move { g.instrument(&ctx, &provider, &model, &req, call).await })
    });
    let embed: InstrumentEmbed = Arc::new(move |ctx, provider, model, texts, call| {
        let g = genai.clone();
        Box::pin(async move {
            g.instrument_embed(&ctx, &provider, &model, texts, call)
                .await
        })
    });
    (complete, embed)
}

/// The days of GOAP_LLM_BEHAVIOR_COST_TTL_DAYS as a duration (default DEFAULT_COST_TTL).
fn cost_ttl(days: i64) -> Duration {
    DAY * u32::try_from(days.max(0)).unwrap_or(u32::MAX)
}

#[tokio::main]
async fn main() {
    let ctx = Ctx::background();
    logger("modelgw");
    let tel = goap_telemetry::setup(&ctx, "modelgw");
    let secrets = Arc::new(Secrets::new());
    let mut srv = Server::new(&env("GOAP_HTTP_ADDR", ":8080"));
    let mut store: Arc<dyn Store> = Arc::new(MemoryStore::new());
    let pool = optional_postgres(&ctx, None).await;
    if let Some(pool) = &pool {
        if let Err(e) = migrate(&ctx, pool, &goap_modelgw::MIGRATIONS, ".").await {
            fatal("migrate", e);
        }
        store = Arc::new(SqlStore::Postgres(pool.clone()));
        let p = pool.clone();
        srv.readiness(move |_ctx| {
            let p = p.clone();
            async move { sqlx::query("SELECT 1").execute(&p).await.map(|_| ()) }
        });
    }
    let graph_url = env("GOAP_GRAPH_URL", "http://localhost:8081");
    // providers, models and aliases are nodes of the graph; API keys are references resolved here
    let mut svc = Service::new(
        Arc::new(goap_llmcfg::Directory::new(graph(&graph_url, true))),
        store,
        Some(resolve(secrets.clone())),
    );
    svc.vault = Some(vault(secrets));
    let (complete, embed) = instrumentation();
    svc.router.instrument = Some(complete);
    svc.router.instrument_embed = Some(embed);

    let iam = match Authorizer::new(Arc::new(Directory::new(graph(&graph_url, false)))) {
        Ok(a) => Arc::new(a),
        Err(e) => fatal("authorizer", e),
    };
    // a measured behaviour cost is measured again after this (ADR 0093)
    svc.cost_ttl = cost_ttl(env_int(
        "GOAP_LLM_BEHAVIOR_COST_TTL_DAYS",
        (DEFAULT_COST_TTL.as_secs() / DAY.as_secs()) as i64,
    ));
    // the exchange of the calls no change log keeps (ADR 0089)
    svc.call_prompts = env_bool("GOAP_LLM_CALL_PROMPTS", true);
    // the roles a model requires are held on the caller's project (ADR 0043)
    svc.authz = Some(iam.clone());
    let svc = svc.shared();

    if let Err(e) = svc.reload(&ctx).await {
        tracing::warn!(err = %e, "model configuration not read yet");
    }
    let (names, targets, providers) = svc.router.aliases();
    for n in &names {
        let t = &targets[n];
        tracing::info!(alias = %n, provider = %t.provider, model = %t.model, "model alias");
    }
    tracing::info!(providers = ?providers, "providers");

    // the ledger of calls (ADR 0089)
    {
        let (svc, ctx) = (svc.clone(), ctx.clone());
        let days = env_int("GOAP_LLM_CALL_RETENTION_DAYS", DEFAULT_CALL_RETENTION_DAYS);
        tokio::spawn(async move { svc.keep_calls(&ctx, days).await });
    }
    let mut handler = Handler::new(svc.clone());
    handler.authz = Some(iam);
    srv.mount(model_service_router(
        Arc::new(handler),
        goap_telemetry::handler_options(),
    ));
    // the Anthropic endpoint where the Claude coding agent's sessions call the model, each call a row of the ledger
    // (ADR 0123 §6); its tokens are minted by the MCP hub with the same secret
    let secret = env("GOAP_MODEL_PROXY_SECRET", "");
    if !secret.is_empty() {
        srv.mount(
            Arc::new(AnthropicProxy {
                service: svc,
                secret: secret.into_bytes(),
                http: None,
            })
            .router(),
        );
    } else {
        tracing::info!(
            "GOAP_MODEL_PROXY_SECRET not set: no Anthropic endpoint, a connector calling a model by itself stays outside the ledger"
        );
    }
    let res = srv.run().await;
    if let Some(pool) = pool {
        pool.close().await;
    }
    let _ = tokio::task::spawn_blocking(move || tel.shutdown()).await;
    if let Err(e) = res {
        fatal("server", e);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_cost_ttl_is_given_in_days() {
        assert_eq!(cost_ttl(30), Duration::from_secs(30 * 86400));
        assert_eq!(cost_ttl(0), Duration::ZERO);
        assert_eq!(cost_ttl(-3), Duration::ZERO);
        assert_eq!(
            cost_ttl((DEFAULT_COST_TTL.as_secs() / DAY.as_secs()) as i64),
            DEFAULT_COST_TTL
        );
    }
}
