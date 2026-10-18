//! The export and import of the graph of a namespace as a snapshot (ADR 0104): the state a
//! baseline holds, without its history (no version, no branch, no change), and its import as one change made of node
//! creations and modifications, left for review like any other change.

use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::Arc;

use chrono::{DateTime, SecondsFormat, Utc};
use goap_ctx::Ctx;
use goap_domain::{
    BaselineID, CHANGE_ABANDONED, Change, ChangeID, ChangeImpact, ChangeImpactID, LOG_MODEL, Link,
    LogFilter, MAIN_BRANCH, Node, NodeID, NodeRef, STRUCTURE_ORGANISATION, branch_of, json_equal,
    namespace_of, parse_type_ref,
};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::api::{
    ChangePatch, LinkWrite, NewChange, NodeCheckout, NodeCreate, NodeTransition, NodeUpdate,
};
use crate::lifecycle::invalidf;
use crate::repo::Tx;
use crate::{Error, Graph, Result};

/// SnapshotFormat names the format of a graph snapshot (JSON).
pub const SNAPSHOT_FORMAT: &str = "goap.graph.snapshot/v1";

/// Snapshot is the state of the graph of a namespace at a baseline, without history: its nodes named by key, their
/// outgoing links to the other nodes of the snapshot.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct Snapshot {
    #[serde(default)]
    pub format: String,
    #[serde(default)]
    pub namespace: String,
    /// Branch and Baseline say where the state was read; informative, an import ignores them.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub branch: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub baseline: String,
    #[serde(with = "goap_domain::gotime", default = "goap_domain::gotime::zero")]
    pub exported_at: DateTime<Utc>,
    #[serde(default, deserialize_with = "null_vec")]
    pub nodes: Vec<SnapshotNode>,
}

impl Default for Snapshot {
    fn default() -> Self {
        Snapshot {
            format: String::new(),
            namespace: String::new(),
            branch: String::new(),
            baseline: String::new(),
            exported_at: goap_domain::gotime::zero(),
            nodes: Vec::new(),
        }
    }
}

/// SnapshotNode is a node of a snapshot.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SnapshotNode {
    #[serde(default)]
    pub key: String,
    #[serde(default, rename = "type")]
    pub r#type: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub state: String,
    /// Owner is the key of the organisational unit owning the node.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub owner: String,
    #[serde(
        default,
        rename = "props",
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_map"
    )]
    pub properties: Map<String, Value>,
    #[serde(
        default,
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_vec"
    )]
    pub links: Vec<SnapshotLink>,
    /// Provenance says where the version exported comes from; informative, an import ignores it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub provenance: Option<SnapshotProvenance>,
}

/// SnapshotProvenance is the origin of the version of a node of a snapshot: the change that wrote it, the action or
/// person that produced it, and whether the run that produced it called a model, the machine-readable mark of content
/// an AI generated (EU AI Act Art. 50(2)). The whole story is the change's PROV-O export (ADR 0057).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct SnapshotProvenance {
    #[serde(default)]
    pub version: i64,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub change: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub produced_by: String,
    /// Model is true when the action run that produced the version called a model.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub model: bool,
}

/// SnapshotLink is an outgoing link of a node of a snapshot, to another node of the snapshot named by its key.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SnapshotLink {
    #[serde(default, rename = "type")]
    pub r#type: String,
    #[serde(default)]
    pub to: String,
    #[serde(
        default,
        rename = "props",
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_map"
    )]
    pub properties: Map<String, Value>,
}

fn null_vec<'de, D: serde::Deserializer<'de>, T: Deserialize<'de>>(
    d: D,
) -> std::result::Result<Vec<T>, D::Error> {
    Ok(Option::<Vec<T>>::deserialize(d)?.unwrap_or_default())
}

fn null_map<'de, D: serde::Deserializer<'de>>(
    d: D,
) -> std::result::Result<Map<String, Value>, D::Error> {
    Ok(Option::<Map<String, Value>>::deserialize(d)?.unwrap_or_default())
}

/// The gate an import asks for every node type it writes (see [`SnapshotImport::gate`]).
pub type SnapshotGate = Arc<dyn Fn(&Change, &str) -> Result<()> + Send + Sync>;

