//! The node index service (ADR 0026): it follows the node and baseline events of the graph,
//! embeds the documents through the model gateway and answers searches.

use std::any::Any;
use std::collections::{HashMap, HashSet};
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;
use std::sync::atomic::{AtomicI64, Ordering};
use std::time::{Duration, Instant};

use async_trait::async_trait;
use goap_authz::{self as authz, Principal, Resource};
use goap_ctx::Ctx;
use goap_domain::{BaselineEvent, ChangeDocEvent, NodeEvent};
use goap_index::{self as index, Hit, Indexer, KIND_CHANGE, Searcher, Store};
use goap_llm as llm;
use parking_lot::{Mutex, RwLock};
use serde::Serialize;

/// A boxed error of a seam (the organisation, the graph).
pub type BoxError = Box<dyn std::error::Error + Send + Sync>;

/// Subjects the indexer consumes.
pub const SUBJECTS: &[&str] = &["goap.node.>", "goap.baseline.>", "goap.changeindex.>"];

/// Access is what the service asks of the organisation to authorize and scope a search (ADR 0095): implemented by
/// `goap_access::Directory`. The index itself knows no organisation.
#[async_trait]
pub trait Access: Send + Sync {
    /// MayAccessProject reports whether the principal may work on a project (a platform role, or a role on the
    /// project or above it; administrators always).
    async fn may_access_project(
        &self,
        ctx: &Ctx,
        p: &Principal,
        project: &str,
    ) -> Result<bool, BoxError>;
    /// SubProjects returns a project and the projects below it.
    async fn sub_projects(&self, ctx: &Ctx, project: &str) -> Result<Vec<String>, BoxError>;
}

#[async_trait]
impl Access for goap_access::Directory {
    async fn may_access_project(
        &self,
        ctx: &Ctx,
        p: &Principal,
        project: &str,
    ) -> Result<bool, BoxError> {
        goap_access::Directory::may_access_project(self, ctx, p, project)
            .await
            .map_err(|e| Box::new(e) as BoxError)
    }

    async fn sub_projects(&self, ctx: &Ctx, project: &str) -> Result<Vec<String>, BoxError> {
        goap_access::Directory::sub_projects(self, ctx, project)
            .await
            .map_err(|e| Box::new(e) as BoxError)
    }
}

/// The future of a [`RepublishFn`].
pub type RepublishFuture = Pin<Box<dyn Future<Output = Result<usize, BoxError>> + Send>>;

/// Republish asks the graph to publish its nodes and changes again (Reindex); it returns how many it published.
pub type RepublishFn = Arc<dyn Fn(Ctx) -> RepublishFuture + Send + Sync>;

/// Makes a [`RepublishFn`] of an async closure.
pub fn republish_fn<F, Fut>(f: F) -> RepublishFn
where
    F: Fn(Ctx) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Result<usize, BoxError>> + Send + 'static,
{
    Arc::new(move |ctx| Box::pin(f(ctx)))
}

/// A store that prepares its vector index for the dimension of the embedding model (Go's interface assertion
/// `EnsureVectorIndex(ctx, dim)`): the PostgreSQL store.
#[async_trait]
pub trait VectorIndex: Send + Sync {
    async fn ensure_vector_index(&self, ctx: &Ctx, dim: i64) -> Result<(), index::Error>;
}

#[async_trait]
impl VectorIndex for index::Postgres {
    async fn ensure_vector_index(&self, ctx: &Ctx, dim: i64) -> Result<(), index::Error> {
        index::Postgres::ensure_vector_index(self, ctx, dim).await
    }
}

/// What [`Service::status`] reports.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Status {
    pub semantic: bool,
    pub store: String,
    pub nodes: i64,
    pub baselines: i64,
    pub changes: i64,
    pub errors: i64,
}

#[derive(Default)]
struct Counters {
    nodes: AtomicI64,
    baselines: AtomicI64,
    changes: AtomicI64,
    errs: AtomicI64,
}

/// Service ties the store, the indexer and the searcher.
pub struct Service {
    pub store: Arc<dyn Store>,
    pub indexer: Indexer,
    /// Searcher is built by new; its authorizer filters its hits (none without an authorizer).
    pub searcher: Searcher,
    /// StoreName is shown by Status.
    pub store_name: String,
    republish: RwLock<Option<RepublishFn>>,
    access: Arc<RwLock<Option<Arc<dyn Access>>>>,
    embed: Option<Arc<Embedder>>,
    counters: Arc<Counters>,
}

