//! Views (ADR 0157): every read of the current graph names what it reads, a lasting branch (at a baseline, else its
//! head) or a change on a flow.
use std::fmt;

use crate::{
    error::{GraphError, Kind},
    model::{BaselineId, ChangeId},
};

/// The view of a read, as the protocol names it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct View {
    pub namespace: String,
    pub branch: String,
    pub baseline: BaselineId,
    pub change: ChangeId,
    pub flow: String,
}

/// `graphapi.ErrNoView`.
pub const NO_VIEW: &str = "no view: name a branch (its namespace and its name) and optionally a baseline, or a change and its option: invalid";

impl View {
    pub fn branch(namespace: &str, branch: &str) -> View {
        View {
            namespace: namespace.into(),
            branch: branch.into(),
            ..View::default()
        }
    }

    pub fn is_change(&self) -> bool {
        !self.change.is_empty()
    }

    /// Refuses a view naming neither a change nor a branch (its namespace and its name), or both.
    pub fn check(&self) -> Result<(), GraphError> {
        if self.is_change() && (!self.namespace.is_empty() || !self.branch.is_empty() || !self.baseline.is_empty()) {
            return Err(GraphError::invalid("a view names a change or a branch, not both"));
        }
        if !self.is_change() && (self.namespace.is_empty() || self.branch.is_empty()) {
            return Err(GraphError::new(Kind::Invalid, NO_VIEW));
        }
        Ok(())
    }
}

impl fmt::Display for View {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match (self.is_change(), self.flow.is_empty(), self.baseline.is_empty()) {
            (true, false, _) => write!(f, "change {} on {}", self.change, self.flow),
            (true, true, _) => write!(f, "change {}", self.change),
            (false, _, false) => write!(f, "{}/{} at {}", self.namespace, self.branch, self.baseline),
            (false, _, true) => write!(f, "{}/{}", self.namespace, self.branch),
        }
    }
}
