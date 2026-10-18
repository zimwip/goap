//! The requests and answers of the graph's operations are its contract, `goap_graphapi`; these names
//! keep the graph's own code and its tests reading as before.

use goap_domain::{Change, Node};

pub use goap_graphapi::{
    ChangeDependencies, ChangePatch, ChangesFilter, Commit, CommitResult, ImpactConflicts,
    LinkEdit, LinkWrite, MergeCandidate, MergeInput, MergePlan, NewChange, NodeCheckout,
    NodeCreate, NodeEdit, NodeName, NodeTransition, NodeUpdate, NodeValidator, OpenDecisionRequest,
    OpenFlowRequest, OpenOptionRequest, OptionComparison, OptionNode, RebaseResult, RebaseState,
    RebasedImpact, Resolution, Restructured, RuleRequest, SplitInput, SuspectLink,
    TransitionRequest, ValidatedNode, ValidatorQuery,
};

/// IsWorking is `goap_graphapi::is_working`.
pub fn is_working(c: &Change, flow: &str, n: &Node) -> bool {
    goap_graphapi::is_working(c, flow, n)
}

/// The branch a flow's versions would be written on (`graphapi.DraftBranch`).
pub(crate) fn draft_branch(c: &Change, flow: &str) -> String {
    goap_graphapi::draft_branch(c, flow)
}

/// The branch of a flow other than the main one (`graphapi.FlowBranch`).
pub(crate) fn flow_branch_name(flow: &str) -> String {
    goap_graphapi::flow_branch(flow)
}
