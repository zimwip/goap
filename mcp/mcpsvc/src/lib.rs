//!
//! Package mcpsvc is the MCP hub: the live registry of connectors and the resolution and invocation of tools for the
//! organisational unit holding a change. MCPs and adapters are nodes of the graph, read here (ADR 0019).
//!
//! - [`Service`]: registration and liveness of connectors, [`Service::check_adapter`], [`Service::template`],
//!   [`Service::effective`], [`Service::tools`] and [`Service::call`] (the adapter's code run by `goap-dsl` over the
//!   connector's operations, a model token minted for a connector that calls a model, ADR 0123 §6).
//! - [`Directory`] / [`Snapshot`]: the units, MCPs, adapter definitions and adapter instances of the heads of the
//!   organisation and platform namespaces; resolution along the unit chain, restrictions adding up.
//! - [`Store`]: [`MemoryStore`] and [`SqlStore`] with [`MIGRATIONS`] / [`SQLITE_MIGRATIONS`].
//! - [`Handler`]: the Connect `goap.mcp.v1.McpService` (`goap_gen::mcp::v1::mcp_service_router`), [`Client`] the
//!   engine side, [`HubPort`] the in-process one; [`ConnectInvoker`] / [`InprocInvoker`] reach the connectors.
//!
//! Differences from the Go package: the sentinels are variants of [`Error`] holding the whole message; Go's func
//! fields are traits ([`SecretResolver`]) or `Arc<dyn Fn>` ([`NowFn`]); nil-able fields are `Option`s (a missing
//! directory or invoker is an error instead of a nil dereference); `Snapshot::resolve` returns
//! `Option<(Instance, inherited)>`; `keep_registered` is async (the first registration is awaited); the `Graph`
//! trait is `goap_access::Graph` (implemented by every `goap_graphapi::Port`); `Client::call_tool` returns a
//! connector failure as `Error::Tool` (Go: a plain error with the same text).

mod client;
mod directory;
mod errors;
mod handler;
mod inproc;
mod invoker;
mod service;
mod sqlstore;
mod store;

pub use client::Client;
pub use directory::{Baselines, Directory, Effective, Graph, Snapshot, build_snapshot};
pub use errors::{
    ERR_ADAPTER_DEF, ERR_NOT_BOUND, ERR_NOT_FOUND, ERR_UNAVAILABLE, Error, ToolError,
};
pub use handler::{Handler, rpc_err};
pub use inproc::{HubPort, INPROC_SCHEME, InprocInvoker};
pub use invoker::{CONNECTOR_TOKEN_HEADER, ConnectInvoker, resolve_secret};
pub use service::{
    ConnectorView, DEFAULT_LEASE, DEFAULT_MODEL_TOKEN_TTL, Invoker, ModelEndpoint, NowFn,
    SecretError, SecretResolver, Service, Tool,
};
pub use sqlstore::{MIGRATIONS, SQLITE_MIGRATIONS, SqlStore};
pub use store::{ConnectorReg, MemoryStore, Store};
