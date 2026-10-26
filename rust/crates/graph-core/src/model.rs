//! The model the graph reads and writes: nodes and their versions, links, baselines, branches.
use std::{borrow::Borrow, fmt};

use chrono::{DateTime, Utc};
use serde_json::{Map, Value};

use crate::lnk::Url;

/// The namespace of what names none.
pub const DEFAULT_NAMESPACE: &str = "default";
/// The default branch.
pub const MAIN_BRANCH: &str = "main";
/// The namespaces of the platform: read whole by every caller (ADR 0161 §5).
pub const NAMESPACE_ORGANISATION: &str = "organisation";
pub const NAMESPACE_PLATFORM: &str = "platform";
pub const NAMESPACE_METHODOLOGY: &str = "methodology";

/// A namespace, `default` when empty.
pub fn namespace_of(ns: &str) -> &str {
    if ns.is_empty() { DEFAULT_NAMESPACE } else { ns }
}

/// A branch, `main` when empty.
pub fn branch_of(b: &str) -> &str {
    if b.is_empty() { MAIN_BRANCH } else { b }
}

macro_rules! id {
    ($(#[$doc:meta])* $name:ident) => {
        $(#[$doc])*
        #[derive(Debug, Clone, Default, PartialEq, Eq, PartialOrd, Ord, Hash, serde::Serialize, serde::Deserialize)]
        #[serde(transparent)]
        pub struct $name(pub String);

        impl $name {
            pub fn as_str(&self) -> &str {
                &self.0
            }
            pub fn is_empty(&self) -> bool {
                self.0.is_empty()
            }
        }

        impl fmt::Display for $name {
            fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                f.write_str(&self.0)
            }
        }

        impl From<&str> for $name {
            fn from(s: &str) -> Self {
                $name(s.to_string())
            }
        }

        impl From<String> for $name {
            fn from(s: String) -> Self {
                $name(s)
            }
        }

        impl Borrow<str> for $name {
            fn borrow(&self) -> &str {
                &self.0
            }
        }
    };
}

id!(
    /// The id of a node (a UUID in the SQL stores).
    NodeId
);
id!(
    /// The id of a link row.
    LinkId
);
id!(
    /// The id of a baseline; empty is the empty state, the one before the first change of a namespace (ADR 0056).
    BaselineId
);
id!(
    /// The id of a change.
    ChangeId
);

/// The number of a version of a node: 1 for its first, 0 for the draft a change holds (ADR 0079).
pub type Version = i32;

/// A version of a node.
#[derive(Debug, Clone, Default, PartialEq, Eq, PartialOrd, Ord, Hash, serde::Serialize, serde::Deserialize)]
pub struct NodeRef {
    pub id: NodeId,
    pub version: Version,
}

impl NodeRef {
    pub fn new(id: NodeId, version: Version) -> NodeRef {
        NodeRef { id, version }
    }

    /// A draft reference: the draft a change holds of the node.
    pub fn is_draft(&self) -> bool {
        self.version == 0
    }
}

impl fmt::Display for NodeRef {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}@v{}", self.id, self.version)
    }
}

/// Properties of a node or a link: a JSON object, `None` when empty.
pub type Props = Option<Map<String, Value>>;

/// A version of a node.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Node {
    pub id: NodeId,
    pub version: Version,
    pub namespace: String,
    pub key: String,
    pub typ: String,
    pub props: Props,
    pub deleted: bool,
    pub change_id: ChangeId,
    pub created_at: Option<DateTime<Utc>>,
    /// The branch the version was written on (never changes, ADR 0032).
    pub branch: String,
    pub parents: Vec<Version>,
    pub reason: String,
    pub state: String,
    pub change_impact: String,
    pub comment: String,
    pub execution: String,
    /// The branches the version joined as is (filled by the reads of a history only).
    pub joined: Vec<String>,
    /// The unit owning the version and the project of the node, logical URLs (ADR 0154).
    pub owner: Url,
    pub project: Url,
    pub origins: Vec<NodeRef>,
    /// Who created the node (ADR 0161 §3), the same for every version: stamped by its first one.
    pub creator: Creator,
    /// The person whose edits made the version (attribution; the first version's is the creator), stamped at commit.
    pub editor: String,
    /// How far the version is seen (ADR 0161 §4: `personal`...), stamped at commit; empty reads as `project`.
    pub visibility: String,
}

/// Who created a node (Go's `domain.Creator`): the subject, and the unit it was created in.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Creator {
    pub subject: String,
    pub unit: Url,
}

pub use graph_catalog::def::{
    VISIBILITIES, VISIBILITY_PERSONAL, VISIBILITY_PRIVATE, VISIBILITY_PROJECT, VISIBILITY_PUBLIC, VISIBILITY_TREE,
    check_visibility, visibility_of,
};

impl Node {
    pub fn node_ref(&self) -> NodeRef {
        NodeRef::new(self.id.clone(), self.version)
    }

    /// Whether the node holds `text` (lower case): in its key, its type or a string property.
    pub fn matches(&self, text: &str) -> bool {
        if self.key.to_lowercase().contains(text) || self.typ.to_lowercase().contains(text) {
            return true;
        }
        self.props.iter().flat_map(|m| m.values()).any(|v| match v {
            Value::String(s) => s.to_lowercase().contains(text),
            _ => false,
        })
    }
}

