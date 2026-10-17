//! The fixtures of the tests: a fake graph standing in for graph + graphsvc.SeedModels, the stores, the services.

use std::collections::{BTreeMap, HashMap};
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use goap_authz as authz;
use goap_ctx::Ctx;
use goap_domain::{Baseline, BaselineID, Link, Node};
use goap_gen::model::v1 as pb;
use goap_graphapi::Error as GraphError;
use goap_llm as llm;
use goap_llmcfg as llmcfg;
use parking_lot::Mutex;
use serde_json::{Map, Value};

use crate::handler::{Client, Handler};
use crate::ledger::{Call, UsageFilter};
use crate::router::Instrument;
use crate::service::{SecretsFn, Service};
use crate::sqlstore::{MIGRATIONS, SQLITE_MIGRATIONS, SqlStore};
use crate::store::{AliasEntry, MemoryStore, ModelEntry, PERIOD_TOTAL, ProviderRecord, Store};

/// FakeGraph is the head of the platform namespace: its nodes, a new baseline at every edit (the Go tests run on a
/// real graph and seed it with graphsvc.SeedModels, not ported yet). It also holds the environment the
/// secrets resolve `env:` references from, so the tests set no process variable.
#[derive(Default)]
pub(crate) struct FakeGraph {
    st: Mutex<GraphState>,
    pub env: Mutex<HashMap<String, String>>,
}

#[derive(Default)]
struct GraphState {
    seq: u64,
    nodes: BTreeMap<String, Node>,
}

#[async_trait]
impl goap_graphsnap::Graph for FakeGraph {
    async fn branch_head(&self, _: &Ctx, _: &str, _: &str) -> Result<Baseline, GraphError> {
        let st = self.st.lock();
        if st.seq == 0 {
            return Err(GraphError::not_found("no baseline"));
        }
        Ok(Baseline {
            id: BaselineID::from(format!("B{}", st.seq)),
            ..Default::default()
        })
    }

    async fn baseline_graph(
        &self,
        _: &Ctx,
        _: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>), GraphError> {
        Ok((self.st.lock().nodes.values().cloned().collect(), Vec::new()))
    }
}

impl FakeGraph {
    /// Writes the node of key (created active, or its properties replaced), one change.
    pub(crate) fn put(&self, key: &str, typ: &str, props: Map<String, Value>) {
        let mut st = self.st.lock();
        st.seq += 1;
        let n = st.nodes.entry(key.to_string()).or_insert_with(|| Node {
            namespace: llmcfg::NAMESPACE_PLATFORM.into(),
            key: key.into(),
            r#type: typ.into(),
            state: "active".into(),
            ..Default::default()
        });
        n.properties = props;
    }

