//! Two graph spaces (ADR 0154 §9): the platform graph serves the platform, the organisation and the methodologies, and
//! is the one graph its callers know; the namespaces of the content live in a content graph it forwards their calls to.
//! [`Router`] is that forwarding, in front of the graph service, as Go's `graphsvc.Router` is: a unary call naming a
//! namespace another graph holds (a top-level `namespace` field, or the one of the view it reads), or a change that
//! lives there (`change_id`, or the `id` of a call on a change), is passed as is, headers and body, to that graph; any
//! other call is served here, and passed to the other graphs in turn when this one answers not found (a node read by
//! its id). The graphs are the declared sources (`platform@Source` of kind graph with a location and the namespaces it
//! holds). The overview of the namespaces and their list are every graph's, each namespace naming its source; the other
//! listings are this graph's.
use std::{
    collections::HashMap,
    sync::{Arc, LazyLock, Mutex},
};

use axum::{
    body::{Body, Bytes, to_bytes},
    extract::{Request, State as Extract},
    http::{HeaderMap, Method, StatusCode, header},
    middleware::Next,
    response::{IntoResponse, Response},
};
use goap_connect::{Codec, Message};
use goap_proto::graph::v1 as pb;
use graph_core::{
    model::{ChangeId, MAIN_BRANCH, NAMESPACE_PLATFORM},
    read,
    sources::{self, Source},
    store::ReadTx,
};
use prost_reflect::{
    DescriptorPool, DeserializeOptions, DynamicMessage, Kind, MethodDescriptor, ReflectMessage, Value,
};

use crate::State;

/// The largest request body the router reads to route it (`RouterMaxBody`); a larger one is served here.
pub const MAX_BODY: usize = 16 << 20;

/// The name of this graph's source in the listings it merges (`graphsvc.PlatformSource`).
const LOCAL_SOURCE: &str = "platform";

const SERVICE: &str = "goap.graph.v1.GraphService";

/// The headers a lookup of a change carries over: the caller's identity, its command, its positions in the logs.
const FORWARDED_PREFIX: &str = "x-goap-";

static POOL: LazyLock<DescriptorPool> =
    LazyLock::new(|| DescriptorPool::decode(goap_proto::DESCRIPTOR).expect("descriptors of proto/"));

/// What the router asks of this graph: the other graphs declared, and whether it holds a change.
#[goap_connect::async_trait]
pub trait Graphs: Send + Sync {
    /// The declared graph sources holding namespaces, each location once (`Router.graphs`).
    async fn graphs(&self) -> Result<Arc<Vec<Source>>, String>;
    /// Whether this graph holds a change (`Graph.HasChange`).
    async fn holds(&self, id: &str) -> Result<bool, String>;
}

/// The graphs this graph's store declares.
pub struct Declared {
    state: Arc<State>,
    /// The graphs declared at a head of the platform namespace, read again when the head moves.
    at: Mutex<Option<(String, Arc<Vec<Source>>)>>,
}

impl Declared {
    pub fn new(state: Arc<State>) -> Declared {
        Declared {
            state,
            at: Mutex::new(None),
        }
    }
}

#[goap_connect::async_trait]
impl Graphs for Declared {
    async fn graphs(&self) -> Result<Arc<Vec<Source>>, String> {
        let tx = self.state.store.reader().await.map_err(|e| e.to_string())?;
        let head = match read::branch_head(&tx, NAMESPACE_PLATFORM, MAIN_BRANCH).await {
            Ok(h) => h,
            Err(e) if e.is_not_found() => return Ok(Arc::default()),
            Err(e) => return Err(e.message),
        };
        if let Some((at, gs)) = self.at.lock().unwrap().as_ref()
            && *at == head.id.0
        {
            return Ok(gs.clone());
        }
        let (all, _) = sources::declared_at(&tx, &head).await.map_err(|e| e.message)?;
        let mut out: Vec<Source> = Vec::new();
        for src in all {
            if src.kind == sources::KIND_GRAPH
                && !src.location.is_empty()
                && !src.namespaces.is_empty()
                && !out.iter().any(|o| o.location == src.location)
            {
                out.push(src);
            }
        }
        let out = Arc::new(out);
        *self.at.lock().unwrap() = Some((head.id.0.clone(), out.clone()));
        Ok(out)
    }

