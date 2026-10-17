//! The two-axis knowledge graph used by GOAP.
//!
//! The domain axis holds versioned content nodes linked version-to-version and grouped into baselines. The change
//! axis holds Changes: the description of a modification starting from a reference baseline (impacts) and proposing
//! the target graph (proposals). A Change is the blackboard of an agent process.

use std::{collections::BTreeMap, fmt};

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    ChangeID, ChangeImpactID, OptionIntent, gotime,
    ids::string_type,
    serde_util::{is_false, is_zero_i64, null_default},
};

string_type!(
    /// NodeID is the stable identity of a node across all its versions.
    NodeID
);

/// Version is a per-node monotonically increasing version number, starting at 1.
pub type Version = i64;

/// NodeRef points to an exact version of a node.
#[derive(Debug, Clone, Default, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(default)]
pub struct NodeRef {
    pub id: NodeID,
    pub version: Version,
}

impl NodeRef {
    /// A reference to a version of a node.
    pub fn new(id: impl Into<NodeID>, version: Version) -> Self {
        NodeRef {
            id: id.into(),
            version,
        }
    }

    /// IsZero reports whether the reference is unset.
    pub fn is_zero(&self) -> bool {
        self.id.is_empty()
    }
}

impl fmt::Display for NodeRef {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}@v{}", self.id, self.version)
    }
}

/// DefaultNamespace is the namespace of nodes and changes that name none (an untyped graph, in tests). With a type
/// catalogue (ADR 0012) no node lives there: a node lives in the namespace of its type, and a change names the
/// namespace it acts on.
pub const DEFAULT_NAMESPACE: &str = "default";

/// NamespacePlatform is the namespace of the platform configuration: the MCPs, the adapter definitions and the model
/// configuration (built-in domain platform).
pub const NAMESPACE_PLATFORM: &str = "platform";

/// NamespaceMethodology is the namespace of the meta-domain (built-in domain methodology): the types of the
/// definition nodes of the methodologies (ADR 0023). Its nodes describe how work is done, not what is worked on, so
/// the views of the graph leave it out by default.
pub const NAMESPACE_METHODOLOGY: &str = "methodology";

/// The node types of the adapter, the meeting point of organisation, MCP and connector (pkg/adapter, ADR 0019): the
/// definition is a node of the platform namespace, the instance a node of the organisation namespace held by a unit.
pub const TYPE_ADAPTER_DEF: &str = "platform@AdapterDef";
/// TypeMCP is the type of the node of an MCP, a generic tool definition (pkg/mcp).
pub const TYPE_MCP: &str = "platform@MCP";
/// The type of the instance of an adapter held by a unit.
pub const TYPE_ADAPTER: &str = "organisation@Adapter";

/// NamespaceOf returns ns, defaulting to DefaultNamespace.
pub fn namespace_of(ns: &str) -> &str {
    if ns.is_empty() { DEFAULT_NAMESPACE } else { ns }
}

/// ChangeBranchOrigin is the Branch.Origin of the branch owned by a change.
pub fn change_branch_origin(id: &ChangeID) -> String {
    format!("change:{id}")
}

/// MainBranch is the default branch.
pub const MAIN_BRANCH: &str = "main";

/// Version reasons: first version of a node.
pub const REASON_CREATE: &str = "create";
/// Successor on the same branch.
pub const REASON_REVISE: &str = "revise";
/// First version on a parallel branch.
pub const REASON_DERIVE: &str = "derive";
/// Merge of versions of two branches.
pub const REASON_MERGE: &str = "merge";
/// A version that makes a change branch equal to an adopted flow (ADR 0025).
pub const REASON_ADOPT: &str = "adopt";

/// BranchOf returns the branch name, defaulting to main.
pub fn branch_of(b: &str) -> &str {
    if b.is_empty() { MAIN_BRANCH } else { b }
}

