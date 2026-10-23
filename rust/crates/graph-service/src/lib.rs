//! The Rust graph service (ADR 0163): the services the Go graph process serves (`GraphService`, `DomainService`,
//! `SmartTagService`, `ImportService`), on the same database. A method not written yet answers `unimplemented`
//! (the generated default) before anything else, so that `goap-proxy` in mode split replays it on the Go service.
use std::sync::Arc;

use axum::{Router, http::StatusCode, response::IntoResponse, routing::get};
use goap_connect::Interceptors;
use goap_proto::{
    graph::v1 as graphpb, importer::v1 as importerpb, registry::v1 as registrypb, smarttag::v1 as smarttagpb,
};
use graph_store::Store;

mod convert;
mod graph;

pub use graph::Graph;

/// What the handlers share.
pub struct State {
    pub store: Store,
}

/// `goap.registry.v1.DomainService`: the domain registry the graph serves (ADR 0125 §3).
pub struct Domains {
    pub state: Arc<State>,
}

impl registrypb::DomainService for Domains {}

/// `goap.smarttag.v1.SmartTagService` (ADR 0143 §7).
pub struct SmartTags {
    pub state: Arc<State>,
}

impl smarttagpb::SmartTagService for SmartTags {}

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
