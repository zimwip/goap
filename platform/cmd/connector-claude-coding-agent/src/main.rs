//! Command connector-claude-coding-agent is the connector of the claude-coding-agent MCP (ADR 0123): Claude Code
//! sessions, run by the Claude Agent SDK in the workspaces of the changes. A standalone service that registers itself
//! with the MCP hub (see goap-connectorkit); it runs on the host of the workspace connector (the sessions work in the
//! same directories), with Node.js and the toolchains the changes build with.
//!
//! GOAP_CLAUDE_AGENT_DIR (default .goap/claude-agent) is the directory of its runner; `connector-claude-coding-agent
//! install` writes the runner there and installs the Claude Agent SDK with npm (make claude-agent-runner).
//! GOAP_WORKSPACE_ROOTS (comma separated) restricts the roots a unit may configure, like the workspace connector's.
//! GOAP_NODE names the Node.js executable (default node).
use std::{path::Path, process::Command, sync::Arc};

use goap_connector_claudeagent::Connector;

/// The comma separated list of a variable, none when it is unset or empty.
fn roots(var: &str) -> Vec<String> {
    match std::env::var(var) {
        Ok(v) if !v.is_empty() => v.split(',').map(str::to_string).collect(),
        _ => Vec::new(),
    }
}

#[tokio::main]
async fn main() {
    let dir = goap_platform::env("GOAP_CLAUDE_AGENT_DIR", ".goap/claude-agent");
    if std::env::args().nth(1).as_deref() == Some("install") {
        if let Err(err) = install(Path::new(&dir)) {
            eprintln!("install: {err}");
            std::process::exit(1);
        }
        return;
    }
    let mut c = Connector::new(dir, roots("GOAP_WORKSPACE_ROOTS"));
    c.node = goap_platform::env("GOAP_NODE", "node");
    goap_connectorkit::run("connector-claude-coding-agent", Arc::new(c)).await;
}

/// install writes the runner into dir and installs its pinned Claude Agent SDK.
fn install(dir: &Path) -> Result<(), String> {
    goap_connector_claudeagent::install(dir).map_err(|e| e.to_string())?;
    let status = Command::new("npm")
        .args(["install", "--omit=dev", "--no-audit", "--no-fund"])
        .current_dir(dir)
        .status();
    let failed = match status {
        Ok(s) if s.success() => None,
        Ok(s) => Some(s.to_string()),
        Err(e) => Some(e.to_string()),
    };
    if let Some(e) = failed {
        return Err(format!("npm install in {}: {e}", dir.display()));
    }
    println!("Claude Agent SDK installed in {}", dir.display());
    Ok(())
}