    async fn holds(&self, id: &str) -> Result<bool, String> {
        let tx = self.state.store.reader().await.map_err(|e| e.to_string())?;
        // an id the store cannot read (not a UUID, on PostgreSQL) is an error, in its words, as Go's `HasChange` says
        Ok(tx.change(&ChangeId::from(id)).await.map_err(|e| e.message)?.is_some())
    }
}

/// Routes the calls of the graph service between this graph and the graphs holding other namespaces.
pub struct Router {
    graphs: Arc<dyn Graphs>,
    http: reqwest::Client,
    /// Change id → base URL of the graph holding it ("" for this graph).
    changes: Mutex<HashMap<String, String>>,
}

impl Router {
    pub fn new(graphs: Arc<dyn Graphs>) -> Router {
        Router {
            graphs,
            http: reqwest::Client::new(),
            changes: Mutex::new(HashMap::new()),
        }
    }

    /// The graph holding a change: this one when it holds it, else the first of the other graphs that knows it,
    /// remembered; one no graph knows is this graph's, which answers not found (`Router.changeBase`).
    async fn change_base(&self, headers: &HeaderMap, gs: &[Source], id: &str) -> Result<String, String> {
        if let Some(b) = self.changes.lock().unwrap().get(id) {
            return Ok(b.clone());
        }
        if self.graphs.holds(id).await? {
            self.changes.lock().unwrap().insert(id.to_string(), String::new());
            return Ok(String::new());
        }
        for g in gs {
            let mut get = self
                .http
                .post(format!("{}/{SERVICE}/GetChange", g.location.trim_end_matches('/')))
                .header(header::CONTENT_TYPE, "application/json")
                .body(serde_json::json!({ "id": id }).to_string());
            for (k, v) in headers {
                if k.as_str().starts_with(FORWARDED_PREFIX) || k == header::AUTHORIZATION {
                    get = get.header(k, v);
                }
            }
            let res = get.send().await.map_err(|e| format!("graph {}: {e}", g.location))?;
            match res.status() {
                StatusCode::OK => {
                    self.changes.lock().unwrap().insert(id.to_string(), g.location.clone());
                    return Ok(g.location.clone());
                }
                StatusCode::NOT_FOUND | StatusCode::FORBIDDEN => {}
                s => {
                    let body = res.text().await.unwrap_or_default();
                    return Err(format!("graph {}: {s} {body}", g.location));
                }
            }
        }
        Ok(String::new())
    }

    /// Passes a call to the graph at `base`, its headers and body as they came, and its answer back.
    async fn forward(&self, base: &str, path: &str, headers: &HeaderMap, body: Bytes) -> Response {
        let mut out = self
            .http
            .post(format!("{}{path}", base.trim_end_matches('/')))
            .body(body);
        for (k, v) in headers {
            if k != header::CONTENT_LENGTH && k != header::HOST {
                out = out.header(k, v);
            }
        }
        let res = match out.send().await {
            Ok(r) => r,
            Err(e) => return bad_gateway(e.to_string()),
        };
        let mut b = Response::builder().status(res.status());
        for (k, v) in res.headers() {
            if k != header::TRANSFER_ENCODING && k != header::CONNECTION {
                b = b.header(k, v);
            }
        }
        match res.bytes().await {
            Ok(bytes) => b.body(Body::from(bytes)).unwrap_or_else(|e| bad_gateway(e.to_string())),
            Err(e) => bad_gateway(e.to_string()),
        }
    }
}

fn bad_gateway(msg: String) -> Response {
    // in the words of Go's `http.Error`
    (StatusCode::BAD_GATEWAY, format!("{msg}\n")).into_response()
}