impl Service {
    /// New builds the service. embedder may be None (text-only index); authorizer may be None (no filtering).
    pub fn new<S: Store + 'static>(
        store: Arc<S>,
        embedder: Option<Arc<dyn llm::Embedder>>,
        authorizer: Option<Arc<dyn authz::Authorizer>>,
    ) -> Service {
        let name = std::any::type_name::<S>()
            .rsplit("::")
            .next()
            .unwrap_or_default()
            .to_string();
        let any: Arc<dyn Any + Send + Sync> = store.clone();
        let vectors = any
            .downcast::<index::Postgres>()
            .ok()
            .map(|p| p as Arc<dyn VectorIndex>);
        Service::with_store(store, name, vectors, embedder, authorizer)
    }

    /// The service of a store known as a trait object: its name for Status and, for a store with a vector index to
    /// prepare, that index.
    pub fn with_store(
        store: Arc<dyn Store>,
        store_name: impl Into<String>,
        vectors: Option<Arc<dyn VectorIndex>>,
        embedder: Option<Arc<dyn llm::Embedder>>,
        authorizer: Option<Arc<dyn authz::Authorizer>>,
    ) -> Service {
        let counters = Arc::new(Counters::default());
        let embed = embedder.map(|client| {
            Arc::new(Embedder {
                vectors,
                ..Embedder::new(client)
            })
        });
        let e: Option<Arc<dyn index::Embedder>> =
            embed.clone().map(|e| e as Arc<dyn index::Embedder>);
        let errs = counters.clone();
        let mut indexer = Indexer::new(store.clone());
        indexer.embedder = e.clone();
        indexer.embed_failed = Some(Arc::new(move |key: &str, err: &index::Error| {
            errs.errs.fetch_add(1, Ordering::Relaxed);
            tracing::debug!(key, err = %err, "embedding skipped");
        }));
        let mut searcher = Searcher::new(store.clone());
        searcher.embedder = e;
        let access: Arc<RwLock<Option<Arc<dyn Access>>>> = Arc::new(RwLock::new(None));
        if let Some(a) = authorizer {
            searcher.authorize = Some(Arc::new(ReadFilter {
                authz: a,
                access: access.clone(),
            }));
        }
        Service {
            store,
            indexer,
            searcher,
            store_name: store_name.into(),
            republish: RwLock::new(None),
            access,
            embed,
            counters,
        }
    }

    /// Access decides which projects a caller may see and expands "a project and its sub-projects"; set it before
    /// serving. None: no project check (tests, and a deployment with no organisation to ask).
    pub fn set_access(&self, a: Option<Arc<dyn Access>>) {
        *self.access.write() = a;
    }

    /// The Access in use.
    pub fn access(&self) -> Option<Arc<dyn Access>> {
        self.access.read().clone()
    }

    /// Republish asks the graph to publish its nodes again (Reindex); None: not available.
    pub fn set_republish(&self, f: Option<RepublishFn>) {
        *self.republish.write() = f;
    }

    /// Handle processes one event of the bus: the subject tells its kind. Unknown subjects are ignored, and so is a
    /// malformed event (not worth a redelivery).
    pub async fn handle(&self, ctx: &Ctx, subject: &str, data: &[u8]) -> Result<(), index::Error> {
        let c = &self.counters;
        if subject.starts_with("goap.node.") {
            let Ok(ev) = serde_json::from_slice::<NodeEvent>(data) else {
                return Ok(());
            };
            return self.count(&c.nodes, self.indexer.on_node(ctx, &ev).await);
        }
        if subject.starts_with("goap.baseline.") {
            let Ok(ev) = serde_json::from_slice::<BaselineEvent>(data) else {
                return Ok(());
            };
            return self.count(&c.baselines, self.indexer.on_baseline(ctx, &ev).await);
        }
        if subject.starts_with("goap.changeindex.") {
            let Ok(ev) = serde_json::from_slice::<ChangeDocEvent>(data) else {
                return Ok(());
            };
            let personal = goap_access::personal_subject(&ev.owner_org).to_string();
            return self.count(
                &c.changes,
                self.indexer.on_change(ctx, &ev, &personal).await,
            );
        }
        Ok(())
    }

    fn count(&self, c: &AtomicI64, res: Result<(), index::Error>) -> Result<(), index::Error> {
        match res {
            Err(e) => {
                self.counters.errs.fetch_add(1, Ordering::Relaxed);
                Err(e)
            }
            Ok(()) => {
                c.fetch_add(1, Ordering::Relaxed);
                Ok(())
            }
        }
    }

    /// Reindex empties the index and has the graph publish its nodes and changes again.
    pub async fn reindex(&self, ctx: &Ctx) -> Result<usize, BoxError> {
        let Some(republish) = self.republish.read().clone() else {
            return Err("reindex is not available".into());
        };
        self.store.reset(ctx).await?;
        if let Some(e) = &self.embed {
            e.reset();
        }
        republish(ctx.clone()).await
    }

    /// Status reports what the service does.
    pub fn status(&self) -> Status {
        let c = &self.counters;
        Status {
            semantic: self.embed.as_ref().is_some_and(|e| e.available()),
            store: self.store_name.clone(),
            nodes: c.nodes.load(Ordering::Relaxed),
            baselines: c.baselines.load(Ordering::Relaxed),
            changes: c.changes.load(Ordering::Relaxed),
            errors: c.errs.load(Ordering::Relaxed),
        }
    }
}

