//! The Rust graph service (ADR 0163): the services the Go graph process serves (`GraphService`, `SmartTagService`,
//! `ImportService`), on the same database, and the domain registry (`DomainService`, ADR 0167), a service of its own
//! on a database of its own ([`domains_app`], binary `goap-domains-rs`). A method not written yet answers
//! `unimplemented` (the generated default) before anything else, so that `goap-proxy` in mode split replays it on the
//! Go service.
use std::sync::Arc;

use axum::{Router, http::StatusCode, response::IntoResponse, routing::get};
use goap_connect::Interceptors;
use goap_proto::{
    graph::v1 as graphpb, importer::v1 as importerpb, registry::v1 as registrypb, smarttag::v1 as smarttagpb,
};
use graph_catalog::{Catalog, record::in_force};
use graph_core::GraphError;
use graph_store::{Store, read::Session};
use tokio::sync::Mutex;

mod convert;
mod domainpb;
mod domains;
mod domainyaml;
pub mod graph;
pub mod guardian;
mod journal;
pub mod platform;
pub mod relay;
pub mod route;
mod smarttags;

pub use domains::{SYSTEM_DOMAINS, builtin_dir};
pub use graph::Graph;

/// What the handlers share.
pub struct State {
    pub store: Store,
    /// Whether a bus takes the events: written in the outbox, as the Go service does with `GOAP_NATS_URL` set, else
    /// none.
    pub outbox: bool,
    /// The relays delivering the outboxes to the bus, when this process delivers them ([`State::relay`]).
    relays: Option<Relays>,
    /// The job service the long runs are jobs of (ADR 0149), when this process reaches one.
    pub jobs: Option<Arc<graph::jobs::Jobs>>,
    /// The graph space served (ADR 0154 §9, Go's `Handler.Space`): empty for the platform graph, `content` for a
    /// content graph. It names the graph's events and job kinds apart from the platform graph's.
    pub space: &'static str,
    /// The platform graph, in a content graph (`GOAP_GRAPH_SPACE=content`, ADR 0154 §9) and in the domain registry:
    /// its organisation and its methodologies are read there.
    pub remote: Option<Arc<platform::Remote>>,
    /// Where the domains come from (ADR 0167): none, the store of this state holds them (the domain registry).
    pub domains: Option<DomainSource>,
    /// What the methodologies of the changes are asked through (ADR 0168): the guardian
    /// service; none, the changes follow their methodologies by name alone.
    pub guardian: Option<Arc<dyn graph_core::guardian::Guardian>>,
    /// The type catalogue of the domains in force, with what told the stored versions apart when it was built.
    catalog: Mutex<(Option<String>, Arc<Catalog>)>,
    /// The persons this process made sure have a user (Go's `EnsureCaller`), and the creation in progress.
    users: Mutex<std::collections::HashSet<String>>,
    ensuring: Mutex<()>,
    /// The settings calls of each subject, one at a time (Go's `lockSettings`).
    pub(crate) settings: Mutex<std::collections::HashMap<String, Arc<Mutex<()>>>>,
}

impl State {
    pub fn new(store: Store) -> State {
        State {
            store,
            outbox: false,
            relays: None,
            jobs: None,
            space: "",
            remote: None,
            domains: None,
            guardian: None,
            catalog: Mutex::new((None, Arc::new(Catalog::of(&[]).expect("an empty catalogue")))),
            users: Mutex::new(Default::default()),
            ensuring: Mutex::new(()),
            settings: Mutex::new(Default::default()),
        }
    }

    /// Writes the graph's events in its outbox and delivers them to `sink` (Go's `outbox.Start` of the graph): returns
    /// the relay, for [`relay::Relay::run`]. The graph's events are named by its space (Go's `Graph.Origin`): `graph`,
    /// `graph-content` for a content graph, so the bus takes neither's events for repeats of the other's. Set
    /// [`State::space`] first.
    pub fn relay(&mut self, sink: relay::Sink) -> Arc<relay::Relay> {
        let origin = if self.space.is_empty() {
            "graph"
        } else {
            "graph-content"
        };
        let r = Arc::new(relay::Relay::new(origin, "event_outbox", Arc::new(sink)));
        self.outbox = true;
        self.relays = Some(Relays {
            graph: Some(r.clone()),
            domains: None,
        });
        r
    }

    /// Writes the domain registry's events in its outbox and delivers them to `sink` (ADR 0167): the relay.
    pub fn relay_domains(&mut self, sink: relay::Sink) -> Arc<relay::Relay> {
        let r = Arc::new(relay::Relay::new("domains", "domain_outbox", Arc::new(sink)));
        self.outbox = true;
        self.relays = Some(Relays {
            graph: None,
            domains: Some(r.clone()),
        });
        r
    }

