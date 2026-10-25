//! The graph of a namespace as a snapshot (Go's `snapshot.go`, ADR 0104): the state a baseline holds, without its
//! history (no version, no branch, no change), its nodes named by key with their links to the other nodes of the
//! snapshot.
use std::collections::{HashMap, HashSet};

use chrono::{DateTime, FixedOffset, Utc};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    GraphError, Result, jsontext,
    model::{Baseline, BaselineId, ChangeId, Link, Node, NodeRef, branch_of, namespace_of},
    reach::Reach,
    read,
    store::ReadTx,
    write::work::fold_change,
};

/// The format of a graph snapshot (JSON).
pub const SNAPSHOT_FORMAT: &str = "goap.graph.snapshot/v1";

/// The prefix of the log entries of the model calls (`domain.LogModel`).
const LOG_MODEL: &str = "model.";

/// The state of the graph of a namespace at a baseline (`graph.Snapshot`), its fields in Go's order.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Snapshot {
    #[serde(default)]
    pub format: String,
    #[serde(default)]
    pub namespace: String,
    /// where the state was read; informative, an import ignores it
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub branch: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub baseline: String,
    #[serde(with = "offset_time", default = "zero_offset")]
    pub exported_at: DateTime<FixedOffset>,
    #[serde(default, deserialize_with = "nullable")]
    pub nodes: Vec<SnapshotNode>,
}

/// A node of a snapshot (`graph.SnapshotNode`).
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SnapshotNode {
    #[serde(default)]
    pub key: String,
    #[serde(rename = "type", default)]
    pub typ: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub state: String,
    /// the unit owning the node, as its URL
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub owner: String,
    #[serde(default, skip_serializing_if = "no_props", deserialize_with = "nullable_map")]
    pub props: Option<Map<String, Value>>,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "nullable")]
    pub links: Vec<SnapshotLink>,
    /// where the version exported comes from; informative, an import ignores it
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub provenance: Option<SnapshotProvenance>,
}

/// The origin of the version of a node of a snapshot (`graph.SnapshotProvenance`, EU AI Act Art. 50(2)).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct SnapshotProvenance {
    #[serde(default)]
    pub version: i64,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub change: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub produced_by: String,
    /// true when the action run that produced the version called a model
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub model: bool,
}

/// An outgoing link of a node of a snapshot, to another node of the snapshot by its key (`graph.SnapshotLink`).
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SnapshotLink {
    #[serde(rename = "type", default)]
    pub typ: String,
    #[serde(default)]
    pub to: String,
    #[serde(default, skip_serializing_if = "no_props", deserialize_with = "nullable_map")]
    pub props: Option<Map<String, Value>>,
}

fn no_props(p: &Option<Map<String, Value>>) -> bool {
    p.as_ref().is_none_or(Map::is_empty)
}

/// A list Go decodes from `null` as none.
fn nullable<'de, D, T>(d: D) -> std::result::Result<Vec<T>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de>,
{
    Ok(Option::<Vec<T>>::deserialize(d)?.unwrap_or_default())
}

fn nullable_map<'de, D>(d: D) -> std::result::Result<Option<Map<String, Value>>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    Option::<Map<String, Value>>::deserialize(d)
}

fn zero_offset() -> DateTime<FixedOffset> {
    jsontext::zero_time().fixed_offset()
}

/// A time with its offset as Go marshals a `time.Time` (RFC 3339, trailing zeros of the fraction dropped, `Z` for no
/// offset) and parses one.
mod offset_time {
    use super::*;

    pub fn serialize<S: serde::Serializer>(t: &DateTime<FixedOffset>, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(&text(t))
    }

    pub fn deserialize<'de, D: serde::Deserializer<'de>>(d: D) -> Result<DateTime<FixedOffset>, D::Error> {
        let s = String::deserialize(d)?;
        DateTime::parse_from_rfc3339(&s).map_err(serde::de::Error::custom)
    }

    pub(super) fn text(t: &DateTime<FixedOffset>) -> String {
        if t.offset().local_minus_utc() == 0 {
            return jsontext::time_text(&t.with_timezone(&Utc));
        }
        let s = t.to_rfc3339_opts(chrono::SecondsFormat::Nanos, false);
        let (head, zone) = s.split_at(s.len() - 6);
        match head.split_once('.') {
            Some((whole, frac)) if frac.trim_end_matches('0').is_empty() => format!("{whole}{zone}"),
            Some((whole, frac)) => format!("{whole}.{}{zone}", frac.trim_end_matches('0')),
            None => s,
        }
    }
}

/// A time as Go's `Format(time.RFC3339)` writes it: to the second, `Z` for no offset.
pub fn rfc3339(t: &DateTime<FixedOffset>) -> String {
    if t.offset().local_minus_utc() == 0 {
        t.format("%Y-%m-%dT%H:%M:%SZ").to_string()
    } else {
        t.format("%Y-%m-%dT%H:%M:%S%:z").to_string()
    }
}