    /// Updates the properties of an existing node.
    pub(crate) fn update(&self, key: &str, props: Map<String, Value>) {
        let typ = self.st.lock().nodes.get(key).map(|n| n.r#type.clone());
        let typ = typ.unwrap_or_else(|| panic!("no node {key}"));
        self.put(key, &typ, props);
    }

    /// Takes an entry out of the configuration: a transition, a node is never deleted (ADR 0076).
    pub(crate) fn retire(&self, key: &str) {
        let mut st = self.st.lock();
        st.seq += 1;
        st.nodes
            .get_mut(key)
            .unwrap_or_else(|| panic!("no node {key}"))
            .state = llmcfg::STATE_RETIRED.into();
    }

    /// graphsvc.SeedModels: seeds the configuration when the graph holds no provider; says whether it did.
    pub(crate) fn seed(
        &self,
        provs: &[ProviderRecord],
        models: &[ModelEntry],
        aliases: &[AliasEntry],
    ) -> bool {
        let has = self
            .st
            .lock()
            .nodes
            .values()
            .any(|n| n.r#type == llmcfg::NODE_TYPE_PROVIDER);
        if has {
            return false;
        }
        for p in provs {
            self.put(
                &llmcfg::provider_key(&p.name),
                llmcfg::NODE_TYPE_PROVIDER,
                p.props(),
            );
        }
        for m in models {
            self.put(&m.key(), llmcfg::NODE_TYPE_MODEL, m.props());
        }
        for a in aliases {
            self.put(
                &llmcfg::alias_key(&a.alias),
                llmcfg::NODE_TYPE_ALIAS,
                a.props(),
            );
        }
        true
    }

    /// Adds behaviours (one change each).
    pub(crate) fn add_behaviors(&self, bs: &[llmcfg::Behavior]) {
        for b in bs {
            self.put(
                &llmcfg::behavior_key(&b.name),
                llmcfg::NODE_TYPE_BEHAVIOR,
                b.props(),
            );
        }
    }

    pub(crate) fn set_env(&self, k: &str, v: &str) {
        self.env.lock().insert(k.into(), v.into());
    }
}

/// The secrets of the tests: `env:<VAR>` alternatives read from the environment of the fake graph.
fn secrets(g: Arc<FakeGraph>) -> SecretsFn {
    Arc::new(move |_ctx, r: String| {
        let g = g.clone();
        Box::pin(async move {
            for alt in r.split('|') {
                if let Some(var) = alt.trim().strip_prefix("env:")
                    && let Some(v) = g.env.lock().get(var)
                    && !v.is_empty()
                {
                    return Ok(v.clone());
                }
            }
            Ok(String::new())
        })
    })
}

/// A service reading its configuration from a fresh fake graph, which the caller edits; not shared yet, so the caller
/// sets its fields first.
pub(crate) fn new_service(store: Arc<dyn Store>) -> (Service, Arc<FakeGraph>) {
    let g = Arc::new(FakeGraph::default());
    let dir = llmcfg::Directory::new(g.clone()).with_ttl(Duration::from_nanos(1));
    (
        Service::new(Arc::new(dir), store, Some(secrets(g.clone()))),
        g,
    )
}

pub(crate) fn fake_provider() -> ProviderRecord {
    ProviderRecord {
        name: "fake".into(),
        kind: "fake".into(),
        protocol: "fake".into(),
        enabled: true,
        ..Default::default()
    }
}

pub(crate) fn model(provider: &str, name: &str) -> ModelEntry {
    ModelEntry {
        provider: provider.into(),
        model: name.into(),
        enabled: true,
        ..Default::default()
    }
}

pub(crate) fn alias(a: &str, target: &str) -> AliasEntry {
    AliasEntry {
        alias: a.into(),
        target: target.into(),
        ..Default::default()
    }
}

pub(crate) fn strings(v: &[&str]) -> Vec<String> {
    v.iter().map(|s| s.to_string()).collect()
}

/// The stores of the tests: memory, SQLite in a directory removed at the end, PostgreSQL with GOAP_TEST_PG_DSN.
pub(crate) struct Stores {
    pub all: Vec<(&'static str, Arc<dyn Store>)>,
    _pg: Option<goap_pgtest::PgTest>,
    dir: PathBuf,
}

impl Drop for Stores {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

pub(crate) async fn stores() -> Stores {
    static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
    let ctx = Ctx::background();
    let dir = std::env::temp_dir().join(format!(
        "goap-modelgw-{}-{}",
        std::process::id(),
        SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
    ));
    std::fs::create_dir_all(&dir).unwrap();
    let db = goap_platform::open_sqlite(&ctx, dir.join("gw.db").to_str().unwrap())
        .await
        .unwrap();
    goap_platform::migrate_sqlite(&ctx, &db, "modelgw", &SQLITE_MIGRATIONS, ".")
        .await
        .unwrap();
    let mut all: Vec<(&'static str, Arc<dyn Store>)> = vec![
        ("memory", Arc::new(MemoryStore::new())),
        ("sqlite", Arc::new(SqlStore::Sqlite(db))),
    ];
    let mut pg = None;
    if let Ok(dsn) = std::env::var(goap_pgtest::DSN_VAR)
        && !dsn.is_empty()
    {
        let t = goap_pgtest::pool_with(&dsn, None).await;
        goap_platform::migrate(&ctx, t.pool(), &MIGRATIONS, ".")
            .await
            .unwrap();
        all.push(("postgres", Arc::new(SqlStore::Postgres(t.pool().clone()))));
        pg = Some(t);
    }
    Stores { all, _pg: pg, dir }
}

pub(crate) fn memory() -> Arc<dyn Store> {
    Arc::new(MemoryStore::new())
}

/// The context of a principal.
pub(crate) fn as_principal(p: authz::Principal) -> Ctx {
    authz::with_principal(&Ctx::background(), p)
}

/// The context of a subject.
pub(crate) fn who(sub: &str) -> Ctx {
    as_principal(authz::Principal {
        subject: sub.into(),
        ..Default::default()
    })
}

/// The context of a subject holding roles.
pub(crate) fn with_roles(sub: &str, roles: &[&str]) -> Ctx {
    as_principal(authz::Principal {
        subject: sub.into(),
        roles: strings(roles),
        ..Default::default()
    })
}

pub(crate) fn user_msg(text: &str) -> Vec<llm::Message> {
    vec![llm::Message {
        role: "user".into(),
        content: text.into(),
        ..Default::default()
    }]
}

/// The instrument of ledgerService: a "boom" answer is a provider failure, any other reports 3 + 4 tokens.
pub(crate) fn ledger_instrument() -> Instrument {
    Arc::new(|ctx, _p, _m, _req, call| {
        Box::pin(async move {
            let r = call(ctx).await?;
            if r.text.contains("boom") {
                return Err(llm::Error::Message("provider failed".into()));
            }
            Ok(llm::Response {
                usage: llm::Usage {
                    input_tokens: 3,
                    output_tokens: 4,
                    ..Default::default()
                },
                ..r
            })
        })
    })
}

/// ledgerService: a service with a "default" alias on a fake model reporting 3 input and 4 output tokens.
pub(crate) fn ledger_service(
    st: Arc<dyn Store>,
    roles: &[&str],
) -> (Service, Arc<FakeGraph>, ModelEntry) {
    let (mut svc, g) = new_service(st);
    let echo = ModelEntry {
        quota_tokens: 100,
        quota_period: PERIOD_TOTAL.into(),
        roles: strings(roles),
        ..model("fake", "echo")
    };
    g.seed(
        &[fake_provider()],
        std::slice::from_ref(&echo),
        &[alias("default", "fake/echo"), alias("embed", "fake/echo")],
    );
    svc.router.instrument = Some(ledger_instrument());
    (svc, g, echo)
}

/// The ledger as an administrator reads it.
pub(crate) async fn calls(s: &Service, f: UsageFilter) -> Vec<Call> {
    s.list_calls(&who("root"), f, true).await.unwrap().calls
}

/// userCalls: the ledger without the gateway's own calibration calls (they have their own tests).
pub(crate) async fn user_calls(s: &Service) -> Vec<Call> {
    s.wait_costs().await;
    calls(s, UsageFilter::default())
        .await
        .into_iter()
        .filter(|c| c.source != llm::SOURCE_CALIBRATION)
        .collect()
}

/// adminOnly authorizes the admin action on the platform to the subject "root".
pub(crate) struct AdminOnly;

#[async_trait]
impl authz::Authorizer for AdminOnly {
    async fn authorize(&self, _: &Ctx, r: &authz::Request) -> Result<bool, authz::Error> {
        Ok(r.subject.subject == "root")
    }
}

/// rpcClient serves the handler over HTTP; the clients forward the principal of their context.
pub(crate) async fn rpc_client(
    svc: Arc<Service>,
    authzr: Option<Arc<dyn authz::Authorizer>>,
) -> (Client, pb::ModelServiceClient) {
    let mut h = Handler::new(svc);
    h.authz = authzr;
    let app = pb::model_service_router(Arc::new(h), goap_connect::Interceptors::none());
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(ln, app).await });
    let conn = || {
        goap_connect::Client::builder(&url)
            .interceptor(goap_identity::Forward)
            .build()
    };
    (Client::new(conn()), pb::ModelServiceClient::new(conn()))
}

/// Serves an axum router on a local port; returns its base URL.
pub(crate) async fn serve(app: axum::Router) -> String {
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(ln, app).await });
    url
}

