//! Command connector-localfs is the local file system connector: a standalone service that registers itself with the
//! MCP hub (see goap-connectorkit). GOAP_LOCALFS_ROOTS (comma separated) restricts the directories an organization
//! may bind.
use std::sync::Arc;

use goap_connector_localfs::Connector;

/// The comma separated list of a variable, none when it is unset or empty (Go's `strings.Split` of a non-empty
/// value: every element kept, empty ones included).
fn roots(var: &str) -> Vec<String> {
    match std::env::var(var) {
        Ok(v) if !v.is_empty() => v.split(',').map(str::to_string).collect(),
        _ => Vec::new(),
    }
}

#[tokio::main]
async fn main() {
    let c = Connector {
        roots: roots("GOAP_LOCALFS_ROOTS"),
    };
    goap_connectorkit::run("connector-localfs", Arc::new(c)).await;
}
