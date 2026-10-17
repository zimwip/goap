//! Port of `mcp/connectors/localfs` (see docs/rust-port.md).
//!
//! Package localfs is the reference connector: it exposes a directory of the local file system. The root directory
//! is configured per organization ("root"), and every path is confined under it, symbolic links included.

mod localfs;

pub use localfs::*;
