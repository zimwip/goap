//! Package graphsnap reads a typed view of the graph at the head of the main branch, rebuilding it only
//! when the head moved. Services that keep configuration as graph nodes (access, models, ...) build their
//! snapshot with it.

mod graphsnap;

pub use graphsnap::*;
