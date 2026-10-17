//! The two-axis knowledge graph used by GOAP.
//!
//! The domain axis holds versioned content nodes linked version-to-version and grouped into baselines. The change
//! axis holds Changes: the description of a modification starting from a reference baseline (impacts) and proposing
//! the target graph (proposals). A Change is the blackboard of an agent process.
//!
//! Naming: the Go constants keep their name in SCREAMING_SNAKE (`TypeArtifact` → `TYPE_ARTIFACT`, `ChangeDraft` →
//! `CHANGE_DRAFT`); the Go string types (`ChangeID`, `ItemType`, `ChangeStatus`, ...) are string newtypes whose
//! constants are values of the type; a field named `Type` is `r#type`.

mod ids;

mod blackboard;
mod change;
mod changeimpact;
mod changelog;
mod decision;
mod dependency;
mod domain;
mod draft;
mod draftdiff;
mod draftmerge;
mod error;
mod flow;
mod hierarchy;
mod impactevent;
mod index;
mod itemtypes;
mod lifecycle;
mod review;
mod reviewrecord;
mod serde_util;
mod structure;
mod transition;
mod typeref;
mod validate;

pub mod gotime;

pub use blackboard::*;
pub use change::*;
pub use changeimpact::*;
pub use changelog::*;
pub use decision::*;
pub use dependency::*;
pub use domain::*;
pub use draft::{Draft, DraftLink, apply_draft_event, draft_ref, draft_seen_by, fold_drafts};
pub use draftdiff::*;
pub use draftmerge::*;
pub use error::*;
pub use flow::{
    FLOW_ACTIVATE_OP, FLOW_ADOPT_OP, FLOW_ADOPTED, FLOW_DEACTIVATE_OP, FLOW_DISCARD_OP,
    FLOW_DISCARDED, FLOW_EVALUATE_OP, FLOW_OPEN, FLOW_OPEN_OP, Flow, FlowCache, FlowEvent,
    FlowStatus, INTENT_DERIVE, INTENT_REFINE, INTENT_REVISE, MAIN_FLOW, OPTION_EVALUATED,
    OPTION_EXPLORING, OPTION_REJECTED, OPTION_SELECTED, OptionIntent, OptionSpec, stale_closure,
    valid_option_intent,
};
pub use hierarchy::*;
pub use impactevent::*;
pub use index::*;
pub use itemtypes::*;
pub use lifecycle::*;
pub use review::*;
pub use reviewrecord::{
    REVIEW_OUTCOME_ACCEPT, REVIEW_OUTCOME_REJECT, REVIEW_STATUS_DISCARDED, REVIEW_STATUS_OPEN,
    REVIEW_STATUS_SUBMITTED, ReviewEntry, ReviewRecord, TYPE_REVIEW, review_entries_of,
    review_outcome, review_record_of, valid_review_status,
};
pub use serde_util::json_equal;
pub use structure::*;
pub use transition::*;
pub use typeref::*;
pub use validate::*;