/// Node is one immutable version of a domain node (or, with Version 0, the draft of a node in a change, ADR 0079: see
/// Draft::as_node). Versions are numbered per node across all branches; Parents link a version to the one(s) it comes
/// from. A version is written on one branch (Branch, never changed) and may join other branches when a merge lands it
/// there as is (Joined, ADR 0032).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Node {
    pub id: NodeID,
    pub version: Version,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub branch: String,
    /// Joined lists the other branches the version is part of (filled by Versions only).
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub joined: Vec<String>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub parents: Vec<Version>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub namespace: String,
    pub key: String,
    pub r#type: String,
    #[serde(
        rename = "props",
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub properties: Map<String, Value>,
    #[serde(skip_serializing_if = "is_false")]
    pub deleted: bool,
    /// State in the lifecycle of the node type (empty: the type has none).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub state: String,
    /// ChangeID is the change that wrote the version: every version has one (ADR 0049, 0054).
    #[serde(skip_serializing_if = "ChangeID::is_empty")]
    pub change_id: ChangeID,
    /// Owner is the organisational unit responsible for the version (a node of the organisation structure, ADR
    /// 0054): the unit holding the change that created the node, carried over by the next versions unless a change
    /// transfers it.
    #[serde(skip_serializing_if = "NodeID::is_empty")]
    pub owner: NodeID,
    /// Project is the project the node was created in (a node of the project structure, ADR 0054): the project of
    /// the change that created it, the same for every version.
    #[serde(skip_serializing_if = "NodeID::is_empty")]
    pub project: NodeID,
    /// ChangeImpact is the change impact that produced this version, Comment the acceptance comment (else the
    /// rationale): the origin of the version (ADR 0024).
    #[serde(skip_serializing_if = "ChangeImpactID::is_empty")]
    pub change_impact: ChangeImpactID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub comment: String,
    /// Execution is the journal execution (action run) that wrote this version: what a relaunch of a step marks stale
    /// (ADR 0025).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub execution: String,
    /// Origins are the nodes the node derives from (ADR 0077, "Merge and split"): set on the first version of the
    /// successor of a merge or a split, pure lineage across nodes, never structural (Parents are the versions of one
    /// node).
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub origins: Vec<NodeRef>,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub created_at: DateTime<Utc>,
}

impl Node {
    /// Ref returns the exact reference of this node version.
    pub fn r#ref(&self) -> NodeRef {
        NodeRef {
            id: self.id.clone(),
            version: self.version,
        }
    }

    /// IsDraft reports the view of the draft of a node in a change (no version, ADR 0079).
    pub fn is_draft(&self) -> bool {
        self.version == 0
    }

    /// On reports whether the version is part of a branch: written there, or joined (Joined is filled by Versions).
    pub fn on(&self, branch: &str) -> bool {
        let branch = branch_of(branch);
        branch_of(&self.branch) == branch || self.joined.iter().any(|j| j == branch)
    }
}

string_type!(
    /// LinkID identifies a link.
    LinkID
);

/// Link is a typed relation between two exact node versions.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Link {
    pub id: LinkID,
    pub r#type: String,
    pub from: NodeRef,
    pub to: NodeRef,
    #[serde(
        rename = "props",
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub properties: Map<String, Value>,
    #[serde(skip_serializing_if = "ChangeID::is_empty")]
    pub change_id: ChangeID,
}

string_type!(
    /// BaselineID identifies a baseline.
    BaselineID
);

/// Baseline is a consistent snapshot of the domain graph: which version of each node is part of it. Links belong to
/// a baseline when both endpoints do.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Baseline {
    pub id: BaselineID,
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub namespace: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub branch: String,
    #[serde(skip_serializing_if = "BaselineID::is_empty")]
    pub parent_id: BaselineID,
    /// MergedFrom is the head baseline of the branch merged in, when this baseline is the result of a merge (ADR
    /// 0032): a baseline has at most one such second parent, ParentID being the first (the target branch's previous
    /// head).
    #[serde(skip_serializing_if = "BaselineID::is_empty")]
    pub merged_from: BaselineID,
    /// RebasedFrom is the baseline the change was forked from before it was rebased onto the head of its target (ADR
    /// 0101), on the commit baseline of a rebased change: the first fork, when it was rebased several times.
    #[serde(skip_serializing_if = "BaselineID::is_empty")]
    pub rebased_from: BaselineID,
    #[serde(skip_serializing_if = "ChangeID::is_empty")]
    pub change_id: ChangeID,
    /// Kind says how the state of the baseline follows from the change that produced it (ADR 0056): BaselineCommit,
    /// BaselineMerge, BaselineFastForward or BaselineSnapshot.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub kind: String,
    /// Gap is how far the baseline is from a snapshot of its state (ADR 0056): 0, the state is materialised (its
    /// entries are stored); n > 0, only its header is stored and the state is computed from the baseline n steps
    /// back and what the changes in between did (see Kind). The graph hides it: a baseline it returns always holds
    /// its nodes.
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub gap: i64,
    #[serde(deserialize_with = "null_default")]
    pub nodes: BTreeMap<NodeID, Version>,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub created_at: DateTime<Utc>,
}

