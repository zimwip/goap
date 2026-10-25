//! The router of the graph service (Go's `TestRouter`): the platform graph serves its namespaces and forwards the calls
//! naming a namespace or a change of the content graph to it, headers included (ADR 0154 §9).
use std::sync::{Arc, Mutex};

use goap_connect::{Code, ConnectError, Ctx, Interceptors, Request, Response, RpcResult, async_trait};
use goap_proto::graph::v1 as pb;
use graph_core::sources::{KIND_GRAPH, Source};
use graph_service::route::{Graphs, Router, route};
use serde_json::{Value, json};

/// A graph answering as named by its title: the changes it holds, a change created in it, a node of its own.
struct Fake {
    name: &'static str,
    changes: &'static [&'static str],
    asked: Mutex<Vec<String>>,
}

impl Fake {
    fn new(name: &'static str, changes: &'static [&'static str]) -> Arc<Fake> {
        Arc::new(Fake {
            name,
            changes,
            asked: Mutex::new(Vec::new()),
        })
    }

    fn namespaces(&self) -> Vec<String> {
        match self.name {
            "platform" => vec!["organisation".into(), "platform".into()],
            _ => vec!["alm".into(), "code".into()],
        }
    }
}

#[async_trait]
impl pb::GraphService for Fake {
    async fn get_change(&self, _: &Ctx, req: Request<pb::GetChangeRequest>) -> RpcResult<pb::GetChangeResponse> {
        self.asked.lock().unwrap().push(req.header("x-goap-user").to_string());
        if !self.changes.contains(&req.msg.id.as_str()) {
            return Err(ConnectError::new(Code::NotFound, "no such change"));
        }
        Ok(Response::new(pb::GetChangeResponse {
            change: Some(pb::Change {
                id: req.msg.id.clone(),
                title: self.name.into(),
                ..Default::default()
            }),
        }))
    }

    async fn create_change(
        &self,
        _: &Ctx,
        req: Request<pb::CreateChangeRequest>,
    ) -> RpcResult<pb::CreateChangeResponse> {
        self.asked.lock().unwrap().push(req.header("x-goap-user").to_string());
        Ok(Response::new(pb::CreateChangeResponse {
            change: Some(pb::Change {
                id: "new".into(),
                title: self.name.into(),
                ..Default::default()
            }),
        }))
    }

    async fn get_node(&self, _: &Ctx, req: Request<pb::GetNodeRequest>) -> RpcResult<pb::GetNodeResponse> {
        let id = req.msg.r#ref.as_ref().map(|r| r.id.clone()).unwrap_or_default();
        if id != format!("{}-node", self.name) {
            return Err(ConnectError::new(Code::NotFound, "no such node"));
        }
        Ok(Response::new(pb::GetNodeResponse {
            view: Some(pb::NodeView {
                node: Some(pb::Node {
                    id,
                    key: self.name.into(),
                    ..Default::default()
                }),
                ..Default::default()
            }),
        }))
    }

    async fn get_graph_overview(
        &self,
        _: &Ctx,
        _: Request<pb::GetGraphOverviewRequest>,
    ) -> RpcResult<pb::GetGraphOverviewResponse> {
        Ok(Response::new(pb::GetGraphOverviewResponse {
            namespaces: self
                .namespaces()
                .into_iter()
                .map(|namespace| pb::NamespaceSummary {
                    namespace,
                    nodes: 1,
                    ..Default::default()
                })
                .collect(),
        }))
    }

    async fn list_namespaces(
        &self,
        _: &Ctx,
        _: Request<pb::ListNamespacesRequest>,
    ) -> RpcResult<pb::ListNamespacesResponse> {
        Ok(Response::new(pb::ListNamespacesResponse {
            namespaces: self.namespaces(),
        }))
    }
}

/// The content graph, declared as a source holding its namespaces; the platform source, this graph, routes nothing.
struct Declared {
    content: String,
    local: &'static [&'static str],
}

