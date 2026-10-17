//! Helpers shared by the tests of the graph service (the helpers of the Go test files).
#![allow(dead_code)]

use std::sync::Arc;

use goap_authz::{Authorizer, Casbin, FLOOR_POLICIES, Principal};
use goap_connect::{Code, ConnectError, Interceptors, Request};
use goap_gen::graph::v1 as pb;
use goap_graph::{Graph, Memory};
use goap_graphsvc::Handler;

/// A graph over a memory store, untyped.
pub fn graph() -> Graph {
    Graph::new(Arc::new(Memory::new()))
}

/// The type catalogue of the domains of the repository (ADR 0012).
pub fn domains_catalog() -> Arc<goap_typecat::Catalog> {
    let ds = goap_def::load_domains("../../domains").expect("load the domains");
    Arc::new(goap_typecat::new(&ds).expect("catalogue of the domains"))
}

/// Judges a graph by a catalogue.
pub fn with_types(g: &mut Graph, cat: Arc<goap_typecat::Catalog>) {
    g.types = Some(Arc::new(move || {
        Some(cat.clone() as Arc<dyn goap_graph::TypeCatalog>)
    }));
}

/// typedGraph: a graph judged by the domains of the repository (ADR 0012).
pub fn typed_graph() -> Graph {
    let mut g = graph();
    with_types(&mut g, domains_catalog());
    g
}

/// A catalogue of one domain written in YAML.
pub fn catalog_of(yaml: &str) -> Arc<goap_typecat::Catalog> {
    let d = goap_def::parse_domain(yaml.as_bytes()).expect("parse the domain");
    Arc::new(goap_typecat::new(&[d]).expect("catalogue"))
}

/// `authz.NewCasbin(nil)`: the default policies.
pub fn casbin() -> Arc<dyn Authorizer> {
    Arc::new(Casbin::new().expect("casbin"))
}

/// `authz.NewCasbinWith(authz.FloorPolicies)`.
pub fn floor() -> Arc<dyn Authorizer> {
    Arc::new(Casbin::new_with(&FLOOR_POLICIES).expect("floor"))
}

/// A request carrying the identity headers of a caller of the unit "acme" with roles ("" none).
pub fn as_<T>(msg: T, subject: &str, roles: &str) -> Request<T> {
    let mut r = Request::new(msg);
    set(&mut r, subject, roles);
    r
}

/// Sets the identity headers of a caller of the unit "acme".
pub fn set<T>(r: &mut Request<T>, subject: &str, roles: &str) {
    goap_identity::set_headers(
        &Principal {
            subject: subject.into(),
            org: "acme".into(),
            roles: roles
                .split(',')
                .filter(|s| !s.is_empty())
                .map(str::to_string)
                .collect(),
            ..Default::default()
        },
        &mut r.headers,
    );
}

/// A request carrying the identity headers of a principal (`identity.SetHeaders`).
pub fn by<T>(msg: T, p: &Principal) -> Request<T> {
    let mut r = Request::new(msg);
    goap_identity::set_headers(p, &mut r.headers);
    r
}

/// A principal of a subject alone.
pub fn subject(s: &str) -> Principal {
    Principal {
        subject: s.into(),
        ..Default::default()
    }
}

/// The code of an RPC result (None: no error).
pub fn code<T>(r: &Result<T, ConnectError>) -> Option<Code> {
    r.as_ref().err().map(|e| e.code())
}

/// Serves a handler over HTTP with interceptors and returns its base URL.
pub async fn serve(h: Handler, interceptors: Interceptors) -> String {
    let app = pb::graph_service_router(Arc::new(h), interceptors);
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    tokio::spawn(async move { goap_connect::axum::serve(ln, app).await });
    url
}

/// The generated client of the graph service at a URL (`graphv1connect.NewGraphServiceClient`).
pub fn rpc(url: &str) -> pb::GraphServiceClient {
    pb::GraphServiceClient::new(goap_connect::Client::new(url))
}

/// The platform's client of the graph service at a URL (`graphclient.NewClient`).
pub fn client(url: &str) -> goap_graphclient::Client {
    goap_graphclient::Client::new(url)
}

/// A JSON object of pairs.
#[macro_export]
macro_rules! obj {
    ($($k:expr => $v:tt),* $(,)?) => {{
        #[allow(unused_mut)]
        let mut m = serde_json::Map::new();
        $(m.insert(($k).to_string(), serde_json::json!($v));)*
        m
    }};
}
