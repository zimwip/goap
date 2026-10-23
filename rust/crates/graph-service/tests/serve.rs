use std::sync::Arc;

use graph_service::{State, app};
use graph_store::{Store, StoreConfig};

async fn serve() -> String {
    let store = Store::open(&StoreConfig::Memory).await.unwrap();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(axum::serve(listener, app(Arc::new(State { store }))).into_future());
    format!("http://{addr}")
}

#[tokio::test]
async fn health_and_a_method_not_written_yet() {
    let base = serve().await;
    let c = reqwest::Client::new();
    assert_eq!(c.get(format!("{base}/healthz")).send().await.unwrap().status(), 200);
    assert_eq!(c.get(format!("{base}/readyz")).send().await.unwrap().status(), 200);
    for path in [
        "/goap.graph.v1.GraphService/CreateChange",
        "/goap.registry.v1.DomainService/ListDomains",
        "/goap.smarttag.v1.SmartTagService/ListSmartTags",
        "/goap.importer.v1.ImportService/ListImporters",
    ] {
        let res = c
            .post(format!("{base}{path}"))
            .header("content-type", "application/json")
            .body("{}")
            .send()
            .await
            .unwrap();
        assert_eq!(res.status(), 501, "{path}");
        let body: serde_json::Value = res.json().await.unwrap();
        assert_eq!(body["code"], "unimplemented", "{path}");
    }
}

async fn call(base: &str, method: &str, subject: Option<&str>, body: serde_json::Value) -> (u16, serde_json::Value) {
    let mut req = reqwest::Client::new()
        .post(format!("{base}/goap.graph.v1.GraphService/{method}"))
        .header("content-type", "application/json")
        .body(body.to_string());
    if let Some(s) = subject {
        req = req.header("x-goap-subject", s);
    }
    let res = req.send().await.unwrap();
    (res.status().as_u16(), res.json().await.unwrap())
}

#[tokio::test]
async fn reads_a_service_serves_and_the_ones_it_leaves_to_go() {
    let base = serve().await;
    let svc = Some("system:test");
    // an anonymous call is refused, as the Go service's Identify does
    let (status, body) = call(&base, "GetNode", None, serde_json::json!({})).await;
    assert_eq!((status, body["code"].as_str()), (401, Some("unauthenticated")));
    // a read naming no view
    let (status, body) = call(&base, "GetNode", svc, serde_json::json!({"key": "R-1"})).await;
    assert_eq!(
        (status, body["code"].as_str()),
        (400, Some("invalid_argument")),
        "{body}"
    );
    assert!(
        body["message"].as_str().unwrap().starts_with("no view: name a branch"),
        "{body}"
    );
    // a node main does not hold
    let view = serde_json::json!({"namespace": "alm", "branch": "main"});
    let (status, body) = call(&base, "GetNode", svc, serde_json::json!({"key": "R-1", "view": view})).await;
    assert_eq!((status, body["code"].as_str()), (404, Some("not_found")), "{body}");
    // a person reads through its reach: the Go service answers it for now
    let (status, body) = call(
        &base,
        "GetNode",
        Some("ann"),
        serde_json::json!({"key": "R-1", "view": view}),
    )
    .await;
    assert_eq!((status, body["code"].as_str()), (501, Some("unimplemented")), "{body}");
    // the head of main before any change: the empty state
    let (status, body) = call(
        &base,
        "GetBranch",
        svc,
        serde_json::json!({"namespace": "alm", "name": "main", "headerOnly": true}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(body["branch"]["name"], "main");
    assert!(body["head"]["id"].is_null(), "{body}");
    // no change yet
    let (status, body) = call(
        &base,
        "ListChanges",
        svc,
        serde_json::json!({"headerOnly": true, "limit": 10}),
    )
    .await;
    assert_eq!((status, &body), (200, &serde_json::json!({})));
}
