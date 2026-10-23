//! The events of the graph as Go writes them in its outbox (ADR 0136): `change.*` of a change, the technical
//! `change.updated`, the documents of the index (ADR 0095), the versions written and the baselines (ADR 0026). The
//! JSON keeps the fields and the order of `contracts/domain`.
use std::collections::BTreeMap;

use chrono::{DateTime, Utc};
use serde::Serialize;
use serde_json::{Map, Value};

use crate::{
    change::{Change, GuardianRef},
    gojson,
    lnk::Url,
    model::{Baseline, BaselineId, ChangeId, NodeId, Version, branch_of},
};

/// `goap.changed.<id>`: a write of a change (header, impacts, log).
pub fn subject_change_touched(id: &ChangeId) -> String {
    format!("goap.changed.{id}")
}

/// `goap.changeindex.<id>`: the document of a change for the index.
pub fn subject_change_indexed(id: &ChangeId) -> String {
    format!("goap.changeindex.{id}")
}

/// `goap.change.<id>.<event>`: an event of a change (`contracts/events.ChangeSubject`).
pub fn change_subject(id: &ChangeId, event: &str) -> String {
    format!("goap.change.{id}.{}", event.strip_prefix("change.").unwrap_or(event))
}

/// A value usable as one token of a NATS subject.
pub fn subject_token(s: &str) -> String {
    s.chars()
        .map(|c| if matches!(c, '.' | '*' | '>' | ' ') { '_' } else { c })
        .collect()
}

/// `goap.node.<namespace>.<type>.<id>.written`.
pub fn subject_node_written(namespace: &str, typ: &str, id: &NodeId) -> String {
    format!(
        "goap.node.{}.{}.{id}.written",
        subject_token(namespace),
        subject_token(typ)
    )
}

/// `goap.baseline.<branch>.advanced`.
pub fn subject_baseline_advanced(branch: &str) -> String {
    format!("goap.baseline.{}.advanced", subject_token(branch))
}

pub const CHANGE_CREATED: &str = "change.created";
pub const CHANGE_COMMITTED: &str = "change.committed";
pub const CHANGE_REBASED: &str = "change.rebased";
pub const CHANGE_MERGED: &str = "change.merged";
pub const CHANGE_APPLIED: &str = "change.applied";
pub const CHANGE_PURGED: &str = "change.purged";
pub const CHANGE_ITEM_ADDED: &str = "change.item_added";
pub const CHANGE_SIGNAL: &str = "change.signal";

fn is_zero(n: &i64) -> bool {
    *n == 0
}

/// A change as Go marshals a `domain.Change` with no items nor impacts.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ChangeJson<'a> {
    id: &'a ChangeId,
    title: &'a str,
    intent: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    methodology: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    methodology_version: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    namespace: &'a str,
    #[serde(skip_serializing_if = "ChangeId::is_empty")]
    parent_id: &'a ChangeId,
    #[serde(skip_serializing_if = "Url::is_zero")]
    owner_org: &'a Url,
    #[serde(skip_serializing_if = "Url::is_zero")]
    project_id: &'a Url,
    #[serde(skip_serializing_if = "str::is_empty")]
    lifecycle: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    state: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    goal: &'a str,
    #[serde(skip_serializing_if = "<[GuardianRef]>::is_empty")]
    guardians: &'a [GuardianRef],
    #[serde(skip_serializing_if = "<[ChangeId]>::is_empty")]
    depends_on: &'a [ChangeId],
    status: &'a str,
    baseline_id: &'a BaselineId,
    #[serde(skip_serializing_if = "str::is_empty")]
    branch: &'a str,
    #[serde(skip_serializing_if = "BaselineId::is_empty")]
    result_baseline_id: &'a BaselineId,
    #[serde(skip_serializing_if = "Option::is_none")]
    data: Option<&'a Map<String, Value>>,
    #[serde(skip_serializing_if = "is_zero")]
    seq: i64,
    items: Option<()>,
    #[serde(with = "gojson::time")]
    created_at: DateTime<Utc>,
}

impl<'a> ChangeJson<'a> {
    pub fn of(c: &'a Change, seq: i64) -> ChangeJson<'a> {
        ChangeJson {
            id: &c.id,
            title: &c.title,
            intent: &c.intent,
            methodology: &c.methodology,
            methodology_version: &c.methodology_version,
            namespace: &c.namespace,
            parent_id: &c.parent_id,
            owner_org: &c.owner_org,
            project_id: &c.project_id,
            lifecycle: &c.lifecycle,
            state: &c.state,
            goal: &c.goal,
            guardians: &c.guardians,
            depends_on: &c.depends_on,
            status: &c.status,
            baseline_id: &c.baseline_id,
            branch: &c.branch,
            result_baseline_id: &c.result_baseline_id,
            data: c.data.as_ref().filter(|m| !m.is_empty()),
            seq,
            items: None,
            created_at: c.created_at.unwrap_or_else(gojson::zero_time),
        }
    }
}

/// A baseline as Go marshals a `domain.Baseline` (its state, `nodes`, left out: null).
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct BaselineJson<'a> {
    id: &'a BaselineId,
    name: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    namespace: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    branch: &'a str,
    #[serde(skip_serializing_if = "BaselineId::is_empty")]
    parent_id: &'a BaselineId,
    #[serde(skip_serializing_if = "BaselineId::is_empty")]
    merged_from: &'a BaselineId,
    #[serde(skip_serializing_if = "BaselineId::is_empty")]
    rebased_from: &'a BaselineId,
    #[serde(skip_serializing_if = "ChangeId::is_empty")]
    change_id: &'a ChangeId,
    #[serde(skip_serializing_if = "str::is_empty")]
    kind: &'a str,
    nodes: Option<()>,
    #[serde(with = "gojson::time")]
    created_at: DateTime<Utc>,
}