/// Decodes a JSON snapshot (Go's `ParseSnapshot`): the snapshot, checked; an unknown field refused.
pub fn parse_snapshot(data: &str) -> Result<Snapshot> {
    let s: Snapshot = serde_json::from_str(data).map_err(|e| GraphError::invalid(format!("parse snapshot: {e}")))?;
    s.check().map(|()| s)
}

impl Snapshot {
    /// Checks the shape of a snapshot (`Snapshot.check`): the format, a namespace, the keys unique, every type of the
    /// namespace, every link with a type and a target.
    pub fn check(&self) -> Result<()> {
        if self.format != SNAPSHOT_FORMAT {
            return Err(GraphError::invalid(format!(
                "unknown snapshot format {} (expected {})",
                quoted(&self.format),
                quoted(SNAPSHOT_FORMAT)
            )));
        }
        if self.namespace.is_empty() {
            return Err(GraphError::invalid("a snapshot names its namespace"));
        }
        let mut seen = HashSet::new();
        for (i, n) in self.nodes.iter().enumerate() {
            if n.key.is_empty() || n.typ.is_empty() {
                return Err(GraphError::invalid(format!("node {i}: a key and a type are required")));
            }
            if !seen.insert(n.key.as_str()) {
                return Err(GraphError::invalid(format!("node {} appears twice", n.key)));
            }
            // `domain.ParseTypeRef`: a bare name, or `<namespace>@<name>` of the namespace of the snapshot
            let ok = match n.typ.split_once('@') {
                None => true,
                Some((ns, name)) => !ns.is_empty() && !name.is_empty() && !name.contains('@') && ns == self.namespace,
            };
            if !ok {
                return Err(GraphError::invalid(format!(
                    "node {}: type {} is not a type of namespace {}",
                    n.key,
                    quoted(&n.typ),
                    self.namespace
                )));
            }
            if n.links.iter().any(|l| l.typ.is_empty() || l.to.is_empty()) {
                return Err(GraphError::invalid(format!(
                    "node {}: a link needs a type and a target",
                    n.key
                )));
            }
        }
        Ok(())
    }
}

/// A text as Go's `%q` writes it.
fn quoted(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_default()
}

/// Whether two values are the same as JSON carries them (Go's `sameValue`): numbers as float64, an empty object none.
pub fn same_value(a: Option<&Value>, b: Option<&Value>) -> bool {
    let norm = |v: Option<&Value>| match v {
        None | Some(Value::Null) => "null".to_string(),
        Some(Value::Object(m)) if m.is_empty() => "null".to_string(),
        Some(v) => jsontext::to_string(v),
    };
    norm(a) == norm(b)
}

/// Whether two property sets are the same (Go's `sameValue` of two maps).
pub fn same_props(a: &Option<Map<String, Value>>, b: &Option<Map<String, Value>>) -> bool {
    let obj = |p: &Option<Map<String, Value>>| p.as_ref().map(|m| Value::Object(m.clone()));
    same_value(obj(a).as_ref(), obj(b).as_ref())
}

/// The patch bringing the properties held to the wanted ones (Go's `propsPatch`): the values that differ, null for the
/// ones the wanted leave out.
pub fn props_patch(held: &Option<Map<String, Value>>, want: &Option<Map<String, Value>>) -> Map<String, Value> {
    let empty = Map::new();
    let (held, want) = (held.as_ref().unwrap_or(&empty), want.as_ref().unwrap_or(&empty));
    let mut patch = Map::new();
    for (k, v) in want {
        if held.get(k).is_none_or(|h| !same_value(Some(h), Some(v))) {
            patch.insert(k.clone(), v.clone());
        }
    }
    for k in held.keys() {
        if !want.contains_key(k) {
            patch.insert(k.clone(), Value::Null);
        }
    }
    patch
}

/// Reads the state of a namespace at a baseline (empty: the head of the branch, main by default) as a snapshot (Go's
/// `ExportSnapshot`): the nodes the caller reaches, with their links to one another, sorted by key.
pub async fn export_snapshot<T: ReadTx>(
    tx: &T,
    namespace: &str,
    branch: &str,
    baseline: &BaselineId,
    reach: Option<&Reach>,
    now: DateTime<Utc>,
) -> Result<Snapshot> {
    let namespace = namespace_of(namespace).to_string();
    let mut s = Snapshot {
        format: SNAPSHOT_FORMAT.into(),
        namespace: namespace.clone(),
        branch: branch_of(branch).to_string(),
        baseline: String::new(),
        exported_at: now.fixed_offset(),
        nodes: Vec::new(),
    };
    let header = if baseline.is_empty() {
        read::any_branch_head(tx, &namespace, branch).await?
    } else {
        let b = read::header_of(tx, baseline).await?;
        if b.namespace != namespace {
            return Err(GraphError::invalid(format!(
                "baseline {baseline} is a baseline of {}, not {namespace}",
                b.namespace
            )));
        }
        s.branch = branch_of(&b.branch).to_string();
        b
    };
    s.baseline = header.id.0.clone();
    if header.id.is_empty() {
        return Ok(s); // nothing committed yet: the empty state
    }
    let (nodes, links) = baseline_graph(tx, &header).await?;
    let (nodes, links) = Reach::keep(reach, nodes, links); // what the caller reads only (ADR 0161 §5)
    let keys: HashMap<_, _> = nodes.iter().map(|n| (n.id.clone(), n.key.clone())).collect();
    let mut out: HashMap<_, Vec<SnapshotLink>> = HashMap::new();
    for l in links {
        let Some(to) = l.to_node().and_then(|t| keys.get(&t.id)) else {
            continue;
        };
        out.entry(l.from.id.clone()).or_default().push(SnapshotLink {
            typ: l.typ.clone(),
            to: to.clone(),
            props: l.props.clone(),
        });
    }
    let mut provenance = Provenance::default();
    for n in nodes {
        if n.deleted {
            continue;
        }
        let mut links = out.remove(&n.id).unwrap_or_default();
        links.sort_by(|a, b| (&a.typ, &a.to).cmp(&(&b.typ, &b.to)));
        let p = provenance.of(tx, &n).await;
        s.nodes.push(SnapshotNode {
            key: n.key,
            typ: n.typ,
            state: n.state,
            owner: if n.owner.is_zero() {
                String::new()
            } else {
                n.owner.to_string()
            },
            props: n.props,
            links,
            provenance: p,
        });
    }
    s.nodes.sort_by(|a, b| a.key.cmp(&b.key));
    Ok(s)
}

