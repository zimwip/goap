//! Command connector-workspace is the connector of the workspace MCP on the local machine (ADR 0118): one directory
//! per change, where the agents of the change work on files, commands and sources. A standalone service that
//! registers itself with the MCP hub (see goap-connectorkit); it runs on a host with git, a shell and the toolchains
//! the changes build with (not in the distroless image of the services).
//!
//! GOAP_WORKSPACE_ROOTS (comma separated) restricts the directories a unit may configure as its root. With
//! GOAP_WORKSPACE_DOCKER set to the address of a Docker Engine API (unix:///var/run/docker.sock, tcp://proxy:2375),
//! it is the connector workspace-docker instead: the commands of each workspace run in a container of its own (the
//! host then needs git only, the toolchains are in the image the unit configures).
use std::sync::Arc;

use goap_connector_workspace::Connector;

/// The comma separated list of a variable, none when it is unset or empty.
fn roots(var: &str) -> Vec<String> {
    match std::env::var(var) {
        Ok(v) if !v.is_empty() => v.split(',').map(str::to_string).collect(),
        _ => Vec::new(),
    }
}

#[tokio::main]
async fn main() {
    let roots = roots("GOAP_WORKSPACE_ROOTS");
    match std::env::var("GOAP_WORKSPACE_DOCKER") {
        Ok(host) if !host.is_empty() => {
            goap_connectorkit::run(
                "connector-workspace-docker",
                Arc::new(Connector::new_docker(&host, roots)),
            )
            .await
        }
        _ => goap_connectorkit::run("connector-workspace", Arc::new(Connector::new(roots))).await,
    }
}
