//! The Rust graph service (ADR 0163): the services the Go graph process serves (`GraphService`, `DomainService`,
//! `SmartTagService`, `ImportService`), on the same database. A method not written yet answers `unimplemented`
//! (the generated default) before anything else, so that `goap-proxy` in mode split replays it on the Go service.
use std::sync::Arc;

use axum::{Router, http::StatusCode, response::IntoResponse, routing::get};
use goap_connect::Interceptors;
use goap_proto::{
    graph::v1 as graphpb, importer::v1 as importerpb, registry::v1 as registrypb, smarttag::v1 as smarttagpb,
};
use graph_catalog::{Catalog, catalog::is_builtin, record::in_force};
use graph_core::GraphError;
use graph_store::{Store, read::Session};
use tokio::sync::Mutex;

mod convert;
mod domainpb;
mod domains;
mod domainyaml;
pub mod graph;
mod journal;
pub mod platform;
pub mod relay;
pub mod route;
mod smarttags;

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
    /// The platform graph, in a content graph (`GOAP_GRAPH_SPACE=content`, ADR 0154 §9): its registry gives the
    /// domains, its organisation and its methodologies are read there.
    pub remote: Option<Arc<platform::Remote>>,
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
            remote: None,
            catalog: Mutex::new((None, Catalog::builtin())),
            users: Mutex::new(Default::default()),
            ensuring: Mutex::new(()),
            settings: Mutex::new(Default::default()),
        }
    }

    /// Writes the events in the outboxes and delivers them to `sink` (Go's `outbox.Start` of the graph and of the domain
    /// registry): returns the relays, for [`relay::Relay::run`].
    pub fn relay(&mut self, sink: relay::Sink) -> [Arc<relay::Relay>; 2] {
        let sink = Arc::new(sink);
        let r = Relays {
            graph: Arc::new(relay::Relay::new("graph", "event_outbox", sink.clone())),
            domains: Arc::new(relay::Relay::new("domains", "domain_outbox", sink)),
        };
        self.outbox = true;
        let out = [r.graph.clone(), r.domains.clone()];
        self.relays = Some(r);
        out
    }

    /// The bus the graph's events go to, when this process delivers them.
    pub(crate) fn sink(&self) -> Option<Arc<relay::Sink>> {
        self.relays.as_ref().map(|r| r.graph.sink())
    }

    /// Delivers the graph's events a commit wrote, at once.
    pub(crate) async fn deliver_graph(&self) {
        if let Some(r) = &self.relays {
            r.graph.after_commit(&self.store).await;
        }
    }

    /// Delivers the domain registry's events a commit wrote, at once.
    pub(crate) async fn deliver_domains(&self) {
        if let Some(r) = &self.relays {
            r.domains.after_commit(&self.store).await;
        }
    }

    /// The type catalogue of the domains in force (`typecat.Live`), built again when a stored version changed: a
    /// version published through the Go registry reaches the next write here. Domains that do not make a catalogue
    /// leave the previous one in force, as `Live.Reload` does.
    pub async fn catalog(&self, tx: &Session) -> Result<Arc<Catalog>, GraphError> {
        if let Some(r) = &self.remote {
            let mut cur = self.catalog.lock().await;
            let c = r.catalog(cur.1.clone()).await?;
            cur.1 = c.clone();
            return Ok(c);
        }
        let fp = tx.domain_fingerprint().await?;
        let mut cur = self.catalog.lock().await;
        if cur.0.as_deref() == Some(fp.as_str()) {
            return Ok(cur.1.clone());
        }
        let rs = tx.domain_records().await?;
        let ds: Vec<_> = in_force(&rs).into_iter().filter(|d| !is_builtin(&d.name)).collect();
        match Catalog::new(&ds) {
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
    /// A read-only session for the reads of a request; in a content graph, with what the platform graph holds of the
    /// organisation and the methodologies now.
    pub async fn reader(&self) -> Result<Session, GraphError> {
        let s = self.store.reader().await.map_err(GraphError::internal)?;
        self.with_platform(s).await
    }

    /// A write transaction on the writer, as [`State::reader`].
    pub async fn writer(&self) -> Result<Session, GraphError> {
        let s = self.store.writer().await.map_err(GraphError::internal)?;
        self.with_platform(s).await
    }

    async fn with_platform(&self, s: Session) -> Result<Session, GraphError> {
        let Some(r) = &self.remote else { return Ok(s) };
        let cat = self.catalog(&s).await?;
        Ok(s.with_platform(Some(r.platform(&cat).await?)))
    }
}

/// The relays of the outboxes this process writes.
struct Relays {
    graph: Arc<relay::Relay>,
    domains: Arc<relay::Relay>,
}

/// `goap.registry.v1.DomainService`: the domain registry the graph serves (ADR 0125 §3).
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

/// The routes of a content graph (ADR 0154 §9): the graph service alone (the jobs, the domain registry, the smart tags
/// and the imports are the platform graph's), `/healthz` and `/readyz`.
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

/// The routes of the service: its four services, `/healthz` and `/readyz` (the database answers).
pub fn app(state: Arc<State>) -> Router {
    let ics = Interceptors::none();
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
        .merge(registrypb::domain_service_router(
            Arc::new(Domains { state: state.clone() }),
            ics.clone(),
        ))
        .merge(smarttagpb::smart_tag_service_router(
            Arc::new(SmartTags { state: state.clone() }),
            ics.clone(),
        ))
        .merge(importerpb::import_service_router(Arc::new(Imports { state }), ics))
        .route("/healthz", get(|| async { "ok" }))
        .route("/readyz", get(move || readiness(ready.clone())))
}