#[async_trait]
impl Graphs for Declared {
    async fn graphs(&self) -> Result<Arc<Vec<Source>>, String> {
        Ok(Arc::new(vec![Source {
            name: "content".into(),
            kind: KIND_GRAPH.into(),
            location: self.content.clone(),
            namespaces: vec!["alm".into(), "code".into()],
            ..Source::default()
        }]))
    }

    async fn holds(&self, id: &str) -> Result<bool, String> {
        Ok(self.local.contains(&id))
    }
}

async fn listen(app: axum::Router) -> String {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(axum::serve(listener, app).into_future());
    format!("http://{addr}")
}

async fn call(base: &str, method: &str, body: Value) -> (u16, Value) {
    let res = reqwest::Client::new()
        .post(format!("{base}/goap.graph.v1.GraphService/{method}"))
        .header("content-type", "application/json")
        .header("x-goap-user", "ada")
        .body(body.to_string())
        .send()
        .await
        .unwrap();
    (res.status().as_u16(), res.json().await.unwrap())
}

#[tokio::test]
async fn calls_go_to_the_graph_holding_what_they_name() {
    const LOCAL: &[&str] = &["C-local"];
    let content = Fake::new("content", &["C-content"]);
    let content_url = listen(pb::graph_service_router(content.clone(), Interceptors::none())).await;
    let router = Arc::new(Router::new(Arc::new(Declared {
        content: content_url,
        local: LOCAL,
    })));
    let base = listen(
        pb::graph_service_router(Fake::new("platform", LOCAL), Interceptors::none())
            .layer(axum::middleware::from_fn_with_state(router, route)),
    )
    .await;

    for (ns, by) in [("alm", "content"), ("organisation", "platform")] {
        let (s, b) = call(&base, "CreateChange", json!({"namespace": ns, "title": "t"})).await;
        assert_eq!((s, b["change"]["title"].as_str()), (200, Some(by)), "{ns}: {b}");
    }
    for (id, by) in [("C-content", "content"), ("C-local", "platform")] {
        let (s, b) = call(&base, "GetChange", json!({"id": id})).await;
        assert_eq!((s, b["change"]["title"].as_str()), (200, Some(by)), "{id}: {b}");
    }
    let (s, b) = call(&base, "GetChange", json!({"id": "C-none"})).await;
    assert_eq!((s, b["code"].as_str()), (404, Some("not_found")), "{b}");
    // a node read by its id: this graph, else the graph that holds it
    for id in ["platform-node", "content-node"] {
        let (s, b) = call(&base, "GetNode", json!({"ref": {"id": id}})).await;
        assert_eq!((s, b["view"]["node"]["id"].as_str()), (200, Some(id)), "{b}");
    }
    let (s, _) = call(&base, "GetNode", json!({"ref": {"id": "none"}})).await;
    assert_eq!(s, 404);
    // the statistics list every graph, each namespace naming its source
    let (s, b) = call(&base, "GetGraphOverview", json!({})).await;
    assert_eq!(s, 200, "{b}");
    let got: Vec<(String, String)> = b["namespaces"]
        .as_array()
        .unwrap()
        .iter()
        .map(|n| {
            (
                n["namespace"].as_str().unwrap().to_string(),
                n["source"].as_str().unwrap().to_string(),
            )
        })
        .collect();
    let want = [
        ("organisation", "platform"),
        ("platform", "platform"),
        ("alm", "content"),
        ("code", "content"),
    ];
    assert_eq!(got, want.map(|(a, b)| (a.to_string(), b.to_string())), "{b}");
    let (s, b) = call(&base, "ListNamespaces", json!({})).await;
    assert_eq!(
        (s, b["namespaces"].clone()),
        (200, json!(["alm", "code", "organisation", "platform"]))
    );
    // the content graph is called as the caller
    let asked = content.asked.lock().unwrap();
    assert!(!asked.is_empty() && asked.iter().all(|w| w == "ada"), "{asked:?}");
}
