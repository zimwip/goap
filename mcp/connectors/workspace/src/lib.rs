//! Port of `mcp/connectors/workspace` (see docs/rust-port.md).
//!
//! Package workspace is the connector of the workspace MCP on the local machine (ADR 0118): one directory per change
//! under a root the unit configures, where the agents of the change read, write, edit and search files, run commands
//! and work on the sources they cloned, the way a coding agent works on a checkout.
//!
//! The file operations are confined to the directory of the workspace (a `cap_std::fs::Dir`, Go's `os.Root`,
//! symbolic links included). The commands are not: they run with the workspace as working directory, a cleaned
//! environment, a timeout and their whole process group killed at the end, but nothing stops a command from reaching
//! outside the directory. This connector is for development and trusted hosts; an isolated workspace is a container
//! connector.
//!
//! The workspace is the one of the change the call runs for (`goap_mcp::CallContext`, carried by
//! `InvokeRequest.change_id`): the tools name no workspace.

mod docker;
mod files;
mod git;
mod proc;
mod runner;
mod shell;
mod workspace;

pub use docker::*;
pub use files::*;
pub use shell::*;
pub use workspace::*;

#[cfg(test)]
mod docker_test;
#[cfg(test)]
mod workspace_test;