/// Go's json.Marshal of a value (keys sorted).
pub(crate) fn must_json<T: serde::Serialize + ?Sized>(v: &T) -> String {
    crate::tools::go_json(v)
}

/// A JSON object from a json! literal.
pub(crate) fn obj(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => panic!("not an object"),
    }
}

/// The connect code of an RPC result.
pub(crate) fn code_of<T>(r: &Result<T, goap_connect::ConnectError>) -> Option<goap_connect::Code> {
    r.as_ref().err().map(|e| e.code())
}

/// A request a fake upstream received.
#[derive(Debug, Clone)]
pub(crate) struct Got {
    pub path: String,
    pub headers: http::HeaderMap,
    pub body: bytes::Bytes,
}

impl Got {
    pub(crate) fn header(&self, name: &str) -> &str {
        self.headers
            .get(name)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
    }

    pub(crate) fn json(&self) -> Value {
        serde_json::from_slice(&self.body).unwrap_or(Value::Null)
    }
}

/// The answer of a fake upstream; chunks, when set, are sent one after the other instead of body.
#[derive(Debug, Clone, Default)]
pub(crate) struct Reply {
    pub status: u16,
    pub headers: Vec<(&'static str, String)>,
    pub body: String,
    pub chunks: Vec<String>,
}

impl Reply {
    pub(crate) fn ok(body: &str) -> Reply {
        Reply {
            status: 200,
            body: body.into(),
            ..Default::default()
        }
    }

