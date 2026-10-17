//! Issues found in the content of a blackboard.

use serde::{Deserialize, Serialize};

use crate::ItemID;

/// Severity of a blackboard issue.
pub const ISSUE_ERROR: &str = "error";
pub const ISSUE_WARNING: &str = "warning";

/// BoardIssue is an inconsistency found in the content of a blackboard.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct BoardIssue {
    /// Item is the item where the problem shows.
    pub item: ItemID,
    /// Culprit is the item whose production is at fault: the item itself, or the item another one wrongly builds on
    /// (a relaunch restarts the step that produced the culprit).
    pub culprit: ItemID,
    /// Code: structure | dangling | derived_from_invalid | reference | outdated | rule | impact | duplicate.
    pub code: String,
    pub message: String,
    pub severity: String,
}