/// BaselineCommit is the state a change leaves on its own branch when it is committed.
pub const BASELINE_COMMIT: &str = "commit";
/// BaselineMerge is the state of a branch after a 3-way merge into it.
pub const BASELINE_MERGE: &str = "merge";
/// BaselineFastForward is the state of a branch after a change was fast-forwarded into it.
pub const BASELINE_FAST_FORWARD: &str = "fast-forward";
/// BaselineRebase is the state of the branch of a committed change moved onto the head of its target (ADR 0102):
/// that head with what the change committed; stored whole (not derived).
pub const BASELINE_REBASE: &str = "rebase";
/// BaselineRevert is a commit that also puts back, on the branch of a change, the nodes it no longer changes (an
/// impact withdrawn after its commit, ADR 0102): stored whole (not derived).
pub const BASELINE_REVERT: &str = "revert";
/// BaselineSnapshot is a state nothing derives (the bootstrap, a fixture): always stored.
pub const BASELINE_SNAPSHOT: &str = "snapshot";

/// DerivedKind tells whether the state of a baseline of this kind follows from what its change did: a commit and a
/// merge from the `landed` events naming the baseline in the log of its change, a fast-forward from the versions its
/// change joined to the branch (ADR 0056).
pub fn derived_kind(kind: &str) -> bool {
    kind == BASELINE_COMMIT || kind == BASELINE_MERGE || kind == BASELINE_FAST_FORWARD
}

string_type!(
    /// TagID identifies a tag.
    TagID
);

/// Tag is a name given to the state a change leaves (ADR 0056): it labels the change that produced the state, and
/// the materialised baseline of that change when one is kept, never a baseline of its own. Tags are not unique: one
/// name may label several changes (a "release" per product line), and a change may carry several names.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Tag {
    pub id: TagID,
    pub name: String,
    pub namespace: String,
    /// ChangeID is the change whose resulting state is named.
    pub change_id: ChangeID,
    /// BaselineID is the materialised snapshot of that state, empty while it is only computed.
    #[serde(skip_serializing_if = "BaselineID::is_empty")]
    pub baseline_id: BaselineID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub created_at: DateTime<Utc>,
}

/// TagFilter selects tags; an empty field matches every value.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct TagFilter {
    pub namespace: String,
    pub name: String,
    pub change: ChangeID,
}

impl Baseline {
    /// Contains reports whether the exact node version is part of the baseline.
    pub fn contains(&self, r: &NodeRef) -> bool {
        self.nodes.get(&r.id).is_some_and(|v| *v == r.version)
    }
}

/// Branch statuses.
pub const BRANCH_OPEN: &str = "open";
pub const BRANCH_MERGED: &str = "merged";
pub const BRANCH_ABANDONED: &str = "abandoned";

/// Branch is a line of versions parallel to main (an option of a change, a maintenance line…). Its head is its most
/// recent baseline.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Branch {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub namespace: String,
    pub parent: String,
    pub fork_baseline: BaselineID,
    /// Head is the latest baseline of the branch.
    #[serde(skip_serializing_if = "BaselineID::is_empty")]
    pub head: BaselineID,
    /// RebasedFrom is the first fork baseline of a change branch moved by a rebase (ADR 0101), set once.
    #[serde(skip_serializing_if = "BaselineID::is_empty")]
    pub rebased_from: BaselineID,
    /// Origin is the change / option that opened it.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub origin: String,
    /// Intent says why the branch exists relative to its parent (derive/revise/refine, same vocabulary as an
    /// Option's, architecture plan "exploration branches"): derive, a new independent line of work (a sub-change of a
    /// sub-activity, the common case); revise, a correction of what the parent already has; refine, a sub-branch
    /// narrowing an already-open sibling's work. "" on a branch opened before this existed.
    #[serde(skip_serializing_if = "OptionIntent::is_empty")]
    pub intent: OptionIntent,
    pub status: String,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub created_at: DateTime<Utc>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
}

/// AttributeCheck is what the graph needs of an attribute of a node type to judge the value of a property: its type
/// (empty: unchecked) and, for an enum, the allowed values.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct AttributeCheck {
    pub name: String,
    pub r#type: String,
    pub r#enum: String,
    pub values: Vec<String>,
}