/// SnapshotImport describes the import of a snapshot.
#[derive(Clone, Default)]
pub struct SnapshotImport {
    pub snapshot: Snapshot,
    /// Title, Intent and Methodology of the change the import creates (a default title and intent when empty).
    pub title: String,
    pub intent: String,
    pub methodology: String,
    /// OwnerOrg and ProjectID are the unit holding the change and the project it acts in (the project is required).
    pub owner_org: String,
    pub project_id: String,
    /// By names the producer of the change impacts.
    pub by: String,
    /// Gate, when set, is asked for every node type the import writes, once the change exists (authorization of the
    /// edges): an error stops the import, and the change is purged.
    pub gate: Option<SnapshotGate>,
}

/// SnapshotImportResult is the outcome of an import: the change it created (empty when the graph already held the
/// snapshot: nothing to import), what it did and what it could not do.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SnapshotImportResult {
    pub change: ChangeID,
    pub created: usize,
    pub updated: usize,
    pub unchanged: usize,
    /// Warnings are what the import left out (an owner unknown here, a link to a node found nowhere, a lifecycle
    /// state not reached by one transition).
    pub warnings: Vec<String>,
}

/// ParseSnapshot decodes a JSON snapshot and checks its shape: the format, a namespace, the keys unique, every type of
/// the namespace, every link to a node of the snapshot or of the graph (checked at import).
pub fn parse_snapshot(data: &[u8]) -> Result<Snapshot> {
    let s: Snapshot =
        serde_json::from_slice(data).map_err(|e| invalidf(format!("parse snapshot: {e}")))?;
    s.check()?;
    Ok(s)
}

