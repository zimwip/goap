//! The events the graph publishes for the node index (ADR 0026) and the index of changes (ADR 0095).

use std::collections::BTreeMap;

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    BaselineID, ChangeID, ChangeStatus, NodeID, Version, gotime,
    serde_util::{is_false, null_default},
};

/// SubjectNodeWritten is "goap.node.<namespace>.<type>.<id>.written" (a format with three `%s`, as in Go; see
/// subject_node_written).
pub const SUBJECT_NODE_WRITTEN: &str = "goap.node.%s.%s.%s.written";
/// SubjectBaselineAdvanced is "goap.baseline.<branch>.advanced".
pub const SUBJECT_BASELINE_ADVANCED: &str = "goap.baseline.%s.advanced";
/// SubjectChangeTouched is "goap.changed.<id>": the header, the impacts or the log of a change were written (a
/// ChangeEvent of type "change.updated", the header only when it was the header). Not under goap.change.>, which the
/// trigger manager follows for the lifecycle of changes.
pub const SUBJECT_CHANGE_TOUCHED: &str = "goap.changed.%s";
/// SubjectChangeIndexed is "goap.changeindex.<id>": the document of a change for the index (a ChangeDocEvent),
/// published when the header or the impacts of the change were written, or when it was purged (ADR 0095). A subject
/// of its own: the hub forwards goap.changed.> to the browsers, which have no use for the impact keys.
pub const SUBJECT_CHANGE_INDEXED: &str = "goap.changeindex.%s";

/// SUBJECT_NODE_WRITTEN filled in.
pub fn subject_node_written(namespace: &str, typ: &str, id: &NodeID) -> String {
    format!("goap.node.{namespace}.{typ}.{id}.written")
}

/// SUBJECT_BASELINE_ADVANCED filled in.
pub fn subject_baseline_advanced(branch: &str) -> String {
    format!("goap.baseline.{branch}.advanced")
}

/// SUBJECT_CHANGE_TOUCHED filled in.
pub fn subject_change_touched(id: &ChangeID) -> String {
    format!("goap.changed.{id}")
}

/// SUBJECT_CHANGE_INDEXED filled in.
pub fn subject_change_indexed(id: &ChangeID) -> String {
    format!("goap.changeindex.{id}")
}

/// SearchProperty declares how the node index uses a property of a node type.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SearchProperty {
    pub property: String,
    #[serde(skip_serializing_if = "is_false")]
    pub text: bool,
    #[serde(skip_serializing_if = "is_false")]
    pub facet: bool,
}

/// NodeEvent is published when a node version is written (on any branch), after the commit. Text and Facets are the
/// searchable properties of the node type, already resolved.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct NodeEvent {
    pub id: NodeID,
    pub version: Version,
    pub branch: String,
    pub namespace: String,
    pub key: String,
    pub r#type: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub state: String,
    #[serde(skip_serializing_if = "is_false")]
    pub deleted: bool,
    /// Project and Owner are the project and the unit owning the version (ADR 0054), used to authorize search results.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub project: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub owner: String,
    #[serde(skip_serializing_if = "ChangeID::is_empty")]
    pub change_id: ChangeID,
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub text: BTreeMap<String, String>,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub facets: Map<String, Value>,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub time: DateTime<Utc>,
}

/// Default is Go's zero value: its times are Go's zero time, not the Unix epoch.
impl Default for NodeEvent {
    fn default() -> Self {
        NodeEvent {
            id: Default::default(),
            version: Default::default(),
            branch: Default::default(),
            namespace: Default::default(),
            key: Default::default(),
            r#type: Default::default(),
            state: Default::default(),
            deleted: Default::default(),
            project: Default::default(),
            owner: Default::default(),
            change_id: Default::default(),
            text: Default::default(),
            facets: Default::default(),
            time: gotime::zero(),
        }
    }
}

/// BaselineEvent is published when a baseline is created: the diff with its parent tells which node versions became
/// the head of the branch.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct BaselineEvent {
    pub id: BaselineID,
    pub branch: String,
    #[serde(skip_serializing_if = "BaselineID::is_empty")]
    pub parent: BaselineID,
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub set: BTreeMap<NodeID, Version>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub removed: Vec<NodeID>,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub time: DateTime<Utc>,
}

/// Default is Go's zero value: its times are Go's zero time, not the Unix epoch.
impl Default for BaselineEvent {
    fn default() -> Self {
        BaselineEvent {
            id: Default::default(),
            branch: Default::default(),
            parent: Default::default(),
            set: Default::default(),
            removed: Default::default(),
            time: gotime::zero(),
        }
    }
}

/// ChangeDocEvent is what the index needs to know of a change (ADR 0095): the header fields it searches and filters
/// on and the keys of the nodes the change acts on, not its items, decisions, reviews or drafts. Deleted is a purge.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ChangeDocEvent {
    pub id: ChangeID,
    #[serde(skip_serializing_if = "is_false")]
    pub deleted: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub intent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub goal: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub namespace: String,
    #[serde(skip_serializing_if = "ChangeStatus::is_empty")]
    pub status: ChangeStatus,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub state: String,
    #[serde(rename = "projectId", skip_serializing_if = "String::is_empty")]
    pub project_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub owner_org: String,
    #[serde(rename = "parentId", skip_serializing_if = "ChangeID::is_empty")]
    pub parent_id: ChangeID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub branch: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub impacts: Vec<ChangeDocRef>,
    /// CreatedAt is always written (Go's omitempty has no effect on a time).
    #[serde(with = "gotime", default = "gotime::zero")]
    pub created_at: DateTime<Utc>,
}

/// Default is Go's zero value: its times are Go's zero time, not the Unix epoch.
impl Default for ChangeDocEvent {
    fn default() -> Self {
        ChangeDocEvent {
            id: Default::default(),
            deleted: Default::default(),
            title: Default::default(),
            intent: Default::default(),
            goal: Default::default(),
            methodology: Default::default(),
            namespace: Default::default(),
            status: Default::default(),
            state: Default::default(),
            project_id: Default::default(),
            owner_org: Default::default(),
            parent_id: Default::default(),
            branch: Default::default(),
            impacts: Default::default(),
            created_at: gotime::zero(),
        }
    }
}

/// ChangeDocRef names a node a change acts on.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ChangeDocRef {
    pub key: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub r#type: String,
}
