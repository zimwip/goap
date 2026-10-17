//! Command mcp is the MCP hub: the registry of connectors (separate services that register themselves), the generic
//! MCP definitions, their adapters and the bindings of the organizations, and the invocation of tools (ADR 0019). It
//! serves the built-in connectors of the platform in-process (ADR 0028).

use std::sync::{Arc, OnceLock};

use async_trait::async_trait;
use goap_access::{Authorizer, Directory};
use goap_adapter as adapter;
use goap_connector_builtin::{Hub, Ports, connectors, forward_identity};
use goap_ctx::Ctx;
use goap_gen::engine::v1::EngineServiceClient;
use goap_gen::mcp::v1::mcp_service_router;
use goap_gen::registry::v1::RegistryServiceClient;
use goap_mcpsvc as mcpsvc;
use goap_platform::{
    Secrets, Server, env, env_duration, fatal, h2c_client, logger, migrate, optional_postgres,
};

/// LateHub is the hub as the built-in connectors see it: the service, set once it is built (the service holds the
/// connectors in its invoker, the connectors read the service: Go shares the map, filled after the service).
#[derive(Default)]
struct LateHub(OnceLock<Arc<mcpsvc::Service>>);

impl LateHub {
    fn service(&self) -> Result<&Arc<mcpsvc::Service>, mcpsvc::Error> {
        self.0
            .get()
            .ok_or_else(|| mcpsvc::Error::Unavailable("the hub is starting".into()))
    }
}

#[async_trait]
impl Hub for LateHub {
    async fn effective(
        &self,
        ctx: &Ctx,
        unit: &str,
    ) -> Result<(Vec<String>, Vec<mcpsvc::Effective>), mcpsvc::Error> {
        self.service()?.effective(ctx, unit).await
    }
    async fn connectors(&self, ctx: &Ctx) -> Result<Vec<mcpsvc::ConnectorView>, mcpsvc::Error> {
        self.service()?.connectors(ctx).await
    }
    async fn connector_of(&self, ctx: &Ctx, a: &adapter::Instance) -> String {
        match self.service() {
            Ok(s) => s.connector_of(ctx, a).await,
            Err(_) => String::new(),
        }
    }
}

/// A Connect client of the platform at `url`: h2c, `interceptors` (the telemetry and the identity of the caller).
fn platform_client(url: &str, interceptors: &goap_connect::Interceptors) -> goap_connect::Client {
    goap_connect::Client::builder(url)
        .http(h2c_client())
        .interceptors(interceptors)
        .build()
}

/// A graph client with no interceptor (Go's `graphclient.NewClient(hc, graphURL)`).
fn bare_graph(url: &str) -> Arc<goap_graphclient::Client> {
    Arc::new(goap_graphclient::Client::from_connect(
        goap_graphclient::Client::builder(url)
            .http(h2c_client())
            .build(),
    ))
}

#[tokio::main]
async fn main() {
    let ctx = Ctx::background();
    logger("mcp");
    let tel = goap_telemetry::setup(&ctx, "mcp");
    let mut srv = Server::new(&env("GOAP_HTTP_ADDR", ":8080"));
    let mut store: Arc<dyn mcpsvc::Store> = Arc::new(mcpsvc::MemoryStore::new());
    let pool = optional_postgres(&ctx, None).await;
    if let Some(pool) = &pool {
        if let Err(e) = migrate(&ctx, pool, &mcpsvc::MIGRATIONS, ".").await {
            fatal("migrate", e);
        }
        store = Arc::new(mcpsvc::SqlStore::Postgres(pool.clone()));
        let p = pool.clone();
        srv.readiness(move |_ctx| {
            let p = p.clone();
            async move { sqlx::query("SELECT 1").execute(&p).await.map(|_| ()) }
        });
    }
    let token = std::env::var("GOAP_CONNECTOR_TOKEN").unwrap_or_default();
    if token.is_empty() {
        tracing::warn!(
            "GOAP_CONNECTOR_TOKEN not set: no connector can register, only the built-in ones are served"
        );
    }
    let graph_url = env("GOAP_GRAPH_URL", "http://localhost:8081");

    let iam = match Authorizer::new(Arc::new(Directory::new(bare_graph(&graph_url)))) {
        Ok(a) => Arc::new(a),
        Err(e) => fatal("authorizer", e),
    };
    // the built-in connectors (ADR 0028) run in the hub; the others register over HTTP. They call the platform's
    // services for the caller of the tool; the domains are the graph's, the methodologies the methodology
    // registry's (ADR 0125 §3), both read through the contract
    let copts = goap_telemetry::client_options().with(forward_identity());
    let hub = Arc::new(LateHub::default());
    let builtins = connectors(Ports {
        graph: Some(Arc::new(goap_graphclient::Client::from_connect(
            platform_client(&graph_url, &copts),
        ))),
        engine: Some(Arc::new(EngineServiceClient::new(platform_client(
            &env("GOAP_ENGINE_URL", "http://localhost:8083"),
            &copts,
        )))),
        domains: Some(Arc::new(goap_domainclient::Client::new(platform_client(
            &graph_url, &copts,
        )))),
        methodologies: Some(Arc::new(RegistryServiceClient::new(platform_client(
            &env("GOAP_REGISTRY_URL", "http://localhost:8082"),
            &copts,
        )))),
        hub: Some(hub.clone()),
        floor: Some(Arc::new(iam.floor())),
        authz: Some(iam.clone()),
    });

    let mut svc = mcpsvc::Service::new(store);
    // the MCPs, the adapter definitions, the adapters and the organisation hierarchy are nodes of the graph
    svc.directory = Some(Arc::new(mcpsvc::Directory::new(bare_graph(&graph_url))));
    svc.invoker = Some(Arc::new(mcpsvc::InprocInvoker {
        connectors: builtins.clone(),
        remote: Some(Arc::new(mcpsvc::ConnectInvoker::new(token.clone()))),
    }));
    svc.secrets = Some(mcpsvc::resolve_secret(Arc::new(Secrets::new())));
    svc.lease = env_duration("GOAP_CONNECTOR_LEASE", mcpsvc::DEFAULT_LEASE);
    // a connector calling a model by itself (the Claude coding agent) gets the gateway's Anthropic endpoint and a
    // token of each call, so its calls are rows of the ledger (ADR 0123 §6)
    let (secret, u) = (
        env("GOAP_MODEL_PROXY_SECRET", ""),
        env("GOAP_MODEL_PROXY_URL", ""),
    );
    if !secret.is_empty() && !u.is_empty() {
        svc.models = Some(mcpsvc::ModelEndpoint {
            url: u,
            secret: secret.into_bytes(),
            ..Default::default()
        });
    }
    let svc = Arc::new(svc);
    let _ = hub.0.set(svc.clone());

    let (run_ctx, stop) = ctx.with_cancel();
    svc.keep_registered(&run_ctx, builtins).await;
    let mut handler = mcpsvc::Handler::new(svc);
    handler.authz = Some(iam);
    handler.connector_token = token;
    srv.mount(mcp_service_router(
        Arc::new(handler),
        goap_telemetry::handler_options(),
    ));
    let res = srv.run().await;
    stop.cancel();
    if let Some(pool) = pool {
        pool.close().await;
    }
    let _ = tokio::task::spawn_blocking(move || tel.shutdown()).await;
    if let Err(e) = res {
        fatal("server", e);
    }
}
