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
mod graph;
mod journal;
mod smarttags;

pub use graph::Graph;

/// What the handlers share.
pub struct State {
    pub store: Store,
    /// Whether a bus takes the events: written in the outbox, as the Go service does with `GOAP_NATS_URL` set (its
    /// relay delivers them for now), else none.
    pub outbox: bool,
    /// The type catalogue of the domains in force, with what told the stored versions apart when it was built.
    catalog: Mutex<(Option<String>, Arc<Catalog>)>,
}

impl State {
    pub fn new(store: Store) -> State {
        State {
            store,
            outbox: false,
            catalog: Mutex::new((None, Catalog::builtin())),
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
