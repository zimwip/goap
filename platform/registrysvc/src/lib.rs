//! The methodology registry (ADR 0023, 0125 §3).
//!
//! A [`Service`] over a [`Store`] ([`GraphStore`]: the versions as nodes of the graph; [`MemoryStore`]) reading the
//! domain registry through [`Domains`]; it validates, publishes and compiles the methodologies ([`activate`], the
//! compile cache, domain pins, suspension), and answers the graph's questions about the changes (lifecycles, guards,
//! gates, guardians, merge, operation roles, the sub-activity cascade). [`Handler`] serves it as the Connect
//! `registry.v1.RegistryService`; [`Client`] is the engine's `MethodologyPort` over that service; [`Drafts`] and
//! [`Client`] are the self-observation agent's `MethodologyDrafts`.

mod activitygoals;
mod aliasstubs;
mod changelifecycle;
mod client;
mod compilecache;
mod convert;
mod defs;
mod domains;
mod drafts;
mod error;
mod graphstore;
mod guardians;
mod handler;
mod refs;
mod scopes;
mod service;
mod steps;
mod store;
mod subactivity;
mod suspend;
mod versions;

#[cfg(test)]
mod tests;

use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_methodology::Compiled;

pub use activitygoals::{DATA_ACTIVITY, activity_of};
pub use client::Client;
pub use compilecache::activate;
pub use convert::{
    from_pb, levels_to_pb, methodology_view_to_pb, plan_preview_to_pb, process_graph_to_pb,
    summary_to_pb, to_pb,
};
pub use domains::Domains;
pub use drafts::Drafts;
pub use error::{
    BoxError, ERR_IMMUTABLE, ERR_INVALID, ERR_NOT_FOUND, ERR_SUSPENDED, Error, Result,
    SuspendedError,
};
pub use graphstore::{
    GraphStore, LINK_SUB_ACTIVITY, NAMESPACE_METHODOLOGY, StoreGraph, TYPE_METHODOLOGY_VERSION,
    methodology_version_key,
};
pub use handler::{Handler, to_connect};
pub use service::{
    ApplicableFn, BoxFuture, Clock, McpScopesFn, SeedError, Service, applicable_fn, mcp_scopes_fn,
};
pub use store::{
    MemoryStore, Record, STATUS_ARCHIVED, STATUS_DRAFT, STATUS_PUBLISHED, Status, Store, key,
};

#[async_trait]
impl goap_engine::MethodologyPort for Service {
    async fn methodology(&self, ctx: &Ctx, name: &str) -> goap_engine::Result<Arc<Compiled>> {
        Service::methodology(self, ctx, name)
            .await
            .map_err(Error::into_engine)
    }

    async fn list(&self, ctx: &Ctx) -> goap_engine::Result<Vec<Arc<Compiled>>> {
        Service::list(self, ctx).await.map_err(Error::into_engine)
    }
}
