//! The type catalogue (ADR 0012 §2): the resolved model of every node type and link type of the domains in force and
//! of the built-in domains, keyed by their qualified reference ("<namespace>@<name>").
//!
//! The registry is the reference of the types; the services that judge or use nodes (graph, engine) hold a catalogue
//! built from the published domains it serves, and replace it when a domain is published. A catalogue is immutable
//! (`Arc<Catalog>`); [`Live`] holds the one in force of a service.
//!
//! Port of `lib/typecat` (see docs/rust-port.md).

mod typecat;

pub use typecat::*;
