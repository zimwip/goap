//! Dependencies between changes (ADR 0108): a change may depend on others, whose work must have reached the branch it
//! merges into before it merges. The relation is stored on the dependent change (Change.depends_on); it orders the
//! merges into an integrator (dependency_order) and is shown to the guards of the change (FACET_DEPENDENCIES).

use std::collections::HashSet;

use serde::{Deserialize, Serialize};

use crate::{Change, ChangeID, ChangeStatus};

/// Dependency is a change another change depends on (or, read the other way, depends on it), as seen from that other
/// change.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Dependency {
    pub id: ChangeID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    pub status: ChangeStatus,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub state: String,
    #[serde(rename = "parentId", skip_serializing_if = "ChangeID::is_empty")]
    pub parent_id: ChangeID,
    /// Satisfied: the work of the dependency has reached the branch the dependent change merges into (main, every
    /// change of its chain applied; or the dependent's parent, the dependency merged into it).
    pub satisfied: bool,
    /// Reason says why it is not satisfied.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
}

/// DependencyOrder is the order in which changes merge given their dependencies: each after the changes of the list
/// it depends on, ties (and a cycle, which the graph never stores) by creation then id. Dependencies outside the list
/// are ignored.
pub fn dependency_order(cs: &[Change]) -> Vec<Change> {
    let inside: HashSet<&ChangeID> = cs.iter().map(|c| &c.id).collect();
    let mut rest: Vec<&Change> = cs.iter().collect();
    rest.sort_by(|a, b| {
        a.created_at
            .cmp(&b.created_at)
            .then_with(|| a.id.cmp(&b.id))
    });
    let mut done: HashSet<ChangeID> = HashSet::with_capacity(cs.len());
    let mut out = Vec::with_capacity(cs.len());
    while !rest.is_empty() {
        let i = rest
            .iter()
            .position(|c| {
                c.depends_on
                    .iter()
                    .all(|d| !inside.contains(d) || done.contains(d))
            })
            .unwrap_or(0); // a cycle: the oldest first
        let c = rest.remove(i);
        done.insert(c.id.clone());
        out.push(c.clone());
    }
    out
}