impl<'a> BaselineJson<'a> {
    pub fn of(b: &'a Baseline) -> BaselineJson<'a> {
        BaselineJson {
            id: &b.id,
            name: &b.name,
            namespace: &b.namespace,
            branch: &b.branch,
            parent_id: &b.parent_id,
            merged_from: &b.merged_from,
            rebased_from: &b.rebased_from,
            change_id: &b.change_id,
            kind: &b.kind,
            nodes: None,
            created_at: b.created_at.unwrap_or_else(gojson::zero_time),
        }
    }
}

/// A version an event of a change names (`domain.EventNode`).
#[derive(Debug, Clone, Serialize)]
pub struct EventNode {
    pub id: NodeId,
    pub version: Version,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub namespace: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub key: String,
    #[serde(rename = "type", skip_serializing_if = "String::is_empty")]
    pub typ: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub state: String,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub deleted: bool,
}

impl EventNode {
    pub fn of(n: &crate::model::Node) -> EventNode {
        EventNode {
            id: n.id.clone(),
            version: n.version,
            namespace: n.namespace.clone(),
            key: n.key.clone(),
            typ: n.typ.clone(),
            state: n.state.clone(),
            deleted: n.deleted,
        }
    }
}

/// An event of a change (`domain.ChangeEvent`), what `Graph.raise` emits.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ChangeEvent<'a> {
    #[serde(rename = "type")]
    pub typ: &'a str,
    pub change: ChangeJson<'a>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub baseline: Option<BaselineJson<'a>>,
    #[serde(skip_serializing_if = "<[Value]>::is_empty")]
    pub items: &'a [Value],
    #[serde(skip_serializing_if = "str::is_empty")]
    pub end_user: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub branch: &'a str,
    #[serde(skip_serializing_if = "<[EventNode]>::is_empty")]
    pub nodes: &'a [EventNode],
}

/// The document of a change for the index (`domain.ChangeDocEvent`).
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ChangeDocEvent<'a> {
    pub id: &'a ChangeId,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub deleted: bool,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub title: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub intent: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub goal: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub methodology: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub namespace: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub status: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub state: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub project_id: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub owner_org: &'a str,
    #[serde(skip_serializing_if = "ChangeId::is_empty")]
    pub parent_id: &'a ChangeId,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub branch: &'a str,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub impacts: Vec<DocRef>,
    #[serde(with = "gojson::time")]
    pub created_at: DateTime<Utc>,
}

#[derive(Serialize)]
pub struct DocRef {
    pub key: String,
    #[serde(rename = "type", skip_serializing_if = "String::is_empty")]
    pub typ: String,
}

/// A version written (`domain.NodeEvent`), for the index.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct NodeEvent<'a> {
    pub id: &'a NodeId,
    pub version: Version,
    pub branch: &'a str,
    pub namespace: &'a str,
    pub key: &'a str,
    #[serde(rename = "type")]
    pub typ: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub state: &'a str,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub deleted: bool,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub project: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub owner: &'a str,
    #[serde(skip_serializing_if = "ChangeId::is_empty")]
    pub change_id: &'a ChangeId,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub text: BTreeMap<String, String>,
    #[serde(skip_serializing_if = "Map::is_empty")]
    pub facets: Map<String, Value>,
    #[serde(with = "gojson::time")]
    pub time: DateTime<Utc>,
}

/// A baseline created (`domain.BaselineEvent`): what it changed of its parent's state.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct BaselineEvent<'a> {
    pub id: &'a BaselineId,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub namespace: &'a str,
    pub branch: &'a str,
    #[serde(skip_serializing_if = "BaselineId::is_empty")]
    pub parent: &'a BaselineId,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub set: BTreeMap<NodeId, Version>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub removed: Vec<NodeId>,
    #[serde(with = "gojson::time")]
    pub time: DateTime<Utc>,
}

/// The branch of a baseline event.
pub fn event_branch(b: &Baseline) -> &str {
    branch_of(&b.branch)
}