/// readFilter keeps the documents the caller may see (ABAC, ADR 0095), before anything is counted or quoted:
///   - a node: action "read" on its type, in its namespace, and access to the project it was created in (a platform
///     role, or a role on the project or above it; administrators see all);
///   - a change: a personal one (held by a personal unit, ADR 0037) only for its subject, administrators included;
///     any other one needs access to its project.
///
/// Callers without identity are trusted internal services and see everything (the gateway always identifies the
/// callers of the public API; the index port is not exposed).
struct ReadFilter {
    authz: Arc<dyn authz::Authorizer>,
    access: Arc<RwLock<Option<Arc<dyn Access>>>>,
}

#[async_trait]
impl index::Authorizer for ReadFilter {
    async fn authorize(&self, ctx: &Ctx, hits: Vec<Hit>) -> Result<Vec<Hit>, index::Error> {
        let who = authz::principal_from(ctx);
        if who.anonymous() {
            return Ok(hits);
        }
        let acc = self.access.read().clone();
        let mut decided: HashMap<(String, String), bool> = HashMap::new();
        let mut projects: HashMap<String, bool> = HashMap::new();
        let mut out = Vec::with_capacity(hits.len());
        for h in hits {
            if h.kind == KIND_CHANGE {
                if !h.personal_to.is_empty() {
                    if h.personal_to == who.subject {
                        out.push(h);
                    }
                    continue;
                }
                if may_project(ctx, acc.as_deref(), &who, &mut projects, &h.project).await? {
                    out.push(h);
                }
                continue;
            }
            let k = (h.r#type.clone(), h.namespace.clone());
            let ok = match decided.get(&k) {
                Some(ok) => *ok,
                None => {
                    let req = authz::Request {
                        subject: who.clone(),
                        action: "read".into(),
                        resource: Resource {
                            r#type: h.r#type.clone(),
                            namespace: h.namespace.clone(),
                            ..Resource::default()
                        },
                    };
                    let ok = self
                        .authz
                        .authorize(ctx, &req)
                        .await
                        .map_err(index::Error::other)?;
                    decided.insert(k, ok);
                    ok
                }
            };
            if !ok {
                continue;
            }
            if may_project(ctx, acc.as_deref(), &who, &mut projects, &h.project).await? {
                out.push(h);
            }
        }
        Ok(out)
    }
}

async fn may_project(
    ctx: &Ctx,
    acc: Option<&dyn Access>,
    who: &Principal,
    seen: &mut HashMap<String, bool>,
    project: &str,
) -> Result<bool, index::Error> {
    let Some(acc) = acc else { return Ok(true) };
    if project.is_empty() {
        return Ok(true);
    }
    if let Some(ok) = seen.get(project) {
        return Ok(*ok);
    }
    let ok = acc
        .may_access_project(ctx, who, project)
        .await
        .map_err(index::Error::Other)?;
    seen.insert(project.to_string(), ok);
    Ok(ok)
}

/// Sink is the event sink of the graph for the all-in-one mode: events are handled in order by one task, off the
/// writer's path (embedding calls are slow).
#[derive(Clone)]
pub struct Sink {
    ch: tokio::sync::mpsc::Sender<(Ctx, String, Vec<u8>)>,
}

impl Sink {
    /// NewSink starts the worker; it stops with ctx.
    pub fn new(ctx: &Ctx, svc: Arc<Service>) -> Sink {
        let (tx, mut rx) = tokio::sync::mpsc::channel::<(Ctx, String, Vec<u8>)>(4096);
        let ctx = ctx.clone();
        tokio::spawn(async move {
            loop {
                tokio::select! {
                    _ = ctx.done() => return,
                    m = rx.recv() => {
                        let Some((ctx, subject, data)) = m else { return };
                        if let Err(e) = svc.handle(&ctx, &subject, &data).await {
                            tracing::warn!(subject, err = %e, "index event");
                        }
                    }
                }
            }
        });
        Sink { ch: tx }
    }

