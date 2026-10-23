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
        "/goap.graph.v1.GraphService/GetNode",
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