    /// The bus the graph's events go to, when this process delivers them.
    pub(crate) fn sink(&self) -> Option<Arc<relay::Sink>> {
        self.relays.as_ref().and_then(|r| r.graph.as_ref()).map(|r| r.sink())
    }

    /// Delivers the graph's events a commit wrote, at once.
    pub(crate) async fn deliver_graph(&self) {
        if let Some(r) = self.relays.as_ref().and_then(|r| r.graph.as_ref()) {
            r.after_commit(&self.store).await;
        }
    }

    /// Delivers the domain registry's events a commit wrote, at once.
    pub(crate) async fn deliver_domains(&self) {
        if let Some(r) = self.relays.as_ref().and_then(|r| r.domains.as_ref()) {
            r.after_commit(&self.store).await;
        }
    }

    /// The type catalogue of the domains in force (`typecat.Live`): the registry's ([`State::domains`]), else the one
    /// of the domains this store holds, built again when a stored version changed. Domains that do not make a
    /// catalogue leave the previous one in force, as `Live.Reload` does.
    pub async fn catalog(&self, tx: &Session) -> Result<Arc<Catalog>, GraphError> {
        match &self.domains {
            Some(DomainSource::Remote(r)) => {
                let mut cur = self.catalog.lock().await;
                let c = r.catalog(cur.1.clone()).await?;
                cur.1 = c.clone();
                Ok(c)
            }
            Some(DomainSource::Local(d)) => {
                let s = d.store.reader().await.map_err(GraphError::internal)?;
                Box::pin(d.catalog(&s)).await
            }
            None => self.stored_catalog(tx).await,
        }
    }

    /// The definitions of the domains in force (`domainsvc.Service.Domains`, a `domainclient` of the registry).
    pub async fn domains_in_force(&self) -> Result<Vec<graph_catalog::def::Domain>, GraphError> {
        match &self.domains {
            Some(DomainSource::Remote(r)) => Ok(r.domains().await?.as_ref().clone()),
            Some(DomainSource::Local(d)) => Box::pin(d.domains_in_force()).await,
            None => {
                let tx = self.store.reader().await.map_err(GraphError::internal)?;
                Ok(in_force(&tx.domain_records().await?))
            }
        }
    }

    /// The catalogue of the domains in force in this store (the domain registry's: `Service.Types`).
    async fn stored_catalog(&self, tx: &Session) -> Result<Arc<Catalog>, GraphError> {
        let fp = tx.domain_fingerprint().await?;
        let mut cur = self.catalog.lock().await;
        if cur.0.as_deref() == Some(fp.as_str()) {
            return Ok(cur.1.clone());
        }
        let rs = tx.domain_records().await?;
        match Catalog::of(&in_force(&rs)) {
            Ok(c) => *cur = (Some(fp), Arc::new(c)),
            Err(e) => {
                tracing::error!(err = %e, "type catalogue");
                cur.0 = Some(fp);
            }
        }
        Ok(cur.1.clone())
    }
}

impl State {
    /// A graph over `store` reading its domains from a domain registry in this process, on a database in memory, the
    /// built-in domains of `builtins` seeded (tests and tools): the graph's state, the registry's.
    pub async fn with_local_domains(store: Store, builtins: &std::path::Path) -> Result<(State, Arc<State>), String> {
        let ds = Store::open_domains(&graph_store::StoreConfig::Memory)
            .await
            .map_err(|e| e.to_string())?;
        let registry = Arc::new(State::new(ds));
        Domains {
            state: registry.clone(),
        }
        .seed_builtin_domains(builtins)
        .await?;
        let mut state = State::new(store);
        state.domains = Some(DomainSource::Local(registry.clone()));
        Ok((state, registry))
    }
}

/// Where a graph reads its domains (ADR 0167).
pub enum DomainSource {
    /// The domain registry at `GOAP_DOMAINS_URL`.
    Remote(Arc<platform::Registry>),
    /// A domain registry in this process (tests and tools).
    Local(Arc<State>),
}

impl State {
    /// A read-only session for the reads of a request, asking the guardian of the changes; in a content graph, with
    /// what the platform graph holds of the organisation now.
    pub async fn reader(&self) -> Result<Session, GraphError> {
        let s = self.store.reader().await.map_err(GraphError::internal)?;
        self.with_platform(s).await
    }

    /// A write transaction on the writer, as [`State::reader`].
    pub async fn writer(&self) -> Result<Session, GraphError> {
        let s = self.store.writer().await.map_err(GraphError::internal)?;
        self.with_platform(s).await
    }

