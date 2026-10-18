//!
//! Package connectorkit is the toolkit to write a connector: a separately deployed service wrapping the API of a real
//! service and exposed to the platform through the MCP hub (ADR 0019).
//!
//! A connector implements [`Connector`] and calls [`run`] from its main:
//!
//! ```ignore
//! #[tokio::main]
//! async fn main() { goap_connectorkit::run("localfs", Arc::new(localfs::Connector::default())).await }
//! ```
//!
//! Run serves the goap.connector.v1.ConnectorService protocol and registers the connector with the hub, renewing the
//! registration as a heartbeat. The hub needs no configuration to learn about a new connector.
//!
//! Environment: GOAP_HTTP_ADDR (listen address), GOAP_MCP_URL (the hub), GOAP_CONNECTOR_URL (URL of this connector
//! as the hub reaches it; default http://<hostname><listen address>), GOAP_CONNECTOR_TOKEN (shared secret between hub
//! and connectors; when set, calls without it are refused).

mod connectorkit;

pub use connectorkit::*;
