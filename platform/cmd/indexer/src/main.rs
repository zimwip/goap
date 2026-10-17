//! Command indexer is the document index service (ADR 0026, 0095): it follows the node, baseline and change events of
//! the graph on NATS, embeds through the model gateway and answers hybrid searches.

use std::sync::Arc;

use goap_access::{Authorizer, Directory};
use goap_authz::Principal;
use goap_ctx::Ctx;
use goap_gen::graph::v1 as graphv1;
use goap_gen::graph::v1::{GraphService, GraphServiceClient};
use goap_gen::index::v1::index_service_router;
use goap_index as index;
use goap_indexersvc::{BoxError, Handler, SUBJECTS, Service, republish_fn};
use goap_platform::{
    Server, env, fatal, h2c_client, logger, migrate, optional_events, optional_postgres,
};
use http::{HeaderMap, HeaderName, HeaderValue};

/// A Connect client of the platform at `url`: h2c and the telemetry.
fn platform_client(url: &str) -> goap_connect::Client {
    goap_connect::Client::builder(url)
        .http(h2c_client())
        .interceptors(&goap_telemetry::client_options())
        .build()
}

fn set(h: &mut HeaderMap, name: &str, value: &str) {
    if let (Ok(n), Ok(v)) = (HeaderName::try_from(name), HeaderValue::from_str(value)) {
        h.insert(n, v);
    }
}

/// The identity headers of a principal, as the indexer forwards it to the graph (subject, org, roles).
fn identity_headers(p: &Principal) -> HeaderMap {
    let mut h = HeaderMap::new();
    set(&mut h, goap_identity::HEADER_SUBJECT, &p.subject);
    set(&mut h, goap_identity::HEADER_ORG, &p.org);
    set(&mut h, goap_identity::HEADER_ROLES, &p.roles.join(","));
    h
}

#[tokio::main]
async fn main() {
    let ctx = Ctx::background();
    logger("indexer");
    let tel = goap_telemetry::setup(&ctx, "indexer");
    let mut srv = Server::new(&env("GOAP_HTTP_ADDR", ":8080"));
    let pool = optional_postgres(&ctx, None).await;
    if let Some(pool) = &pool {
        if let Err(e) = migrate(&ctx, pool, &index::MIGRATIONS, ".").await {
            fatal("migrate", e);
        }
        let p = pool.clone();
        srv.readiness(move |_ctx| {
            let p = p.clone();
            async move { sqlx::query("SELECT 1").execute(&p).await.map(|_| ()) }
        });
    }
    let events = optional_events(&ctx).await;
    if let Some(bus) = &events {
        let bus = bus.clone();
        srv.readiness(move |ctx| {
            let bus = bus.clone();
            async move { bus.ready(&ctx).await }
        });
    }

    let graph_url = env("GOAP_GRAPH_URL", "http://localhost:8081");
    let directory = Arc::new(Directory::new(Arc::new(
        goap_graphclient::Client::from_connect(platform_client(&graph_url)),
    )));
    let iam = match Authorizer::new(directory.clone()) {
        Ok(a) => Arc::new(a),
        Err(e) => fatal("authorizer", e),
    };
    // embeddings: the model gateway, alias "embed"; without one the index is text only
    let embedder: Arc<dyn goap_llm::Embedder> = Arc::new(goap_modelgw::Client::new(
        platform_client(&env("GOAP_MODELGW_URL", "http://localhost:8084")),
    ));
    let authz: Arc<dyn goap_authz::Authorizer> = iam.clone();
    let svc = match &pool {
        Some(pool) => Service::new(
            Arc::new(index::Postgres::new(pool.clone())),
            Some(embedder),
            Some(authz),
        ),
        None => Service::new(Arc::new(index::Memory::new()), Some(embedder), Some(authz)),
    };
    let svc = Arc::new(svc);
    // the projects a caller may see, and a project with its sub-projects (ADR 0095)
    svc.set_access(Some(directory));
    let graph_rpc = Arc::new(GraphServiceClient::new(platform_client(&graph_url)));
    svc.set_republish(Some(republish_fn(move |ctx: Ctx| {
        let graph_rpc = graph_rpc.clone();
        async move {
            let p = goap_authz::principal_from(&ctx);
            let req = goap_connect::Request::with_headers(
                graphv1::RepublishIndexRequest {},
                identity_headers(&p),
            );
            let r = graph_rpc
                .republish_index(&ctx, req)
                .await
                .map_err(|e| Box::new(e) as BoxError)?;
            Ok(r.msg.versions as usize)
        }
    })));
    if let Some(bus) = &events {
        let (bus, svc, ctx) = (bus.clone(), svc.clone(), ctx.clone());
        tokio::spawn(async move {
            let handle = |ctx: Ctx, subject: String, data: bytes::Bytes| {
                let svc = svc.clone();
                async move { svc.handle(&ctx, &subject, &data).await }
            };
            if let Err(e) = bus.consume_durable(&ctx, "indexer", SUBJECTS, handle).await {
                fatal("index consumer", e);
            }
        });
    } else {
        tracing::warn!("no event bus: the index only changes through Reindex");
    }
    let mut handler = Handler::new(svc);
    handler.authz = Some(iam);
    srv.mount(index_service_router(
        Arc::new(handler),
        goap_telemetry::handler_options(),
    ));
    let res = srv.run().await;
    if let Some(bus) = events {
        bus.close().await;
    }
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
    fn the_identity_is_forwarded_to_the_graph() {
        let p = Principal {
            subject: "alice".into(),
            org: "ORG-DEFAULT".into(),
            roles: vec!["admin".into(), "reader".into()],
            ..Default::default()
        };
        let h = identity_headers(&p);
        assert_eq!(h[goap_identity::HEADER_SUBJECT], "alice");
        assert_eq!(h[goap_identity::HEADER_ORG], "ORG-DEFAULT");
        assert_eq!(h[goap_identity::HEADER_ROLES], "admin,reader");
    }
}
