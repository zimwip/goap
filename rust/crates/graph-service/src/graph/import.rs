//! The import of a snapshot (Go's `ImportSnapshot`, ADR 0104): one new change on main bringing the namespace to the
//! snapshot, each node created, or checked out and brought to it, its links, then its lifecycle state.
//!
//! The Go service runs each step as an operation of the graph in a transaction of its own and purges the change when
//! one fails. They run here in one transaction, each step a transaction of the Go service within it
//! ([`Tx::checkpoint`]): a step that fails is undone alone, the steps before it stay and the change is purged after
//! them, as there; a transition refused is undone alone and warned about.
use std::collections::{HashMap, HashSet};

use super::*;
use graph_catalog::catalog::STRUCTURE_ORGANISATION;
use graph_core::{
    model::Target,
    snapshot::{self, Snapshot, SnapshotNode},
    view::View,
    write::work::LinkWrite,
};

/// The rationale of the impacts an import writes.
const WHY: &str = "imported from a snapshot";

/// What a node of the snapshot asks: a creation, or a modification of the node held.
struct Plan<'a> {
    sn: &'a SnapshotNode,
    /// the node held; none: created
    node: Option<graph_core::model::Node>,
    props: serde_json::Map<String, serde_json::Value>,
    owner: String,
    links: bool,
    impact: String,
    draft: NodeId,
}

/// The outcome of an import.
#[derive(Default)]
struct Imported {
    change: Option<ChangeId>,
    created: i32,
    updated: i32,
    unchanged: i32,
    warnings: Vec<String>,
}

impl Graph {
    pub(super) async fn import_graph_rpc(
        &self,
        caller: Caller,
        m: pb::ImportGraphRequest,
    ) -> Answer<pb::ImportGraphResponse> {
        let Some(parsed) = snapshot::parse_snapshot(&m.document) else {
            return Err(connect_error(GraphError::unimplemented(
                "a snapshot the decoder refuses, in Go's words: answered by the Go service",
            )));
        };
        let s = parsed.map_err(connect_error)?;
        let token_project = caller.project.clone();
        let by = caller.subject.clone();
        let (owner, stamp) = match self.resolve_owner(&caller, &m.owner_org).await {
            Ok(v) => v,
            Err((e, stamp)) => return stamp.on(Err(e)),
        };
        stamp.on(self
            .write_on(caller, &[], async |tx, acc| {
                platform_served(tx, &s.namespace)?;
                let project = if !m.project_id.is_empty() {
                    m.project_id.clone()
                } else if !token_project.is_empty() {
                    token_project.clone()
                } else {
                    tx.structure_root(STRUCTURE_PROJECT)?
                };
                let r = match import(tx, acc, &s, &m, &owner, &project, &by).await? {
                    Ok(r) => r,
                    Err(e) => return Ok(Err(e)),
                };
                let change = match &r.change {
                    Some(id) => Some(whole_pb(&whole::change_at(tx.store, id, i64::MAX).await?)?),
                    None => None,
                };
                Ok(Ok(pb::ImportGraphResponse {
                    change,
                    created: r.created,
                    updated: r.updated,
                    unchanged: r.unchanged,
                    warnings: r.warnings,
                }))
            })
            .await
            .and_then(|res| match res.msg {
                Ok(msg) => Ok(Response {
                    headers: res.headers,
                    trailers: res.trailers,
                    msg,
                }),
                Err(err) => {
                    let mut e = connect_error(err);
                    e.meta_mut().extend(res.headers);
                    Err(e)
                }
            }))
    }
}

