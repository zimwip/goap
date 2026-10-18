//! Flows and options of a change.

use std::collections::BTreeMap;

use goap_domain::{
    Change, ChangeItem, Flow, ItemID, Node, NodeID, NodeRef, OptionIntent, branch_of,
};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::serde_util::null_default;

/// OpenFlowRequest relaunches a step of the action flow on a new flow branch.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct OpenFlowRequest {
    /// Parent is the flow the relaunched step ran on ("" = the main flow).
    pub parent: String,
    /// ForkAfter is the last item of the parent's view before the step ("" = none).
    pub fork_after: ItemID,
    /// Seeds are the items produced by the relaunched step and by what followed it; the items derived from them are
    /// invalidated too.
    pub seeds: Vec<ItemID>,
    /// StaleRuns are opaque producer ids (the Execution of change impacts and node versions) of the relaunched step
    /// and of what followed it: what they produced is stale until the branch is adopted (ADR 0025).
    pub stale_runs: Vec<String>,
    /// Origin is an opaque record the opener keeps on the flow (the graph stores and returns it, never reads it).
    pub origin: Map<String, Value>,
    /// Items are written on the new flow at creation (a note for whoever works on it): the graph gives each the flow,
    /// an id, a time and, unset, the accepted status.
    pub items: Vec<ChangeItem>,
}

/// IsWorking reports whether a node as a call of the change reads it is the draft of the flow itself (ADR 0077,
/// 0079): a draft (no version), whose branch is the one the flow names (flow "": the active option, else the main
/// flow). The draft of a parent flow is not: the flow checks the node out to write its own. Callers use it to decide
/// between a checkout and an update.
pub fn is_working(c: &Change, flow: &str, n: &Node) -> bool {
    n.is_draft() && branch_of(&n.branch) == draft_branch(c, &c.resolve_flow(flow))
}

/// OpenOptionRequest opens an option of a change.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct OpenOptionRequest {
    pub name: String,
    pub hypothesis: String,
    /// Parent is the option this one is a sub-branch of ("" = forked from the main flow). Non-empty only makes sense
    /// with intent refine: isolating narrower work within an already-open option.
    pub parent: String,
    /// Intent says why the option exists relative to parent (derive, revise or refine; "" = unspecified).
    pub intent: OptionIntent,
    /// Activate makes the new option the one the change works on.
    pub activate: bool,
    pub by: String,
}

/// OptionNode is a node written by at least one option, with its version in the main flow and in each option (None:
/// not in the graph of that side, absent or retired; version 0: the draft that side holds, ADR 0079).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct OptionNode {
    pub node: NodeID,
    pub key: String,
    pub r#type: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub main: Option<NodeRef>,
    #[serde(deserialize_with = "null_default")]
    pub options: BTreeMap<String, Option<NodeRef>>,
    /// Props are the properties of the node on each side: "main" or the option id.
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub props: BTreeMap<String, Map<String, Value>>,
}

/// OptionComparison compares the open options of a change at a level (written or accepted): the nodes any of them
/// changed from the main flow, both sides forking from the same graph (ADR 0032 §6).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct OptionComparison {
    pub level: String,
    #[serde(deserialize_with = "null_default")]
    pub options: Vec<Flow>,
    #[serde(deserialize_with = "null_default")]
    pub nodes: Vec<OptionNode>,
}

/// DraftBranch names the branch a flow's versions would be written on: the change branch for the main flow. It is
/// the branch of the node view of a draft, which says whose draft it is (is_working); no branch of that name is
/// created.
pub fn draft_branch(c: &Change, flow: &str) -> String {
    if flow.is_empty() {
        return branch_of(&c.branch).to_string();
    }
    flow_branch(flow)
}

/// FlowBranch names the branch of a flow other than the main one.
pub fn flow_branch(flow: &str) -> String {
    let short = if flow.len() > 8 {
        flow.get(..8).unwrap_or(flow)
    } else {
        flow
    };
    format!("flow-{short}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn branches_of_flows() {
        assert_eq!(flow_branch("0123456789"), "flow-01234567");
        assert_eq!(flow_branch("abc"), "flow-abc");
        let c = Change::default();
        assert_eq!(draft_branch(&c, ""), "main");
        let draft = Node {
            branch: "main".into(),
            ..Default::default()
        };
        assert!(is_working(&c, "", &draft));
        assert!(is_working(&c, goap_domain::MAIN_FLOW, &draft));
        assert!(!is_working(&c, "f1", &draft));
        let version = Node {
            version: 1,
            ..Default::default()
        };
        assert!(!is_working(&c, "", &version));
    }
}
