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
pub mod graph;
mod journal;
pub mod relay;
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
    /// The type catalogue of the domains in force, with what told the stored versions apart when it was built.
    catalog: Mutex<(Option<String>, Arc<Catalog>)>,
    /// The persons this process made sure have a user (Go's `EnsureCaller`), and the creation in progress.
    users: Mutex<std::collections::HashSet<String>>,
    ensuring: Mutex<()>,
    /// The changes this graph was found to hold, remembered as Go's router does (`Router.changeBase`): one purged since
    /// is this graph's still, which answers it is not found, rather than a change another graph may hold.
    pub(crate) held: Mutex<std::collections::HashSet<graph_core::model::ChangeId>>,
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
            catalog: Mutex::new((None, Catalog::builtin())),
            users: Mutex::new(Default::default()),
            ensuring: Mutex::new(()),
            held: Mutex::new(Default::default()),
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
    /// version published through the Go registry reaches the next write here.
    pub async fn catalog(&self, tx: &Session) -> Result<Arc<Catalog>, GraphError> {
        let fp = tx.domain_fingerprint().await?;
        let mut cur = self.catalog.lock().await;
        if cur.0.as_deref() == Some(fp.as_str()) {
            return Ok(cur.1.clone());
        }
        let rs = tx.domain_records().await?;
        let ds: Vec<_> = in_force(&rs).into_iter().filter(|d| !is_builtin(&d.name)).collect();
        let c = Catalog::new(&ds).map_err(|e| {
            GraphError::unimplemented(format!("the domains in force do not make a catalogue here: {e}"))
        })?;
        *cur = (Some(fp), Arc::new(c));
        Ok(cur.1.clone())
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

impl importerpb::ImportService for Imports {}

/// The routes of the service: its four services, `/healthz` and `/readyz` (the database answers).
pub fn app(state: Arc<State>) -> Router {
    let ics = Interceptors::none();
    let ready = state.clone();
    Router::new()
        .merge(graphpb::graph_service_router(
            Arc::new(Graph { state: state.clone() }),
            ics.clone(),
        ))
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
        .route(
            "/readyz",
            get(move || {
                let s = ready.clone();
                async move {
                    match s.store.ping().await {
                        Ok(()) => (StatusCode::OK, "ready".to_string()).into_response(),
                        Err(e) => (StatusCode::SERVICE_UNAVAILABLE, format!("database: {e}")).into_response(),
                    }
                }
            }),
        )
}