async fn import(
    tx: &mut Tx<'_, Session>,
    acc: &Access,
    s: &Snapshot,
    m: &pb::ImportGraphRequest,
    owner: &str,
    project: &str,
    by: &str,
) -> Result<Result<Imported, GraphError>, GraphError> {
    let mut res = Imported::default();
    if s.nodes.is_empty() {
        return Err(GraphError::invalid("the snapshot holds no node"));
    }
    let ns = s.namespace.as_str();
    let head = read::branch_head(tx.store, ns, MAIN_BRANCH).await?;
    let (nodes, links) = snapshot::baseline_graph(tx.store, &head).await?;
    let mut key_of: HashMap<NodeId, String> = nodes.iter().map(|n| (n.id.clone(), n.key.clone())).collect();
    let mut out_of: HashMap<NodeId, Vec<graph_core::model::Link>> = HashMap::new();
    for l in links {
        out_of.entry(l.from.id.clone()).or_default().push(l);
    }
    let by_key: HashMap<String, graph_core::model::Node> = nodes.into_iter().map(|n| (n.key.clone(), n)).collect();

    // an owner is a unit of this graph: a URL, or the key of a snapshot of before (ADR 0154 §9)
    let org_ns = tx
        .catalog()
        .structure(STRUCTURE_ORGANISATION)
        .map(|st| st.namespace.clone())
        .unwrap_or_default();
    let mut known: HashMap<String, bool> = HashMap::new();
    let mut plans = Vec::new();
    for sn in &s.nodes {
        let mut owner = sn.owner.clone();
        if !owner.is_empty() && !is_unit(tx, &org_ns, &mut known, &owner).await? {
            res.warnings.push(format!(
                "{}: owner {owner} is not a unit of this graph, left to the unit of the change",
                sn.key
            ));
            owner.clear();
        }
        let mut p = Plan {
            sn,
            node: None,
            props: serde_json::Map::new(),
            owner: String::new(),
            links: false,
            impact: String::new(),
            draft: NodeId::default(),
        };
        if let Some(n) = by_key.get(&sn.key) {
            if n.typ != sn.typ {
                return Err(GraphError::invalid(format!(
                    "node {} is a {} here, the snapshot has a {}",
                    sn.key, n.typ, sn.typ
                )));
            }
            p.props = snapshot::props_patch(&n.props, &sn.props);
            if !owner.is_empty() && !n.owner.names(&owner) {
                p.owner = owner;
            }
            let state = !sn.state.is_empty() && sn.state != n.state;
            p.links = !same_links(out_of.get(&n.id).map(Vec::as_slice).unwrap_or_default(), &key_of, sn);
            if p.props.is_empty() && p.owner.is_empty() && !state && !p.links {
                res.unchanged += 1;
                continue;
            }
            p.node = Some(n.clone());
        } else {
            p.owner = owner;
        }
        plans.push(p);
    }
    if plans.is_empty() {
        return Ok(Ok(res));
    }

    let title = if m.title.is_empty() {
        format!("Import of a snapshot of {ns}")
    } else {
        m.title.clone()
    };
    let intent = if m.intent.is_empty() {
        format!(
            "Bring {ns} to the snapshot exported at {}",
            snapshot::rfc3339(&s.exported_at)
        )
    } else {
        m.intent.clone()
    };
    let c = tx
        .create_change(NewChange {
            namespace: ns.to_string(),
            title,
            intent,
            methodology: m.methodology.clone(),
            own_branch: true,
            owner_org: owner.to_string(),
            project_id: project.to_string(),
            ..NewChange::default()
        })
        .await?;
    tx.checkpoint().await?;
    match fill(tx, acc, &c, &mut plans, &mut key_of, &by_key, by, &mut res.warnings).await {
        Ok(()) => {}
        Err(e) if e.kind == Kind::Unimplemented => return Err(e),
        // the steps before the one refused stay, as in the Go service, which then purges the change it made
        Err(e) => {
            if let Err(pe) = tx.purge_change(&c.id).await {
                if pe.kind == Kind::Conflict {
                    return Err(GraphError::unimplemented(
                        "an import failing on a change it cannot purge, abandoned by the Go service",
                    ));
                }
                return Err(pe);
            }
            tx.checkpoint().await?;
            return Ok(Err(e));
        }
    }
    res.created = plans.iter().filter(|p| p.node.is_none()).count() as i32;
    res.updated = plans.len() as i32 - res.created;
    res.change = Some(c.id);
    Ok(Ok(res))
}

/// Runs an operation of the graph the Go service runs in a transaction of its own: kept once it succeeds, undone alone
/// when it fails, its error told about what it wrote.
macro_rules! step {
    ($tx:expr, $what:expr, $op:expr) => {{
        let r = $tx.rehearse().await?;
        match $op {
            Ok(v) => {
                $tx.checkpoint().await?;
                v
            }
            Err(e) => {
                $tx.rewind(r).await?;
                return Err(e.context($what));
            }
        }
    }};
}

