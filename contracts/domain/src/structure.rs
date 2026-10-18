//! Structures (ADR 0054): the two hierarchies every node version is placed in. A node version is owned by an
//! organisational unit (WHO is responsible for it) and its node was created in a project (WHERE the work happens).
//! Both hierarchies are node types a domain tags (`structure:` on a node type, pkg/domain/def), so the graph knows
//! them, their parent link and their root from its type catalogue (pkg/typecat) and never from hard-coded names.

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::serde_util::{is_false, null_default};

/// StructureOrganisation names the axis of the organisational units: the owners of node versions and of changes.
pub const STRUCTURE_ORGANISATION: &str = "organisation";
/// StructureProject names the axis of the projects: where a node is created and a change acts.
pub const STRUCTURE_PROJECT: &str = "project";

/// Structure is a hierarchy the graph places every node version in: the node type that builds it (and its subtypes),
/// the link type from a child to its parent, and the key of its root, created by the bootstrap of the graph.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Structure {
    pub kind: String,
    /// Type is the qualified node type of the hierarchy; its subtypes belong to it too.
    pub r#type: String,
    /// Namespace is the namespace of Type: where the nodes of the hierarchy live.
    pub namespace: String,
    /// Parent is the qualified link type from a child to its parent. The root links to nothing or to itself
    /// (SelfParent).
    pub parent: String,
    /// Root is the key of the root node, created by the bootstrap.
    pub root: String,
    /// SelfParent: the root is its own parent, rather than rootless.
    #[serde(skip_serializing_if = "is_false")]
    pub self_parent: bool,
    /// Bootstrap are the initial properties of the root node.
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub bootstrap: Map<String, Value>,
}

/// StructureSet is a structure with the node types belonging to it (the tagged type and its subtypes, sorted). It
/// derefs to its structure (Go embeds it).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct StructureSet {
    #[serde(flatten)]
    pub structure: Structure,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub types: Vec<String>,
}

impl std::ops::Deref for StructureSet {
    type Target = Structure;
    fn deref(&self) -> &Structure {
        &self.structure
    }
}

/// RequiredLink is a link a node of a type must carry (`requires:` on a node type, ADR 0065): exactly Count outgoing
/// links of type Link (the qualified link type once resolved by the type catalogue; a bare name of the domain in its
/// definition). It is checked when a node is created and after each write, as the parent of a structure is (ADR
/// 0054).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RequiredLink {
    pub link: String,
    /// Count is the number of links of the type the node must have; 0 in a definition means 1.
    #[serde(skip_serializing_if = "crate::serde_util::is_zero_i64")]
    pub count: i64,
}

impl RequiredLink {
    /// Exactly is the number of links required: Count, 1 when unset.
    pub fn exactly(&self) -> i64 {
        if self.count <= 0 { 1 } else { self.count }
    }
}

/// Structures are the structures in force, one per kind, with the node types belonging to each: what the services
/// reading the organisation (pkg/access, internal/mcpsvc) learn from the graph service (GetStructures), so that they
/// never name the types themselves.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Structures(#[serde(deserialize_with = "null_default")] pub Vec<StructureSet>);

impl std::ops::Deref for Structures {
    type Target = Vec<StructureSet>;
    fn deref(&self) -> &Vec<StructureSet> {
        &self.0
    }
}

impl std::ops::DerefMut for Structures {
    fn deref_mut(&mut self) -> &mut Vec<StructureSet> {
        &mut self.0
    }
}

impl From<Vec<StructureSet>> for Structures {
    fn from(v: Vec<StructureSet>) -> Self {
        Structures(v)
    }
}

impl Structures {
    /// Of returns the structure of a kind (zero when none is declared).
    pub fn of(&self, kind: &str) -> Structure {
        self.0
            .iter()
            .find(|x| x.kind == kind)
            .map(|x| x.structure.clone())
            .unwrap_or_default()
    }

    /// Organisation is the structure of the organisational units.
    pub fn organisation(&self) -> Structure {
        self.of(STRUCTURE_ORGANISATION)
    }

    /// Project is the structure of the projects.
    pub fn project(&self) -> Structure {
        self.of(STRUCTURE_PROJECT)
    }

    /// TypesOf returns the node types belonging to the structure of a kind.
    pub fn types_of(&self, kind: &str) -> &[String] {
        self.0
            .iter()
            .find(|x| x.kind == kind)
            .map_or(&[], |x| x.types.as_slice())
    }

    /// In reports whether a node type belongs to the structure of a kind (its tagged type or a subtype). Go's `In`.
    pub fn includes(&self, kind: &str, typ: &str) -> bool {
        self.types_of(kind).iter().any(|t| t == typ)
    }
}
