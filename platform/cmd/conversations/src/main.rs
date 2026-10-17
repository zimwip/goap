//! Command conversations keeps the conversations of the users with the assistant, outside the graph (ADR 0085), and
//! hosts the assistant that answers them (ADR 0087): it is the one process that holds both the conversations and,
//! through clients acting for the caller, the graph, the model gateway and the registry.

use std::sync::Arc;

use async_trait::async_trait;
use goap_assistantsvc::{BoxError, EngineClient, Methodologies, PortGraph};
use goap_connect::Interceptors;
use goap_convsvc::{MIGRATIONS, MemoryStore, SqlStore, Store};
use goap_ctx::Ctx;
use goap_gen::assistant::v1::assistant_service_router;
use goap_gen::conversations::v1::conversation_service_router;
use goap_gen::engine::v1::EngineServiceClient;
use goap_gen::registry::v1::RegistryServiceClient;
use goap_identity::Extractor;
use goap_methodology::Compiled;
use goap_platform::{Server, env, fatal, h2c_client, logger, migrate, optional_postgres};

/// Registry is the registry client as the methodologies of the assistant (Go's `*registrysvc.Client`, which
/// satisfies `assistantsvc.Methodologies`).
struct Registry(goap_registrysvc::Client);

#[async_trait]
impl Methodologies for Registry {
    async fn methodology(&self, ctx: &Ctx, name: &str) -> Result<Arc<Compiled>, BoxError> {
        self.0
            .methodology(ctx, name)
            .await
            .map_err(|e| Box::new(e) as BoxError)
    }
}

/// A Connect client of the platform at `url`: h2c and `interceptors`.
fn platform_client(url: &str, interceptors: &Interceptors) -> goap_connect::Client {
    goap_connect::Client::builder(url)
        .http(h2c_client())
        .interceptors(interceptors)
        .build()
}

#[tokio::main]
async fn main() {
    let ctx = Ctx::background();
    logger("conversations");
    let tel = goap_telemetry::setup(&ctx, "conversations");
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
    let convs = Arc::new(goap_convsvc::Service::new(store));
    srv.mount(conversation_service_router(
        Arc::new(goap_convsvc::Handler::new(convs.clone())),
        goap_telemetry::handler_options(),
    ));

    // the assistant calls the platform for the caller: every client carries the principal of its context
    let copts = goap_telemetry::client_options().with(goap_identity::forward());
    let graph_client = Arc::new(goap_graphclient::Client::from_connect(platform_client(
        &env("GOAP_GRAPH_URL", "http://localhost:8081"),
        &goap_telemetry::client_options(),
    )));
    let registry_url = env("GOAP_REGISTRY_URL", "http://localhost:8082");
    let mut assistant = goap_assistantsvc::Service::new(
        convs,
        Arc::new(goap_modelgw::Client::new(platform_client(
            &env("GOAP_MODELGW_URL", "http://localhost:8084"),
            &copts,
        ))),
        Arc::new(PortGraph(graph_client.clone())),
        Arc::new(Registry(goap_registrysvc::Client::new(platform_client(
            &registry_url,
            &copts,
        )))),
        Arc::new(goap_assistantsvc::Directory(Arc::new(
            goap_access::Directory::new(graph_client),
        ))),
    );
    assistant.engine = Some(Arc::new(EngineClient {
        api: Arc::new(EngineServiceClient::new(platform_client(
            &env("GOAP_ENGINE_URL", "http://localhost:8083"),
            &copts,
        ))),
        views: Some(Arc::new(RegistryServiceClient::new(platform_client(
            &registry_url,
            &copts,
        )))),
    }));
    srv.mount(assistant_service_router(
        Arc::new(goap_assistantsvc::Handler {
            service: Arc::new(assistant),
            identity: Extractor::default(),
        }),
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
