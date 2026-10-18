//!
//! Renders the state of a graph at a baseline as an OKF tree (ADR 0111): one Markdown file per node, its identity,
//! properties and links in a YAML front matter, its long texts and its links to other nodes in a Markdown body, and
//! an index per namespace. The snapshot comes from the graph (`goap_graph::Snapshot`, ADR 0104): no history, the
//! state of one baseline.
//!
//! API: [`write`]`(w: impl std::io::Write, &Snapshot, &Options, at) -> Result<(), Error>` streams the zip;
//! [`Options`] (`name`, `body`: optional closures); [`from_types`] / [`from_catalog`] derive them from the type
//! catalogue; the front matter keys `KEY_*`, [`PROPERTY_PREFIX`], [`FORMAT`], [`INDEX_FILE`], [`Index`],
//! [`LinkProperties`], [`BodyAttribute`].

mod okf;
mod types;

pub use okf::*;
pub use types::*;