/// The method of the graph service a path names, none for another service.
fn method(path: &str) -> Option<MethodDescriptor> {
    let (svc, name) = path.trim_start_matches('/').split_once('/')?;
    if svc != SERVICE {
        return None;
    }
    POOL.get_service_by_name(SERVICE)?.methods().find(|m| m.name() == name)
}

/// What a call names: the graph it goes to is then known.
enum Named {
    Namespace(String),
    Change(String),
    Nothing,
}

/// What a request names, read from its body: its namespace (that of the view it reads, ADR 0157), else its change.
/// A request that does not decode is this graph's to answer, as one naming something.
fn named(m: &MethodDescriptor, json: bool, body: &[u8]) -> Option<Named> {
    let input = m.input();
    let msg = if json {
        let mut de = serde_json::Deserializer::from_slice(body);
        DynamicMessage::deserialize_with_options(input, &mut de, &DeserializeOptions::new().deny_unknown_fields(false))
            .ok()?
    } else {
        DynamicMessage::decode(input, body).ok()?
    };
    let view = msg.descriptor().get_field_by_name("view").and_then(|f| match f.kind() {
        Kind::Message(d) if d.full_name() == "goap.graph.v1.View" && !f.is_list() && msg.has_field(&f) => {
            match msg.get_field(&f).into_owned() {
                Value::Message(v) => Some(v),
                _ => None,
            }
        }
        _ => None,
    });
    let fields = view.as_ref().unwrap_or(&msg);
    let string = |m: &DynamicMessage, name: &str| -> String {
        match m.descriptor().get_field_by_name(name) {
            Some(f) if f.kind() == Kind::String && !f.is_list() && !f.is_map() => {
                m.get_field(&f).as_str().unwrap_or_default().to_string()
            }
            _ => String::new(),
        }
    };
    let ns = string(fields, "namespace");
    if !ns.is_empty() {
        return Some(Named::Namespace(ns));
    }
    let mut id = string(fields, "change_id");
    if id.is_empty() && m.name().contains("Change") {
        id = string(fields, "id");
    }
    Some(if id.is_empty() {
        Named::Nothing
    } else {
        Named::Change(id)
    })
}

