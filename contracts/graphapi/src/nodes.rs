//! The node operations of a change (ADR 0076, 0077, 0079).

use std::{fmt, sync::Arc};

use goap_domain::{ChangeImpact, ChangeImpactID, ItemID, NodeID, NodeRef};
use serde_json::{Map, Value};

use crate::Error;

/// NodeCreate is a node ImpactNodeCreate creates.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct NodeCreate {
    pub key: String,
    pub r#type: String,
    pub properties: Map<String, Value>,
    /// Owner is the key of the organisational unit owning the node (ADR 0054); empty: the unit holding the change.
    pub owner: String,
    pub rationale: String,
    /// Links are outgoing links of the draft.
    pub links: Vec<LinkWrite>,
    /// Flow is the flow the call works on ("": the active option, else the main flow; domain::MAIN_FLOW names the
    /// main flow) and Execution the action run that makes it (ADR 0025).
    pub flow: String,
    pub execution: String,
    pub produced_by: String,
    pub derived_from: Vec<ItemID>,
}

/// NodeCheckout names the node ImpactNodeCheckout or ImpactNodeTransition works on: a change impact, or a node (then
/// the impact the flow sees on it, declared when the change holds none).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct NodeCheckout {
    pub impact: ChangeImpactID,
    pub node: NodeID,
    /// Key names the node by its key, when neither impact nor node is given.
    pub key: String,
    /// Rationale says why, when the call declares the impact.
    pub rationale: String,
    pub flow: String,
    pub execution: String,
    pub produced_by: String,
}

/// NodeTransition moves a node along its lifecycle (ImpactNodeTransition): to is the state it goes to. It derefs to
/// its checkout (Go embeds it).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct NodeTransition {
    pub checkout: NodeCheckout,
    pub to: String,
}

impl std::ops::Deref for NodeTransition {
    type Target = NodeCheckout;
    fn deref(&self) -> &NodeCheckout {
        &self.checkout
    }
}

impl std::ops::DerefMut for NodeTransition {
    fn deref_mut(&mut self) -> &mut NodeCheckout {
        &mut self.checkout
    }
}

/// NodeUpdate edits a draft.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct NodeUpdate {
    /// Node and Key name the node when ImpactNodeUpdate is given no change impact (see resolve).
    pub node: NodeID,
    pub key: String,
    /// Properties are merged over the ones of the draft.
    pub properties: Map<String, Value>,
    /// Owner transfers the node to another organisational unit (its key, ADR 0054).
    pub owner: String,
    pub flow: String,
    pub execution: String,
}

/// LinkWrite is an outgoing link of a draft: to an exact node version, or, with version 0, to the draft of a node the
/// change holds (resolved to the version landing writes for it).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct LinkWrite {
    pub r#type: String,
    pub to: NodeRef,
    pub properties: Map<String, Value>,
}

/// NodeName names a node a merge or a split works on: a change impact, or a node (its id, or its key).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct NodeName {
    pub impact: ChangeImpactID,
    pub node: NodeID,
    pub key: String,
}

/// Gate is asked about the type of every node a merge or a split writes or modifies, the parents it discovers
/// included (the access nodes, ADR 0068, are not the caller's to change): its error refuses the call. It runs inside
/// the transaction and must not read the graph.
pub type Gate = Arc<dyn Fn(&str) -> Result<(), Error> + Send + Sync>;

/// MergeInput is a merge (ImpactNodeMerge): sources are replaced, in their parents, by the new node into.
#[derive(Clone, Default)]
pub struct MergeInput {
    pub sources: Vec<NodeName>,
    pub into: NodeCreate,
    /// Rationale says why, on the impacts the merge declares (the sources, the parents); empty: the one of into.
    pub rationale: String,
    /// Flow and Execution replace the ones of into (see NodeCreate).
    pub flow: String,
    pub execution: String,
    /// Gate, when set, is asked about the type of every node the call writes or modifies (see [`Gate`]).
    pub gate: Option<Gate>,
}

/// SplitInput is a split (ImpactNodeSplit): source is replaced, in its parents, by the new nodes into.
#[derive(Clone, Default)]
pub struct SplitInput {
    pub source: NodeName,
    pub into: Vec<NodeCreate>,
    pub rationale: String,
    pub flow: String,
    pub execution: String,
    /// Gate is the one of MergeInput.
    pub gate: Option<Gate>,
}

impl fmt::Debug for MergeInput {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("MergeInput")
            .field("sources", &self.sources)
            .field("into", &self.into)
            .field("rationale", &self.rationale)
            .field("flow", &self.flow)
            .field("execution", &self.execution)
            .field("gate", &self.gate.is_some())
            .finish()
    }
}

impl fmt::Debug for SplitInput {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("SplitInput")
            .field("source", &self.source)
            .field("into", &self.into)
            .field("rationale", &self.rationale)
            .field("flow", &self.flow)
            .field("execution", &self.execution)
            .field("gate", &self.gate.is_some())
            .finish()
    }
}

/// SuspectLink is a link of another node to a source that a split leaves as it is: which successor it should follow
/// is for the reviewer to say (ADR 0003: it becomes suspect).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SuspectLink {
    pub from: NodeRef,
    pub from_key: String,
    pub r#type: String,
    pub to: NodeRef,
    pub to_key: String,
}

/// Restructured is what a merge or a split did to the change.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Restructured {
    /// Successors are the impacts of the new nodes (created, checked out, with their origins).
    pub successors: Vec<ChangeImpact>,
    /// Sources are the impacts of the merged or split nodes, via the impact of their parent.
    pub sources: Vec<ChangeImpact>,
    /// Parents are the impacts of the nodes whose links moved (each checked out): the parents, and for a merge the
    /// nodes holding other links to the sources.
    pub parents: Vec<ChangeImpact>,
    /// Suspect lists the links a split leaves to its source (see SuspectLink).
    pub suspect: Vec<SuspectLink>,
}
