//! Port of `contracts/graphapi` (see docs/rust-port.md): the contract of the graph for its callers, the requests and
//! answers of the operations on a change (`engine.GraphPort`, the built-in MCPs, the services) and the errors they
//! return ([`Error`]). It depends on `goap-domain` only, so a caller can name the graph's operations without importing
//! the graph and its storage (`platform/layering`).

mod change;
mod commit;
mod decision;
mod errors;
mod flow;
mod merge;
mod nodes;
mod port;
mod serde_util;
mod validator;

pub use change::*;
pub use commit::*;
pub use decision::*;
pub use errors::*;
pub use flow::*;
pub use merge::*;
pub use nodes::*;
pub use port::*;
pub use validator::*;