/// The middleware routing the calls of the graph service (`Router.ServeHTTP`).
pub async fn route(Extract(r): Extract<Arc<Router>>, req: Request, next: Next) -> Response {
    if req.method() != Method::POST {
        return next.run(req).await;
    }
    let Some(m) = method(req.uri().path()) else {
        return next.run(req).await;
    };
    let gs = match r.graphs.graphs().await {
        Ok(gs) => gs,
        Err(e) => return bad_gateway(e),
    };
    if gs.is_empty() {
        return next.run(req).await;
    }
    match m.name() {
        "GetGraphOverview" => return merge::<pb::GetGraphOverviewResponse>(&r, &gs, req, next).await,
        "ListNamespaces" => return merge::<pb::ListNamespacesResponse>(&r, &gs, req, next).await,
        _ => {}
    }
    let ct = req
        .headers()
        .get(header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or_default()
        .to_string();
    if req.headers().contains_key(header::CONTENT_ENCODING)
        || (ct != "application/proto" && ct != "application/json")
        || m.is_client_streaming()
        || m.is_server_streaming()
    {
        return next.run(req).await; // streaming, gRPC, compressed: served here
    }
    let (parts, body) = req.into_parts();
    let Ok(body) = to_bytes(body, MAX_BODY).await else {
        // too large to route (or unreadable): this graph answers what it reads of it
        return next.run(Request::from_parts(parts, Body::empty())).await;
    };
    let path = parts.uri.path().to_string();
    let base = match named(&m, ct == "application/json", &body) {
        None => String::new(),
        Some(Named::Namespace(ns)) => gs
            .iter()
            .find(|g| g.holds(&ns))
            .map(|g| g.location.clone())
            .unwrap_or_default(),
        Some(Named::Change(id)) => match r.change_base(&parts.headers, &gs, &id).await {
            Ok(b) => b,
            Err(e) => return bad_gateway(e),
        },
        Some(Named::Nothing) => {
            // this graph, else the first other graph that knows what it reads
            let headers = parts.headers.clone();
            let local = next.run(Request::from_parts(parts, Body::from(body.clone()))).await;
            if local.status() != StatusCode::NOT_FOUND {
                return local;
            }
            for g in gs.iter() {
                let other = r.forward(&g.location, &path, &headers, body.clone()).await;
                if other.status() != StatusCode::NOT_FOUND {
                    return other;
                }
            }
            return local;
        }
    };
    if base.is_empty() {
        return next.run(Request::from_parts(parts, Body::from(body))).await;
    }
    r.forward(&base, &path, &parts.headers, body).await
}

/// A listing every graph answers, merged.
trait Listing: Message + Sized {
    /// Names the source of the namespaces that name none.
    fn stamp(&mut self, source: &str);
    /// Adds the namespaces of another listing this one does not list yet.
    fn absorb(&mut self, other: Self);
}

impl Listing for pb::GetGraphOverviewResponse {
    fn stamp(&mut self, source: &str) {
        for n in &mut self.namespaces {
            if n.source.is_empty() {
                n.source = source.to_string();
            }
        }
    }

    fn absorb(&mut self, other: Self) {
        for n in other.namespaces {
            if !self.namespaces.iter().any(|x| x.namespace == n.namespace) {
                self.namespaces.push(n);
            }
        }
    }
}

impl Listing for pb::ListNamespacesResponse {
    fn stamp(&mut self, _: &str) {}

    fn absorb(&mut self, other: Self) {
        for n in other.namespaces {
            if !self.namespaces.contains(&n) {
                self.namespaces.push(n);
            }
        }
        self.namespaces.sort();
    }
}

/// Serves a listing every graph answers: this graph's answer, then each graph source's, a namespace listed once (this
/// graph first), each summary naming its source. A graph that does not answer is left out of the listing.
async fn merge<L: Listing>(r: &Router, gs: &[Source], req: Request, next: Next) -> Response {
    let (mut parts, body) = req.into_parts();
    let Ok(body) = to_bytes(body, MAX_BODY).await else {
        return (StatusCode::BAD_REQUEST, "request too large").into_response();
    };
    // the answers are decoded here: none compressed
    parts.headers.remove(header::ACCEPT_ENCODING);
    parts.headers.remove("connect-accept-encoding");
    let codec = parts
        .headers
        .get(header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .and_then(Codec::from_unary_content_type);
    let headers = parts.headers.clone();
    let path = parts.uri.path().to_string();
    let local = next.run(Request::from_parts(parts, Body::from(body.clone()))).await;
    let Some(codec) = codec.filter(|_| local.status() == StatusCode::OK) else {
        return local;
    };
    let (lp, lb) = local.into_parts();
    let Ok(lb) = to_bytes(lb, usize::MAX).await else {
        return bad_gateway("the answer of this graph could not be read".into());
    };
    let Ok(mut out) = codec.decode::<L>(&lb) else {
        return Response::from_parts(lp, Body::from(lb));
    };
    out.stamp(LOCAL_SOURCE);
    for g in gs {
        let other = r.forward(&g.location, &path, &headers, body.clone()).await;
        if other.status() != StatusCode::OK {
            continue;
        }
        let Ok(b) = to_bytes(other.into_body(), usize::MAX).await else {
            continue;
        };
        if let Ok(mut msg) = codec.decode::<L>(&b) {
            msg.stamp(&g.name);
            out.absorb(msg);
        }
    }
    let b = match codec.encode(&out) {
        Ok(b) => b,
        Err(e) => return (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()).into_response(),
    };
    let mut res = Response::from_parts(lp, Body::from(b));
    res.headers_mut().remove(header::CONTENT_LENGTH);
    res.headers_mut().remove(header::CONTENT_ENCODING);
    res
}