/// Where a link points: a version of a node of this graph, or a logical URL of another source (ADR 0154).
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub enum Target {
    Node(NodeRef),
    External(Url),
}

/// A link, as it leaves a version of its source.
#[derive(Debug, Clone, PartialEq)]
pub struct Link {
    pub id: LinkId,
    pub typ: String,
    pub from: NodeRef,
    pub to: Target,
    pub props: Props,
    pub change_id: ChangeId,
}

impl Link {
    /// The node version the link points to, none for an external target.
    pub fn to_node(&self) -> Option<&NodeRef> {
        match &self.to {
            Target::Node(r) => Some(r),
            Target::External(_) => None,
        }
    }

    pub fn is_external(&self) -> bool {
        matches!(self.to, Target::External(_))
    }
}

/// The header of a baseline: the state a change left, ordered by `seq` among the commits (ADR 0140).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Baseline {
    pub id: BaselineId,
    pub name: String,
    pub parent_id: BaselineId,
    pub merged_from: BaselineId,
    pub change_id: ChangeId,
    pub created_at: Option<DateTime<Utc>>,
    pub branch: String,
    pub namespace: String,
    pub kind: String,
    pub rebased_from: BaselineId,
    pub seq: i64,
}

/// A branch of a namespace.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Branch {
    pub namespace: String,
    pub name: String,
    pub parent: String,
    pub fork_baseline: BaselineId,
    pub head: BaselineId,
    pub origin: String,
    pub status: String,
    pub created_at: Option<DateTime<Utc>>,
    pub description: String,
    pub intent: String,
    pub rebased_from: BaselineId,
    /// The branch of a change: it holds no baseline, its state is its fork with what committed on it (ADR 0140).
    pub ephemeral: bool,
    pub fork_seq: i64,
}

/// The status of an open branch.
pub const BRANCH_OPEN: &str = "open";
pub const BRANCH_MERGED: &str = "merged";
pub const BRANCH_ABANDONED: &str = "abandoned";

/// A count of the nodes or links of a type.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TypeCount {
    pub typ: String,
    pub count: usize,
}

/// A version of a node with its links, as a view reads it.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct NodeView {
    pub node: Node,
    /// The latest version of the node on main (the node's own when it has none there).
    pub latest: Version,
    pub out: Vec<Link>,
    pub r#in: Vec<Link>,
}

/// A node and its direct neighbours.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Neighbourhood {
    pub node: Node,
    pub nodes: Vec<Node>,
    pub links: Vec<Link>,
    /// The links naming a version of their target the view does not hold.
    pub suspect: Vec<LinkId>,
}

/// A page of the nodes of a view.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct NodePage {
    pub nodes: Vec<Node>,
    pub total: usize,
    pub types: Vec<TypeCount>,
    pub baseline: BaselineId,
}

/// What a page of nodes asks.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct NodeQuery {
    /// That type only (empty: every type); the counts per type are of every type.
    pub typ: String,
    /// The nodes whose key, type or a string property holds it, case-insensitive.
    pub text: String,
    pub include_deleted: bool,
    /// The nodes the impacts of a change act on (empty: every node).
    pub change: ChangeId,
    pub offset: usize,
    pub limit: usize,
    /// The reach of the caller (None: every node).
    pub reach: Option<ReachFilter>,
}

/// A caller's reach as the stores filter it (Go's `graphapi.ReachFilter`, ADR 0161 §4-5): the keys of the structures
/// as the nodes store them (their logical URLs and their bare keys).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ReachFilter {
    /// The projects it works on.
    pub projects: Vec<String>,
    /// The projects whose versions of scope tree it reads.
    pub trees: Vec<String>,
    /// The units whose private versions it reads.
    pub units: Vec<String>,
    /// The caller: the personal versions of the nodes it created.
    pub subject: String,
}

impl ReachFilter {
    /// Whether the reach holds a version (`ReachFilter.Holds`).
    pub fn holds(&self, n: &Node) -> bool {
        let is_in =
            |list: &[String], u: &Url| !u.key.is_empty() && list.iter().any(|x| *x == u.key || *x == u.to_string());
        match visibility_of(&n.visibility) {
            VISIBILITY_PUBLIC => true,
            VISIBILITY_TREE => is_in(&self.trees, &n.project) || is_in(&self.projects, &n.project),
            VISIBILITY_PRIVATE => is_in(&self.projects, &n.project) && is_in(&self.units, &n.owner),
            VISIBILITY_PERSONAL => {
                is_in(&self.projects, &n.project) && !self.subject.is_empty() && n.creator.subject == self.subject
            }
            _ => is_in(&self.projects, &n.project),
        }
    }
}

/// The largest page of a browse.
pub const MAX_BROWSE_LIMIT: usize = 500;

/// Why a version was written (`node_version.reason`).
pub const REASON_CREATE: &str = "create";
pub const REASON_REVISE: &str = "revise";
pub const REASON_DERIVE: &str = "derive";
pub const REASON_MERGE: &str = "merge";
pub const REASON_REBASE: &str = "rebase";
pub const REASON_REVERT: &str = "revert";