impl Snapshot {
    /// check checks the shape of a snapshot.
    pub fn check(&self) -> Result<()> {
        if self.format != SNAPSHOT_FORMAT {
            return Err(invalidf(format!(
                "unknown snapshot format {:?} (expected {SNAPSHOT_FORMAT:?})",
                self.format
            )));
        }
        if self.namespace.is_empty() {
            return Err(invalidf("a snapshot names its namespace"));
        }
        let mut seen = HashSet::new();
        for (i, n) in self.nodes.iter().enumerate() {
            if n.key.is_empty() || n.r#type.is_empty() {
                return Err(invalidf(format!("node {i}: a key and a type are required")));
            }
            if !seen.insert(n.key.as_str()) {
                return Err(invalidf(format!("node {} appears twice", n.key)));
            }
            let ok = parse_type_ref(&n.r#type)
                .is_ok_and(|q| q.namespace.is_empty() || q.namespace == self.namespace);
            if !ok {
                return Err(invalidf(format!(
                    "node {}: type {:?} is not a type of namespace {}",
                    n.key, n.r#type, self.namespace
                )));
            }
            if n.links
                .iter()
                .any(|l| l.r#type.is_empty() || l.to.is_empty())
            {
                return Err(invalidf(format!(
                    "node {}: a link needs a type and a target",
                    n.key
                )));
            }
        }
        Ok(())
    }
}

impl Graph {
    /// ExportSnapshot reads the state of a namespace at a baseline (empty: the head of the branch, main by default) as
    /// a snapshot. The links kept are the ones between nodes of the namespace: a link to another namespace is left
    /// out.
    pub async fn export_snapshot(
        &self,
        ctx: &Ctx,
        namespace: &str,
        branch: &str,
        baseline: &BaselineID,
    ) -> Result<Snapshot> {
        let namespace = namespace_of(namespace).to_string();
        let mut s = Snapshot {
            format: SNAPSHOT_FORMAT.into(),
            namespace: namespace.clone(),
            branch: branch_of(branch).to_string(),
            exported_at: self.now(),
            ..Default::default()
        };
        let baseline = if baseline.is_empty() {
            self.branch_head(ctx, &namespace, &s.branch).await?.id
        } else {
            let b = self.baseline(ctx, baseline).await?;
            if b.namespace != namespace {
                return Err(invalidf(format!(
                    "baseline {baseline} is a baseline of {}, not {namespace}",
                    b.namespace
                )));
            }
            s.branch = branch_of(&b.branch).to_string();
            baseline.clone()
        };
        s.baseline = baseline.to_string();
        if baseline.is_empty() {
            return Ok(s); // nothing landed yet: the empty state
        }
        let (nodes, links) = self.baseline_graph(ctx, &baseline).await?;
        let keys: HashMap<&NodeID, &str> = nodes.iter().map(|n| (&n.id, n.key.as_str())).collect();
        let mut out: HashMap<NodeID, Vec<SnapshotLink>> = HashMap::new();
        for l in &links {
            let Some(to) = keys.get(&l.to.id) else {
                continue;
            };
            out.entry(l.from.id.clone())
                .or_default()
                .push(SnapshotLink {
                    r#type: l.r#type.clone(),
                    to: to.to_string(),
                    properties: l.properties.clone(),
                });
        }
        let mut owners = KeyReader::default();
        let mut provenance = ProvenanceReader::default();
        for n in &nodes {
            if n.deleted {
                continue;
            }
            let mut sn = SnapshotNode {
                key: n.key.clone(),
                r#type: n.r#type.clone(),
                state: n.state.clone(),
                properties: n.properties.clone(),
                links: out.remove(&n.id).unwrap_or_default(),
                provenance: provenance.of(self, ctx, n).await,
                ..Default::default()
            };
            if !n.owner.is_empty() {
                sn.owner = owners.key(self, ctx, &n.owner).await;
            }
            sn.links
                .sort_by(|a, b| a.r#type.cmp(&b.r#type).then_with(|| a.to.cmp(&b.to)));
            s.nodes.push(sn);
        }
        s.nodes.sort_by(|a, b| a.key.cmp(&b.key));
        Ok(s)
    }

    /// ImportSnapshot imports a snapshot into the main branch of its namespace as one change (ADR 0104): a node of the
    /// snapshot the graph does not hold is created, one it holds is checked out and brought to the snapshot
    /// (properties, owner, lifecycle state, outgoing links to the nodes of the namespace), one equal to the snapshot
    /// is left alone, and the nodes the snapshot does not name are untouched (a node is never deleted). The change
    /// stays active with its impacts proposed: it is reviewed, committed and merged like any other. A failed import
    /// leaves no change behind.
    pub async fn import_snapshot(
        &self,
        ctx: &Ctx,
        input: SnapshotImport,
    ) -> Result<SnapshotImportResult> {
        let s = &input.snapshot;
        s.check()?;
        if s.nodes.is_empty() {
            return Err(invalidf("the snapshot holds no node"));
        }
        let ns = s.namespace.clone();
        let mut res = SnapshotImportResult::default();
        let head = self.branch_head(ctx, &ns, MAIN_BRANCH).await?;
        let (nodes, links) = if head.id.is_empty() {
            (Vec::new(), Vec::new())
        } else {
            self.baseline_graph(ctx, &head.id).await?
        };
        let mut by_key: HashMap<String, Node> = HashMap::new();
        let mut key_of: HashMap<NodeID, String> = HashMap::new();
        for n in nodes {
            key_of.insert(n.id.clone(), n.key.clone());
            by_key.insert(n.key.clone(), n);
        }
        let mut out_of: HashMap<NodeID, Vec<Link>> = HashMap::new();
        for l in links {
            out_of.entry(l.from.id.clone()).or_default().push(l);
        }
        let in_snapshot: HashSet<&str> = s.nodes.iter().map(|n| n.key.as_str()).collect();
        let _ = &in_snapshot;
        let mut owners = KeyReader::default();
        let org_ns = self.structure(STRUCTURE_ORGANISATION).namespace;
        let mut known_unit: HashMap<String, bool> = HashMap::new();

        // what each node of the snapshot asks: a creation, a modification of the node held, or nothing
        let mut plans: Vec<Plan> = Vec::new();
        for sn in &s.nodes {
            let mut p = Plan {
                sn: sn.clone(),
                ..Default::default()
            };
            let mut owner = sn.owner.clone();
            if !owner.is_empty() {
                let ok = match known_unit.get(&owner) {
                    Some(ok) => *ok,
                    None => {
                        let ok = self.node_by_key(ctx, &org_ns, &owner).await.is_ok();
                        known_unit.insert(owner.clone(), ok);
                        ok
                    }
                };
                if !ok {
                    res.warnings.push(format!(
                        "{}: owner {owner} is not a unit of this graph, left to the unit of the change",
                        sn.key
                    ));
                    owner = String::new();
                }
            }
            if let Some(n) = by_key.get(&sn.key) {
                if n.r#type != sn.r#type {
                    return Err(invalidf(format!(
                        "node {} is a {} here, the snapshot has a {}",
                        sn.key, n.r#type, sn.r#type
                    )));
                }
                p.props = props_patch(&n.properties, &sn.properties);
                if !owner.is_empty() && owners.key(self, ctx, &n.owner).await != owner {
                    p.owner = owner;
                }
                p.state = !sn.state.is_empty() && sn.state != n.state;
                p.links = !same_links(
                    out_of.get(&n.id).map(Vec::as_slice).unwrap_or_default(),
                    &key_of,
                    &sn.links,
                );
                p.node = Some(n.clone());
                if p.props.is_empty() && p.owner.is_empty() && !p.state && !p.links {
                    res.unchanged += 1;
                    continue;
                }
            } else {
                p.owner = owner;
            }
            plans.push(p);
        }
        if plans.is_empty() {
            return Ok(res);
        }
        let title = if input.title.is_empty() {
            format!("Import of a snapshot of {ns}")
        } else {
            input.title.clone()
        };
        let intent = if input.intent.is_empty() {
            format!(
                "Bring {ns} to the snapshot exported at {}",
                s.exported_at.to_rfc3339_opts(SecondsFormat::Secs, true)
            )
        } else {
            input.intent.clone()
        };
        let c = self
            .create_change(
                ctx,
                NewChange {
                    namespace: ns.clone(),
                    title,
                    intent,
                    methodology: input.methodology.clone(),
                    own_branch: true,
                    owner_org: input.owner_org.clone(),
                    project_id: input.project_id.clone(),
                    ..Default::default()
                },
            )
            .await?;
        let done = self
            .import_plans(ctx, &c, &input, plans, &by_key, &mut key_of, &mut res)
            .await;
        if let Err(err) = done {
            if self.purge_change(ctx, &c.id).await.is_err() {
                let patch = ChangePatch {
                    status: Some(CHANGE_ABANDONED),
                    ..Default::default()
                };
                if let Err(aerr) = self.update_change(ctx, &c.id, &patch).await {
                    return Err(Error::new(err.kind(), format!("{err}\n{aerr}")));
                }
            }
            return Err(err);
        }
        res.change = c.id;
        Ok(res)
    }