/// Writes the plans in the change: the gate of each type, the nodes, their links, their states.
#[allow(clippy::too_many_arguments)]
async fn fill(
    tx: &mut Tx<'_, Session>,
    acc: &Access,
    c: &change::Change,
    plans: &mut [Plan<'_>],
    key_of: &mut HashMap<NodeId, String>,
    by_key: &HashMap<String, graph_core::model::Node>,
    by: &str,
    warnings: &mut Vec<String>,
) -> Result<(), GraphError> {
    let id = c.id.clone();
    let mut types = HashSet::new();
    for p in plans.iter() {
        if types.insert(p.sn.typ.as_str()) {
            acc.check_create(c, tx.catalog(), &p.sn.typ)?;
        }
    }
    // first the nodes, created or checked out, so that the links may name any of them
    for p in plans.iter_mut() {
        match &p.node {
            None => {
                acc.allow(tx, id.as_str(), operations::IMPACT_CREATE)
                    .await
                    .map_err(|e| e.context(&p.sn.key))?;
                let cn = step!(
                    tx,
                    &p.sn.key,
                    tx.impact_node_create(
                        &id,
                        NodeCreate {
                            key: p.sn.key.clone(),
                            typ: p.sn.typ.clone(),
                            props: p.sn.props.clone(),
                            owner: p.owner.clone(),
                            rationale: WHY.into(),
                            produced_by: by.to_string(),
                            ..NodeCreate::default()
                        },
                        "",
                    )
                    .await
                );
                p.impact = cn.id.clone();
                p.draft = cn.post.map(|r| r.id).unwrap_or_default();
                key_of.insert(p.draft.clone(), p.sn.key.clone());
            }
            Some(n) => {
                acc.allow(tx, id.as_str(), operations::IMPACT_CHECKOUT)
                    .await
                    .map_err(|e| e.context(&p.sn.key))?;
                let node = NodeCheckout {
                    node: n.id.clone(),
                    rationale: WHY.into(),
                    produced_by: by.to_string(),
                    ..NodeCheckout::default()
                };
                let cn = step!(tx, &p.sn.key, tx.impact_node_checkout(&id, "", vec![node]).await)
                    .into_iter()
                    .next()
                    .ok_or_else(|| GraphError::internal("a checkout answered no impact"))?;
                p.impact = cn.id.clone();
                p.draft = n.id.clone();
                if !p.props.is_empty() || !p.owner.is_empty() {
                    acc.allow(tx, id.as_str(), operations::IMPACT_UPDATE)
                        .await
                        .map_err(|e| e.context(&p.sn.key))?;
                    step!(
                        tx,
                        &p.sn.key,
                        tx.impact_node_update(&id, &cn.id, p.props.clone(), &p.owner, "", "")
                            .await
                    );
                }
            }
        }
    }
    // the target of a link: a node of the import (its draft), else a node the graph holds in the namespace
    let ref_of: HashMap<&str, NodeRef> = plans
        .iter()
        .map(|p| (p.sn.key.as_str(), NodeRef::new(p.draft.clone(), 0)))
        .collect();
    let target = |key: &str| {
        ref_of
            .get(key)
            .cloned()
            .or_else(|| by_key.get(key).map(|n| n.node_ref()))
    };
    let view = View {
        change: id.clone(),
        ..View::default()
    };
    for p in plans.iter() {
        if p.node.is_some() && !p.links {
            continue;
        }
        let held = if p.node.is_some() {
            let w = World::open(tx.store, &view).await.map_err(|e| e.context(&p.sn.key))?;
            w.node_and_links(&NodeRef::new(p.draft.clone(), 0))
                .await
                .map_err(|e| e.context(&p.sn.key))?
                .1
        } else {
            Vec::new()
        };
        let mut matched = vec![false; held.len()];
        for l in &p.sn.links {
            let Some(to) = target(&l.to) else {
                warnings.push(format!(
                    "{}: link {} to {} left out: no such node, neither in the snapshot nor in {}",
                    p.sn.key, l.typ, l.to, c.namespace
                ));
                continue;
            };
            let found = held
                .iter()
                .enumerate()
                .find(|(i, h)| !matched[*i] && h.typ == l.typ && h.to_node().is_some_and(|t| t.id == to.id));
            if let Some((i, h)) = found {
                matched[i] = true;
                if !snapshot::same_props(&h.props, &l.props) {
                    acc.allow(tx, id.as_str(), operations::IMPACT_LINK_UPDATE)
                        .await
                        .map_err(|e| e.context(format!("{}: link {} to {}", p.sn.key, l.typ, l.to)))?;
                    step!(
                        tx,
                        format!("{}: link {} to {}", p.sn.key, l.typ, l.to),
                        tx.impact_link_update(&id, &h.id, l.props.clone(), "", "").await
                    );
                }
                continue;
            }
            acc.allow(tx, id.as_str(), operations::IMPACT_LINK_CREATE)
                .await
                .map_err(|e| e.context(format!("{}: link {} to {}", p.sn.key, l.typ, l.to)))?;
            let w = LinkWrite {
                typ: l.typ.clone(),
                to,
                target: Url::default(),
                props: l.props.clone(),
            };
            step!(
                tx,
                format!("{}: link {} to {}", p.sn.key, l.typ, l.to),
                tx.impact_link_create(&id, &p.impact, w, "", "").await
            );
        }
        for (i, h) in held.iter().enumerate() {
            // a link to a node outside the namespace is not part of a snapshot: it stays
            let in_namespace = matches!(&h.to, Target::Node(t) if key_of.contains_key(&t.id));
            if matched[i] || !in_namespace {
                continue;
            }
            acc.allow(tx, id.as_str(), operations::IMPACT_LINK_DELETE)
                .await
                .map_err(|e| e.context(format!("{}: link {}", p.sn.key, h.typ)))?;
            step!(
                tx,
                format!("{}: link {}", p.sn.key, h.typ),
                tx.impact_link_delete(&id, &h.id, "", "").await
            );
        }
    }
    // the lifecycle states last, over the drafts: one transition, else a warning
    for p in plans.iter() {
        if p.sn.state.is_empty() {
            continue;
        }
        let w = World::open(tx.store, &view).await.map_err(|e| e.context(&p.sn.key))?;
        let cur = w
            .node_and_links(&NodeRef::new(p.draft.clone(), 0))
            .await
            .map_err(|e| e.context(&p.sn.key))?
            .0
            .state;
        if cur == p.sn.state {
            continue;
        }
        acc.allow(tx, id.as_str(), operations::IMPACT_TRANSITION)
            .await
            .map_err(|e| e.context(&p.sn.key))?;
        let authorize = |node: &str, key: &str, t: &Transition| acc.transition(node, key, t);
        let r = tx.rehearse().await?;
        let n = NodeCheckout {
            impact: p.impact.clone(),
            ..NodeCheckout::default()
        };
        match tx.impact_node_transition(&id, "", n, &p.sn.state, &authorize).await {
            Ok(_) => tx.checkpoint().await?,
            Err(e) if matches!(e.kind, Kind::Invalid | Kind::Conflict) => {
                tx.rewind(r).await?;
                warnings.push(format!(
                    "{}: state {} not reached from {cur} ({})",
                    p.sn.key, p.sn.state, e.message
                ));
            }
            Err(e) => {
                tx.rewind(r).await?;
                return Err(e.context(&p.sn.key));
            }
        }
    }
    Ok(())
}

/// Whether an owner names a unit of this graph (Go's `unit` of `ImportSnapshot`), each unit looked up once.
async fn is_unit(
    tx: &Tx<'_, Session>,
    org_ns: &str,
    known: &mut HashMap<String, bool>,
    owner: &str,
) -> Result<bool, GraphError> {
    let Ok(u) = tx.structure_ref(STRUCTURE_ORGANISATION, owner) else {
        return Ok(false);
    };
    if u.is_zero() || u.source != org_ns {
        return Ok(false);
    }
    if let Some(&ok) = known.get(&u.key) {
        return Ok(ok);
    }
    let ok = match tx.store.node_id_by_key(org_ns, &u.key).await? {
        Some(id) => tx.store.latest_on(&id, MAIN_BRANCH).await?.is_some(),
        None => false,
    };
    known.insert(u.key, ok);
    Ok(ok)
}

/// Whether the links a node holds to nodes of the namespace are the ones a snapshot wants (Go's `sameLinks`: type,
/// target key and properties, as multisets).
fn same_links(held: &[graph_core::model::Link], key_of: &HashMap<NodeId, String>, want: &SnapshotNode) -> bool {
    let h: Vec<(&str, &str, &graph_core::model::Props)> = held
        .iter()
        .filter_map(|l| {
            let k = key_of.get(&l.to_node()?.id)?;
            Some((l.typ.as_str(), k.as_str(), &l.props))
        })
        .collect();
    if h.len() != want.links.len() {
        return false;
    }
    let mut used = vec![false; h.len()];
    for w in &want.links {
        let Some(i) = (0..h.len())
            .find(|&i| !used[i] && h[i].0 == w.typ && h[i].1 == w.to && snapshot::same_props(h[i].2, &w.props))
        else {
            return false;
        };
        used[i] = true;
    }
    true
}