/// The graph a baseline holds (Go's `BaselineGraph`): its versions and the links between them, to the exact version
/// it holds.
pub async fn baseline_graph<T: ReadTx>(tx: &T, header: &Baseline) -> Result<(Vec<Node>, Vec<Link>)> {
    if header.id.is_empty() {
        return Ok((Vec::new(), Vec::new()));
    }
    let state = read::select(tx, header, None).await?;
    let mut refs: Vec<NodeRef> = state.iter().map(|(n, v)| NodeRef::new(n.clone(), *v)).collect();
    refs.sort();
    if refs.is_empty() {
        return Ok((Vec::new(), Vec::new()));
    }
    let nodes = tx.nodes_at(&refs, "").await?;
    let links = tx
        .links_from(&refs, None)
        .await?
        .into_iter()
        .filter(|l| l.to_node().is_some_and(|t| state.get(&t.id) == Some(&t.version)))
        .collect();
    Ok((nodes, links))
}

/// The provenance of the versions of a snapshot, the impacts and the model calls of each change read once (Go's
/// `provenanceReader`).
#[derive(Default)]
struct Provenance {
    changes: HashMap<ChangeId, Origin>,
}

#[derive(Default)]
struct Origin {
    /// by impact: who produced it and the execution it was written in
    impacts: HashMap<String, (String, String)>,
    /// the executions that called a model
    models: HashSet<String>,
}

impl Provenance {
    async fn of<T: ReadTx>(&mut self, tx: &T, n: &Node) -> Option<SnapshotProvenance> {
        if n.change_id.is_empty() {
            return None;
        }
        let mut p = SnapshotProvenance {
            version: i64::from(n.version),
            change: n.change_id.0.clone(),
            ..SnapshotProvenance::default()
        };
        if !n.change_impact.is_empty() {
            if !self.changes.contains_key(&n.change_id) {
                // a change that cannot be read gives no provenance beyond its id, as Go's
                let o = origin(tx, &n.change_id).await.unwrap_or_default();
                self.changes.insert(n.change_id.clone(), o);
            }
            let o = &self.changes[&n.change_id];
            if let Some((by, execution)) = o.impacts.get(&n.change_impact) {
                p.produced_by = by.clone();
                p.model = !execution.is_empty() && o.models.contains(execution);
            }
        }
        Some(p)
    }
}

async fn origin<T: ReadTx>(tx: &T, id: &ChangeId) -> Result<Origin> {
    let mut o = Origin::default();
    for cn in fold_change(tx, id).await?.fold.impacts {
        o.impacts.insert(cn.id, (cn.produced_by, cn.execution));
    }
    for e in tx.log(id, 0, &[LOG_MODEL]).await? {
        if !e.execution.is_empty() {
            o.models.insert(e.execution);
        }
    }
    Ok(o)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_snapshot_writes_as_go_does() {
        let s = Snapshot {
            format: SNAPSHOT_FORMAT.into(),
            namespace: "dv".into(),
            branch: "main".into(),
            baseline: String::new(),
            exported_at: zero_offset(),
            nodes: vec![SnapshotNode {
                key: "A".into(),
                typ: "dv@Thing".into(),
                links: vec![SnapshotLink {
                    typ: "dv@of".into(),
                    to: "B".into(),
                    props: None,
                }],
                provenance: Some(SnapshotProvenance {
                    version: 1,
                    ..SnapshotProvenance::default()
                }),
                ..SnapshotNode::default()
            }],
        };
        assert_eq!(
            jsontext::to_string(&s),
            r#"{"format":"goap.graph.snapshot/v1","namespace":"dv","branch":"main","exportedAt":"0001-01-01T00:00:00Z","nodes":[{"key":"A","type":"dv@Thing","links":[{"type":"dv@of","to":"B"}],"provenance":{"version":1}}]}"#
        );
    }
}