    pub(crate) fn status(status: u16, body: &str) -> Reply {
        Reply {
            status,
            body: body.into(),
            ..Default::default()
        }
    }

    pub(crate) fn header(mut self, k: &'static str, v: &str) -> Reply {
        self.headers.push((k, v.into()));
        self
    }
}

/// A fake upstream answering every request with f; returns its base URL.
pub(crate) async fn upstream<F>(f: F) -> String
where
    F: Fn(Got) -> Reply + Send + Sync + 'static,
{
    let f = Arc::new(f);
    let app = axum::Router::new().fallback(axum::routing::any(
        move |uri: http::Uri, headers: http::HeaderMap, body: bytes::Bytes| {
            let f = f.clone();
            async move {
                let r = f(Got {
                    path: uri.path().to_string(),
                    headers,
                    body,
                });
                let mut b = http::Response::builder().status(r.status);
                for (k, v) in &r.headers {
                    b = b.header(*k, v);
                }
                let body = if r.chunks.is_empty() {
                    axum::body::Body::from(r.body)
                } else {
                    let chunks = r.chunks.clone();
                    axum::body::Body::from_stream(futures::stream::iter(
                        chunks
                            .into_iter()
                            .map(|c| Ok::<_, std::convert::Infallible>(bytes::Bytes::from(c))),
                    ))
                };
                b.body(body).unwrap()
            }
        },
    ));
    serve(app).await
}

/// The requests a provider was sent.
pub(crate) type Sent = Arc<Mutex<Vec<llm::Request>>>;

/// behaviorService: a ledger service whose provider records the system text it is sent (the calibration calls have
/// their own tests).
pub(crate) fn behavior_service(st: Arc<dyn Store>) -> (Service, Arc<FakeGraph>, Sent) {
    let (mut svc, g, _) = ledger_service(st, &[]);
    let sent: Sent = Arc::default();
    let inner = svc.router.instrument.clone().unwrap();
    let s = sent.clone();
    svc.router.instrument = Some(Arc::new(move |ctx, p, m, req, call| {
        if llm::meta_from(&ctx).source != llm::SOURCE_CALIBRATION {
            s.lock().push(req.clone());
        }
        inner(ctx, p, m, req, call)
    }));
    (svc, g, sent)
}

/// A behaviour, enabled or not.
pub(crate) fn behavior(name: &str, instruction: &str, enabled: bool) -> llmcfg::Behavior {
    llmcfg::Behavior {
        name: name.into(),
        instruction: instruction.into(),
        enabled,
        ..Default::default()
    }
}
