//! Merges of branches and rebases of changes.

use goap_domain::{BaselineID, ChangeID, ChangeImpactID, NodeID, NodeRef};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::serde_util::{is_false, null_default};

/// MergeCandidate is a node changed on the source branch since its fork.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct MergeCandidate {
    pub node: NodeID,
    pub key: String,
    pub r#type: String,
    pub kind: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ancestor: Option<NodeRef>,
    /// Ours is the version on the target branch (None when added or deleted there).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ours: Option<NodeRef>,
    pub theirs: NodeRef,
    /// Deleted is set when the source branch deleted the node.
    #[serde(skip_serializing_if = "is_false")]
    pub deleted: bool,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub base: Map<String, Value>,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub ours_props: Map<String, Value>,
    /// TheirsProps and Merged are the source and proposed merged properties.
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub theirs_props: Map<String, Value>,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub merged: Map<String, Value>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub conflicts: Vec<String>,
}

/// MergePlan lists what merging a branch into another would do.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct MergePlan {
    pub from: String,
    pub into: String,
    pub into_head: BaselineID,
    #[serde(deserialize_with = "null_default")]
    pub candidates: Vec<MergeCandidate>,
}

/// Resolution resolves a merge conflict: props is the resolved property map (for a deletion conflict, any resolution
/// with skip unset applies the deletion); skip keeps the target version unchanged.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Resolution {
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub props: Map<String, Value>,
    #[serde(skip_serializing_if = "is_false")]
    pub skip: bool,
}

/// ImpactConflicts are the conflicts a rebase left on a change impact, not settled yet.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ImpactConflicts {
    #[serde(rename = "impactId")]
    pub impact: ChangeImpactID,
    pub key: String,
    #[serde(deserialize_with = "null_default")]
    pub conflicts: Vec<String>,
}

/// RebaseState is where a change stands against the branch it was forked from (ADR 0101, 0103): the change impacts
/// whose node that branch changed since the fork, a rebase bringing them up to date, and the conflicts left to settle.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RebaseState {
    #[serde(rename = "parentId")]
    pub parent: ChangeID,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub behind: Vec<ChangeImpactID>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub conflicts: Vec<ImpactConflicts>,
    /// Branch and Onto, for a change behind the head of the branch it was forked from: that branch and the head
    /// baseline a rebase moves it to (empty: not behind).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub branch: String,
    #[serde(skip_serializing_if = "BaselineID::is_empty")]
    pub onto: BaselineID,
}

impl MergePlan {
    /// Conflicting returns the candidates that need a resolution.
    pub fn conflicting(&self) -> Vec<MergeCandidate> {
        self.candidates
            .iter()
            .filter(|c| !c.conflicts.is_empty())
            .cloned()
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn conflicting_candidates() {
        let p = MergePlan {
            candidates: vec![
                MergeCandidate {
                    key: "A".into(),
                    ..Default::default()
                },
                MergeCandidate {
                    key: "B".into(),
                    conflicts: vec!["title".into()],
                    ..Default::default()
                },
            ],
            ..Default::default()
        };
        let c = p.conflicting();
        assert_eq!(c.len(), 1);
        assert_eq!(c[0].key, "B");
        let j = serde_json::to_value(&p.candidates[1]).unwrap();
        assert_eq!(
            j,
            serde_json::json!({"node": "", "key": "B", "type": "", "kind": "", "theirs": {"id": "", "version": 0}, "conflicts": ["title"]})
        );
    }
}
