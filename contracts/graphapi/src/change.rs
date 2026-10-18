//! The requests on the header of a change.

use goap_domain::{BaselineID, Change, ChangeID, ChangeStatus, Dependency, OptionIntent};
use serde_json::{Map, Value};

/// ChangeDependencies are the dependencies of a change around it (ADR 0108).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ChangeDependencies {
    /// DependsOn are the changes it depends on, as it sees them.
    pub depends_on: Vec<Dependency>,
    /// Dependents are the open changes depending on it, each as that change sees it.
    pub dependents: Vec<Dependency>,
    /// Order is the order in which its open sub-changes merge into it (domain::dependency_order), each with whether
    /// its own dependencies are satisfied.
    pub order: Vec<Dependency>,
}

/// NewChange describes a change to open.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct NewChange {
    pub title: String,
    pub intent: String,
    pub methodology: String,
    /// MethodologyVersion is the version of the methodology the change follows (ADR 0113). Empty: a sub-change of its
    /// parent's methodology takes its parent's version, any other change the latest published one
    /// (ChangeLifecycles.MethodologyVersion).
    pub methodology_version: String,
    /// Goal the change works towards (the goal of a methodology or a process of it). Empty: the main goal of the
    /// methodology (ChangeLifecycles.DefaultGoal, ADR 0096), a sub-change's parent goal.
    pub goal: String,
    /// Namespace the change acts on (default: domain::DEFAULT_NAMESPACE).
    pub namespace: String,
    pub baseline_id: BaselineID,
    /// Branch the change applies to (default main); it must be open. With own_branch it is the branch the change is
    /// finally merged into.
    pub branch: String,
    /// OwnBranch gives the change a branch of its own, named after it and forked from baseline_id: its versions live
    /// there until the change is merged.
    pub own_branch: bool,
    /// BranchIntent says why the own branch exists relative to its parent (derive/revise/refine, same vocabulary as
    /// an Option's); only meaningful with own_branch or parent_id. "" (the default) is domain::INTENT_DERIVE.
    pub branch_intent: OptionIntent,
    /// ParentID makes the change a sub-change of another one (see prepareSubChange).
    pub parent_id: ChangeID,
    /// OwnerOrg is the key of the unit responsible for the change (empty: the default organisation).
    pub owner_org: String,
    /// ProjectID is the key of the project this change acts in and its new nodes belong to (ADR 0039, 0091). A
    /// change cannot be created without one: a sub-change inherits it from its parent when unset (and must stay
    /// within the parent's project when set), any other change names it, and an empty one is refused with
    /// Error::Invalid. The edges resolve a caller's active project (empty: the root project), the graph does not.
    pub project_id: String,
    pub data: Map<String, Value>,
}

/// ChangesFilter narrows ListChanges; the default value matches every change.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ChangesFilter {
    pub namespace: String,
    pub owner_org: String,
    /// Status: draft, active, committed, applied, abandoned (empty: every status).
    pub status: Vec<ChangeStatus>,
}

/// ChangePatch updates mutable fields of a change header. None fields are kept. Title and Intent are the current
/// definition of the change; goap-change/reformulate is the only caller that sets them, after superseding the
/// previous definition as an "intent" item so the history is kept.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ChangePatch {
    pub title: Option<String>,
    pub intent: Option<String>,
    pub goal: Option<String>,
    pub status: Option<ChangeStatus>,
    /// Merged over the data of the change.
    pub data: Map<String, Value>,
}

impl ChangesFilter {
    /// Match reports whether a change satisfies the filter.
    pub fn matches(&self, c: &Change) -> bool {
        if !self.namespace.is_empty() && c.namespace != self.namespace {
            return false;
        }
        if !self.owner_org.is_empty() && c.owner_org != self.owner_org {
            return false;
        }
        if !self.status.is_empty() && !self.status.contains(&c.status) {
            return false;
        }
        true
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn filter_matches() {
        let c = Change {
            namespace: "alm".into(),
            owner_org: "ORG".into(),
            status: goap_domain::CHANGE_ACTIVE,
            ..Default::default()
        };
        assert!(ChangesFilter::default().matches(&c));
        assert!(
            ChangesFilter {
                namespace: "alm".into(),
                status: vec![goap_domain::CHANGE_DRAFT, goap_domain::CHANGE_ACTIVE],
                ..Default::default()
            }
            .matches(&c)
        );
        assert!(
            !ChangesFilter {
                owner_org: "X".into(),
                ..Default::default()
            }
            .matches(&c)
        );
        assert!(
            !ChangesFilter {
                status: vec![goap_domain::CHANGE_APPLIED],
                ..Default::default()
            }
            .matches(&c)
        );
    }
}