    /// importPlans writes the plans of an import on the change it opened.
    #[allow(clippy::too_many_arguments)]
    async fn import_plans(
        &self,
        ctx: &Ctx,
        c: &Change,
        input: &SnapshotImport,
        mut plans: Vec<Plan>,
        by_key: &HashMap<String, Node>,
        key_of: &mut HashMap<NodeID, String>,
        res: &mut SnapshotImportResult,
    ) -> Result<()> {
        let ns = &input.snapshot.namespace;
        if let Some(gate) = &input.gate {
            let mut types = HashSet::new();
            for p in &plans {
                if types.insert(p.sn.r#type.clone()) {
                    gate(c, &p.sn.r#type)?;
                }
            }
        }
        let why = "imported from a snapshot";
        // first the nodes, created or checked out, so that the links may name any of them
        for p in &mut plans {
            let Some(node) = &p.node else {
                let cn: ChangeImpact = self
                    .impact_node_create(
                        ctx,
                        &c.id,
                        NodeCreate {
                            key: p.sn.key.clone(),
                            r#type: p.sn.r#type.clone(),
                            properties: p.sn.properties.clone(),
                            owner: p.owner.clone(),
                            rationale: why.into(),
                            produced_by: input.by.clone(),
                            ..Default::default()
                        },
                    )
                    .await
                    .map_err(|e| e.context(&p.sn.key))?;
                p.impact = cn.id;
                p.draft_id = cn.post.map(|r| r.id).unwrap_or_default();
                key_of.insert(p.draft_id.clone(), p.sn.key.clone());
                res.created += 1;
                continue;
            };
            let cn = self
                .impact_node_checkout(
                    ctx,
                    &c.id,
                    NodeCheckout {
                        node: node.id.clone(),
                        rationale: why.into(),
                        produced_by: input.by.clone(),
                        ..Default::default()
                    },
                )
                .await
                .map_err(|e| e.context(&p.sn.key))?;
            p.impact = cn.id.clone();
            p.draft_id = node.id.clone();
            if !p.props.is_empty() || !p.owner.is_empty() {
                self.impact_node_update(
                    ctx,
                    &c.id,
                    &cn.id,
                    NodeUpdate {
                        properties: p.props.clone(),
                        owner: p.owner.clone(),
                        ..Default::default()
                    },
                )
                .await
                .map_err(|e| e.context(&p.sn.key))?;
            }
            res.updated += 1;
        }
        // the target of a link: a node of the import (its draft), else a node the graph holds in the namespace
        let ref_of: HashMap<String, NodeRef> = plans
            .iter()
            .map(|p| {
                (
                    p.sn.key.clone(),
                    NodeRef {
                        id: p.draft_id.clone(),
                        version: 0,
                    },
                )
            })
            .collect();
        let target = |key: &str| -> Option<NodeRef> {
            ref_of
                .get(key)
                .cloned()
                .or_else(|| by_key.get(key).map(Node::r#ref))
        };
        for p in &plans {
            if p.node.is_some() && !p.links {
                continue;
            }
            let held = if p.node.is_some() {
                self.change_node_view(
                    ctx,
                    &c.id,
                    "",
                    &NodeRef {
                        id: p.draft_id.clone(),
                        version: 0,
                    },
                )
                .await
                .map_err(|e| e.context(&p.sn.key))?
                .out
            } else {
                Vec::new()
            };
            let mut matched = vec![false; held.len()];
            for l in &p.sn.links {
                let Some(to) = target(&l.to) else {
                    res.warnings.push(format!(
                        "{}: link {} to {} left out: no such node, neither in the snapshot nor in {ns}",
                        p.sn.key, l.r#type, l.to
                    ));
                    continue;
                };
                let mut found = false;
                for (i, h) in held.iter().enumerate() {
                    if matched[i] || h.r#type != l.r#type || h.to.id != to.id {
                        continue;
                    }
                    matched[i] = true;
                    found = true;
                    if !same_props(&h.properties, &l.properties) {
                        self.impact_link_update(ctx, &c.id, &h.id, l.properties.clone(), "", "")
                            .await
                            .map_err(|e| {
                                e.context(format!("{}: link {} to {}", p.sn.key, l.r#type, l.to))
                            })?;
                    }
                    break;
                }
                if found {
                    continue;
                }
                self.impact_link_create(
                    ctx,
                    &c.id,
                    &p.impact,
                    LinkWrite {
                        r#type: l.r#type.clone(),
                        to,
                        properties: l.properties.clone(),
                    },
                    "",
                    "",
                )
                .await
                .map_err(|e| e.context(format!("{}: link {} to {}", p.sn.key, l.r#type, l.to)))?;
            }
            for (i, h) in held.iter().enumerate() {
                // a link to a node outside the namespace is not part of a snapshot: it stays
                if matched[i] || !key_of.contains_key(&h.to.id) {
                    continue;
                }
                self.impact_link_delete(ctx, &c.id, &h.id, "", "")
                    .await
                    .map_err(|e| e.context(format!("{}: link {}", p.sn.key, h.r#type)))?;
            }
        }
        // the lifecycle states last, over the drafts: one transition, else a warning
        for p in &plans {
            if p.sn.state.is_empty() {
                continue;
            }
            let cur = self
                .change_node(
                    ctx,
                    &c.id,
                    "",
                    &NodeRef {
                        id: p.draft_id.clone(),
                        version: 0,
                    },
                )
                .await
                .map_err(|e| e.context(&p.sn.key))?;
            if cur.state == p.sn.state {
                continue;
            }
            let t = NodeTransition {
                checkout: NodeCheckout {
                    impact: p.impact.clone(),
                    ..Default::default()
                },
                to: p.sn.state.clone(),
            };
            if let Err(err) = self.impact_node_transition(ctx, &c.id, t).await {
                if err.is_invalid() || err.is_conflict() {
                    res.warnings.push(format!(
                        "{}: state {} not reached from {} ({err})",
                        p.sn.key, p.sn.state, cur.state
                    ));
                    continue;
                }
                return Err(err.context(&p.sn.key));
            }
        }
        Ok(())
    }
}

/// plan is what a node of a snapshot asks of an import.
#[derive(Default)]
struct Plan {
    sn: SnapshotNode,
    /// the node held (None: created)
    node: Option<Node>,
    props: Map<String, Value>,
    owner: String,
    state: bool,
    links: bool,
    impact: ChangeImpactID,
    draft_id: NodeID,
}

/// keyReader returns the key of a node by its id, cached; "" when it cannot be read.
#[derive(Default)]
struct KeyReader {
    cache: HashMap<NodeID, String>,
}

impl KeyReader {
    async fn key(&mut self, g: &Graph, ctx: &Ctx, id: &NodeID) -> String {
        if let Some(k) = self.cache.get(id) {
            return k.clone();
        }
        let k = g
            .node(
                ctx,
                &NodeRef {
                    id: id.clone(),
                    version: 0,
                },
            )
            .await
            .map(|n| n.key)
            .unwrap_or_default();
        self.cache.insert(id.clone(), k.clone());
        k
    }
}

/// provenanceReader returns the provenance of a version, reading the impacts and the model calls of each change once.
#[derive(Default)]
struct ProvenanceReader {
    cache: HashMap<ChangeID, Origin>,
}

#[derive(Default)]
struct Origin {
    impacts: BTreeMap<ChangeImpactID, ChangeImpact>,
    /// the executions that called a model
    models: HashSet<String>,
}

impl ProvenanceReader {
    async fn of(&mut self, g: &Graph, ctx: &Ctx, n: &Node) -> Option<SnapshotProvenance> {
        if n.change_id.is_empty() {
            return None;
        }
        let mut p = SnapshotProvenance {
            version: n.version,
            change: n.change_id.to_string(),
            ..Default::default()
        };
        if !n.change_impact.is_empty() {
            if !self.cache.contains_key(&n.change_id) {
                let o = read_origin(g, ctx, &n.change_id).await.unwrap_or_default();
                self.cache.insert(n.change_id.clone(), o);
            }
            let o = &self.cache[&n.change_id];
            if let Some(cn) = o.impacts.get(&n.change_impact) {
                p.produced_by = cn.produced_by.clone();
                p.model = !cn.execution.is_empty() && o.models.contains(&cn.execution);
            }
        }
        Some(p)
    }
}

async fn read_origin(g: &Graph, ctx: &Ctx, id: &ChangeID) -> Result<Origin> {
    let mut o = Origin::default();
    let mut tx = g.begin(ctx).await?;
    for cn in tx.change_impacts(ctx, id).await? {
        o.impacts.insert(cn.id.clone(), cn);
    }
    let entries = tx
        .log(
            ctx,
            &LogFilter {
                change: id.clone(),
                types: vec![format!("{LOG_MODEL}.")],
                ..Default::default()
            },
        )
        .await?;
    for e in entries {
        if !e.execution.is_empty() {
            o.models.insert(e.execution);
        }
    }
    tx.commit(ctx).await?;
    Ok(o)
}

/// propsPatch is the patch bringing the properties held to the wanted ones: the values that differ, null for the ones
/// the wanted leave out.
fn props_patch(held: &Map<String, Value>, want: &Map<String, Value>) -> Map<String, Value> {
    let mut patch = Map::new();
    for (k, v) in want {
        if held.get(k).is_none_or(|hv| !same_value(hv, v)) {
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

/// sameLinks tells whether the outgoing links held to nodes of the namespace are the wanted ones (type, target key and
/// properties, as multisets).
fn same_links(held: &[Link], key_of: &HashMap<NodeID, String>, want: &[SnapshotLink]) -> bool {
    let h: Vec<SnapshotLink> = held
        .iter()
        .filter_map(|l| {
            key_of.get(&l.to.id).map(|k| SnapshotLink {
                r#type: l.r#type.clone(),
                to: k.clone(),
                properties: l.properties.clone(),
            })
        })
        .collect();
    if h.len() != want.len() {
        return false;
    }
    let mut used = vec![false; h.len()];
    for w in want {
        let found = h.iter().enumerate().position(|(i, x)| {
            !used[i]
                && x.r#type == w.r#type
                && x.to == w.to
                && same_props(&x.properties, &w.properties)
        });
        match found {
            Some(i) => used[i] = true,
            None => return false,
        }
    }
    true
}

fn same_props(a: &Map<String, Value>, b: &Map<String, Value>) -> bool {
    same_value(&Value::Object(a.clone()), &Value::Object(b.clone()))
}

/// sameValue compares two values as JSON would carry them (numbers by value, an empty map as no map).
fn same_value(a: &Value, b: &Value) -> bool {
    fn norm(v: &Value) -> &Value {
        match v {
            Value::Object(m) if m.is_empty() => &Value::Null,
            v => v,
        }
    }
    json_equal(norm(a), norm(b))
}
