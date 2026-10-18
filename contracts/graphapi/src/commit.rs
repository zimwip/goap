//! Commits of node edits, rebases and transitions of a change.

use goap_domain::{Baseline, BaselineID, ChangeID, ChangeImpactID, LinkID, NodeRef};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::serde_util::{is_false, null_default};

/// NodeEdit is one node created or modified by a Commit.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct NodeEdit {
    /// Key and Type name a created node (pre is None).
    pub key: String,
    pub r#type: String,
    /// Pre is the version a modified node starts from (in the reference baseline).
    pub pre: Option<NodeRef>,
    /// Props are merged over the current properties (a null value clears one).
    pub props: Map<String, Value>,
    /// State moves the node to a lifecycle state, after its other edits: a transition of its draft, the node checked
    /// out first when it has none (ImpactNodeTransition, ADR 0076, 0079): a retired entry is edited and restored in
    /// the same change.
    pub state: String,
    /// Owner transfers the node to another organisational unit (its key, ADR 0054); empty: unchanged, or the unit
    /// holding the commit for a created node.
    pub owner: String,
    /// Rationale says why; the title of the commit when empty.
    pub rationale: String,
    pub links: Vec<LinkEdit>,
    /// RemoveLinks are outgoing links of the pre version the new version leaves out (removing a child is a
    /// modification of its parent, ADR 0024 §4).
    pub remove_links: Vec<LinkID>,
}

/// LinkEdit is an outgoing link added by an edit: to an existing version (to) or to a node created by another edit of
/// the commit (to_key). Nodes created by the commit may link each other in any direction, cycles included: a link to
/// a node not written yet is added, in the same change, once it is.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct LinkEdit {
    pub r#type: String,
    pub to: Option<NodeRef>,
    pub to_key: String,
    pub props: Map<String, Value>,
}

/// Commit describes a change made of node edits, applied as one unit.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Commit {
    pub namespace: String,
    pub title: String,
    pub intent: String,
    pub methodology: String,
    pub data: Map<String, Value>,
    pub baseline: BaselineID,
    /// Branch the commit lands on (default main).
    pub branch: String,
    /// By is who accepts the edits: the producer.
    pub by: String,
    pub baseline_name: String,
    pub edits: Vec<NodeEdit>,
    /// OwnerOrg is the unit holding the commit and ProjectID the project it acts in (ADR 0054); empty: the root unit.
    /// The project is required (ADR 0091).
    pub owner_org: String,
    pub project_id: String,
}

/// CommitResult is the outcome of a Commit.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct CommitResult {
    pub change: ChangeID,
    pub baseline: Baseline,
}

/// RebasedImpact is what a rebase did to a change impact.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RebasedImpact {
    #[serde(rename = "impactId")]
    pub impact: ChangeImpactID,
    pub key: String,
    /// Changed is set when its review went back to proposed: the rebase left conflicts on a decided impact (a clean
    /// rebase keeps the reviews, ADR 0101 §2).
    pub changed: bool,
    /// Merged is set when the node was merged three-way with the target's version.
    #[serde(skip_serializing_if = "is_false")]
    pub merged: bool,
    /// Conflicts are the fields changed on both sides (domain::CONFLICT_PROP, ...), kept as the change had them.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub conflicts: Vec<String>,
}

/// RebaseResult is what RebaseChange did.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RebaseResult {
    /// Parent is the parent change of a sub-change, empty for a root change.
    #[serde(rename = "parentId")]
    pub parent: ChangeID,
    /// Branch is the target branch of a root change; From the baseline it was forked from and Onto the head it moved
    /// to (equal: it was not behind).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub branch: String,
    #[serde(skip_serializing_if = "BaselineID::is_empty")]
    pub from: BaselineID,
    #[serde(skip_serializing_if = "BaselineID::is_empty")]
    pub onto: BaselineID,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub impacts: Vec<RebasedImpact>,
}

/// TransitionRequest asks for a transition of the lifecycle of a change.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct TransitionRequest {
    /// Transition is the name of the transition out of the current state.
    pub transition: String,
    /// Decision is the decision point that gates the transition, when its guard asks for one: it is seen by the guard
    /// as change.decision, and is consumed by the move (a point gates one transition only).
    pub decision: String,
    /// By is the caller, recorded in the journal.
    pub by: String,
}

impl RebaseResult {
    /// Changed reports a rebase that left conflicts: the change has impacts to settle and review again.
    pub fn changed(&self) -> bool {
        self.impacts
            .iter()
            .any(|i| i.changed || !i.conflicts.is_empty())
    }

    /// Summary says what a rebase left to do: the impacts to review again, the conflicts to settle.
    pub fn summary(&self) -> String {
        let mut review = Vec::new();
        let mut conflicts = Vec::new();
        for i in &self.impacts {
            if i.changed || !i.conflicts.is_empty() {
                review.push(i.key.as_str());
            }
            for c in &i.conflicts {
                conflicts.push(format!("{} {c}", i.key));
            }
        }
        let mut out = format!(
            "rebased onto branch {}: review again {}",
            self.branch,
            review.join(", ")
        );
        if !conflicts.is_empty() {
            out.push_str("; settle the conflicts ");
            out.push_str(&conflicts.join(", "));
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rebase_result_summary_and_json() {
        let r = RebaseResult {
            branch: "main".into(),
            impacts: vec![
                RebasedImpact {
                    impact: "i1".into(),
                    key: "REQ-1".into(),
                    changed: true,
                    ..Default::default()
                },
                RebasedImpact {
                    impact: "i2".into(),
                    key: "REQ-2".into(),
                    conflicts: vec!["props.title".into()],
                    ..Default::default()
                },
                RebasedImpact {
                    impact: "i3".into(),
                    key: "REQ-3".into(),
                    ..Default::default()
                },
            ],
            ..Default::default()
        };
        assert!(r.changed());
        assert_eq!(
            r.summary(),
            "rebased onto branch main: review again REQ-1, REQ-2; settle the conflicts REQ-2 props.title"
        );
        assert!(!RebaseResult::default().changed());
        assert_eq!(
            serde_json::to_string(&RebaseResult::default()).unwrap(),
            r#"{"parentId":""}"#
        );
        assert_eq!(
            serde_json::to_string(&r.impacts[0]).unwrap(),
            r#"{"impactId":"i1","key":"REQ-1","changed":true}"#
        );
    }
}
