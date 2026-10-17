mod common;

use std::sync::Arc;

use common::{as_, casbin, with_alm};
use goap_connect::Ctx;
use goap_def::STATUS_PUBLISHED;
use goap_domainsvc::{Handler, MemoryStore, Service};
use goap_gen::registry::v1 as pb;

/// The client reads what the service serves, keeps it until invalidated, and answers the reads of a methodology
/// registry (ADR 0125 §3).
#[tokio::test]
async fn client() {
    let s = Arc::new(Service {
        authz: Some(casbin()),
        ..Service::new(Arc::new(MemoryStore::new()))
    });
    with_alm(&s).await;
    let app = pb::domain_service_router(
        Arc::new(Handler::new(s.clone())),
        goap_connect::Interceptors::none(),
    );
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(ln, app).await });

    let c = goap_domainclient::Client::connect(&url);
    let ctx = Ctx::background();
    let ds = c.domains(&ctx).await.unwrap();
    assert!(
        ds.len() == 1 && ds[0].name == "alm",
        "domains in force: {ds:?}"
    );
    let all = c.domain_versions(&ctx, true).await.unwrap();
    assert!(
        all.len() == 4 && all[0].builtin,
        "every version, the built-in ones first: {}",
        all.len()
    );
    let alm = c.get_domain(&ctx, "alm", "").await.unwrap();
    assert!(
        alm.status == STATUS_PUBLISHED
            && alm.published_at.is_some()
            && !alm.domain.node_types.is_empty(),
        "latest published: {alm:?}"
    );
    let err = c.get_domain(&ctx, "alm", "0.0.1").await.unwrap_err();
    assert!(err.is_not_found(), "unknown version: {err}");

    let mut next = alm.domain.clone();
    next.version = "99.0.0".into();
    s.save_domain(&as_(&["admin"]), next).await.unwrap();
    s.publish_domain(&as_(&["admin"]), "alm", "99.0.0")
        .await
        .unwrap();
    let r = c.get_domain(&ctx, "alm", "").await.unwrap();
    assert_ne!(
        r.domain.version, "99.0.0",
        "the client keeps what it read until invalidated"
    );
    c.invalidate();
    let r = c.get_domain(&ctx, "alm", "").await.unwrap();
    assert_eq!(r.domain.version, "99.0.0", "read again");
}