    /// The session with the catalogue of the domains in force, and what the platform graph holds when it is read
    /// from afar.
    async fn with_platform(&self, s: Session) -> Result<Session, GraphError> {
        let cat = self.catalog(&s).await?;
        let s = s.with_catalog(cat.clone()).with_guardian(self.guardian.clone());
        let Some(r) = &self.remote else { return Ok(s) };
        Ok(s.with_platform(Some(r.platform(&cat).await?)))
    }
}

/// The relays of the outboxes this process writes.
struct Relays {
    graph: Option<Arc<relay::Relay>>,
    domains: Option<Arc<relay::Relay>>,
}

/// `goap.registry.v1.DomainService`: the domain registry (ADR 0125 §3, a service of its own since ADR 0167).
pub struct Domains {
    pub state: Arc<State>,
}

/// `goap.smarttag.v1.SmartTagService` (ADR 0143 §7).
pub struct SmartTags {
    pub state: Arc<State>,
}

/// `goap.importer.v1.ImportService` (ADR 0148).
pub struct Imports {
    pub state: Arc<State>,
}

/// The routes of a content graph (ADR 0154 §9): the graph service alone (the jobs, the smart tags and the imports are the
/// platform graph's), `/healthz` and `/readyz`.
pub fn content_app(state: Arc<State>) -> Router {
    let ready = state.clone();
    Router::new()
        .merge(graphpb::graph_service_router(
            Arc::new(Graph { state }),
            Interceptors::none(),
        ))
        .route("/healthz", get(|| async { "ok" }))
        .route("/readyz", get(move || readiness(ready.clone())))
}

async fn readiness(s: Arc<State>) -> axum::response::Response {
    match s.store.ping().await {
        Ok(()) => (StatusCode::OK, "ready".to_string()).into_response(),
        Err(e) => (StatusCode::SERVICE_UNAVAILABLE, format!("database: {e}")).into_response(),
    }
}

/// The routes of the domain registry (ADR 0167): `DomainService`, `/healthz` and `/readyz`.
pub fn domains_app(state: Arc<State>) -> Router {
    let ready = state.clone();
    Router::new()
        .merge(registrypb::domain_service_router(
            Arc::new(Domains { state }),
            Interceptors::none(),
        ))
        .route("/healthz", get(|| async { "ok" }))
        .route("/readyz", get(move || readiness(ready.clone())))
}

/// Logs each request with the statements it ran on its database (`goap::rpc` at debug: `RUST_LOG=goap::rpc=debug`),
/// the number to watch for a call whose round trips grow with its batch.
struct Counted;

#[goap_connect::async_trait]
impl goap_connect::Interceptor for Counted {
    async fn unary(
        &self,
        ctx: goap_connect::Ctx,
        req: goap_connect::AnyRequest,
        next: goap_connect::Next<'_>,
    ) -> Result<goap_connect::AnyResponse, goap_connect::ConnectError> {
        if !tracing::enabled!(target: "goap::rpc", tracing::Level::DEBUG) {
            return next.run(ctx, req).await;
        }
        let procedure = req.spec.procedure.clone();
        let start = std::time::Instant::now();
        let texts = tracing::enabled!(target: "goap::rpc", tracing::Level::TRACE);
        let (out, n) = graph_store::stats::counted(next.run(ctx, req), texts).await;
        if let Some(texts) = &n.texts {
            tracing::trace!(target: "goap::rpc", procedure, statements = ?texts);
        }
        tracing::debug!(
            target: "goap::rpc",
            procedure,
            statements = n.statements,
            trips = n.trips,
            rows = n.rows,
            ms = start.elapsed().as_secs_f64() * 1000.0,
            ok = out.is_ok(),
        );
        out
    }
}

/// The routes of the graph service: its three services, `/healthz` and `/readyz` (the database answers).
pub fn app(state: Arc<State>) -> Router {
    let ics = Interceptors::none().with(Counted);
    let ready = state.clone();
    Router::new()
        .merge(
            graphpb::graph_service_router(Arc::new(Graph { state: state.clone() }), ics.clone()).layer(
                axum::middleware::from_fn_with_state(
                    Arc::new(route::Router::new(Arc::new(route::Declared::new(state.clone())))),
                    route::route,
                ),
            ),
        )
        .merge(smarttagpb::smart_tag_service_router(
            Arc::new(SmartTags { state: state.clone() }),
            ics.clone(),
        ))
        .merge(importerpb::import_service_router(Arc::new(Imports { state }), ics))
        .route("/healthz", get(|| async { "ok" }))
        .route("/readyz", get(move || readiness(ready.clone())))
}
