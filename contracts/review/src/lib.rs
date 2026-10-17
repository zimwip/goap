//! Port of `contracts/review` (see docs/rust-port.md).
//!
//! The review object of a change (ADR 0080): a reviewer builds a review up, with a global comment and one entry per
//! change impact (its own comment and outcome), then submits it; the submission applies every entry as an ordinary
//! review of the impact, all of them or none. The review is an item of the change of the built-in kind
//! `domain::TYPE_REVIEW` (ADR 0105: every verdict on an impact is made by a submitted review, the graph writes the
//! record of the ones it makes itself), each version of the item the whole record of its data.key; [`reviews`] folds
//! the items, [`Service`] (service.rs) is the use case over a [`Port`] that builds a review up and submits it. The
//! graph and the domain know nothing of this crate: the record format is the domain's, the mechanism the graph's
//! (`domain::ReviewBatch`).
//!
//! Data of a review item: key, flow (the flow branch the entries are reviewed on, "" the main flow), comment (the
//! global comment), status (open, then submitted or discarded: both final), entries (`[{impact, comment, outcome}]`),
//! by (the author), submittedAt (RFC 3339, once submitted).

mod review;
mod service;

pub use review::*;
pub use service::*;