    /// Publish takes the events the index follows (nodes, baselines, change documents) and leaves the others.
    pub async fn publish<T: Serialize + ?Sized>(
        &self,
        ctx: &Ctx,
        subject: &str,
        v: &T,
    ) -> Result<(), serde_json::Error> {
        if !subject.starts_with("goap.node.")
            && !subject.starts_with("goap.baseline.")
            && !subject.starts_with("goap.changeindex.")
        {
            return Ok(());
        }
        let data = serde_json::to_vec(v)?;
        // a stopped worker drops the event, as the Go worker would never read it
        let _ = self
            .ch
            .send((ctx.without_cancel(), subject.to_string(), data))
            .await;
        Ok(())
    }
}

/// retryAfter is how long a failed embedder is left alone.
pub const RETRY_AFTER: Duration = Duration::from_secs(30);

#[derive(Default)]
struct EmbedState {
    until: Option<Instant>,
    dim: usize,
    ok: bool,
}

/// Embedder adapts the model gateway to `goap_index::Embedder`. After a failure (no embedding model configured,
/// quota…) it stays quiet for a while instead of calling the gateway for every node; the first success makes the
/// store prepare its vector index for the model's dimension.
pub struct Embedder {
    pub client: Arc<dyn llm::Embedder>,
    /// alias or provider/model; empty: the "embed" alias
    pub model: String,
    /// The vector index of the store to prepare (Go's `Store` field, asserted to `EnsureVectorIndex`).
    pub vectors: Option<Arc<dyn VectorIndex>>,
    state: Mutex<EmbedState>,
}

impl Embedder {
    pub fn new(client: Arc<dyn llm::Embedder>) -> Embedder {
        Embedder {
            client,
            model: String::new(),
            vectors: None,
            state: Mutex::new(EmbedState::default()),
        }
    }

    /// Available tells whether the last embedding call worked.
    pub fn available(&self) -> bool {
        self.state.lock().ok
    }

    fn reset(&self) {
        let mut s = self.state.lock();
        s.until = None;
        s.dim = 0;
    }
}

#[async_trait]
impl index::Embedder for Embedder {
    async fn embed(&self, ctx: &Ctx, texts: &[String]) -> Result<Vec<Vec<f32>>, index::Error> {
        if self.state.lock().until.is_some_and(|u| Instant::now() < u) {
            return Err(index::Error::msg(
                "embedding model unavailable, retrying later",
            ));
        }
        let mctx = llm::with_meta(
            ctx,
            llm::CallMeta {
                source: llm::SOURCE_INDEXER.into(),
                ..llm::CallMeta::default()
            },
        );
        let res = self
            .client
            .embed(
                &mctx,
                llm::EmbedRequest {
                    model: self.model.clone(),
                    texts: texts.to_vec(),
                },
            )
            .await;
        let r = match res {
            Err(e) => {
                {
                    let mut s = self.state.lock();
                    s.until = Some(Instant::now() + RETRY_AFTER);
                    s.ok = false;
                }
                tracing::warn!(err = %e, "embedding unavailable: indexing text only");
                return Err(index::Error::other(e));
            }
            Ok(r) => r,
        };
        let ensure = {
            let mut s = self.state.lock();
            s.ok = true;
            match r.vectors.first() {
                Some(v) if v.len() != s.dim => {
                    s.dim = v.len();
                    Some(s.dim)
                }
                _ => None,
            }
        };
        if let (Some(dim), Some(vi)) = (ensure, &self.vectors)
            && let Err(e) = vi.ensure_vector_index(ctx, dim as i64).await
        {
            tracing::error!(dim, err = %e, "vector index");
        }
        Ok(r.vectors)
    }
}

/// The distinct values of `vals` in their first order (Go's `slices.Contains` dedup).
pub(crate) fn dedup(vals: impl IntoIterator<Item = String>) -> Vec<String> {
    let mut seen = HashSet::new();
    vals.into_iter()
        .filter(|v| seen.insert(v.clone()))
        .collect()
}
