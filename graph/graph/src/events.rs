//! Port of `events.go`: the events the graph publishes for what each transaction wrote (ADR 0026, 0053, 0095), once
//! it committed, and the republication of the whole graph to rebuild an index.
//!
//! Go's `observedRepo` / `observedTx` are the observer part of [`GraphTx`] (guard.rs): it records what a transaction
//! wrote when the graph has a sink ([`Graph::observe`]), and [`GraphTx::commit`] calls [`Graph::events_of`] inside the
//! transaction, then publishes once it committed.

use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{
    BaselineEvent, ChangeDocEvent, ChangeDocRef, ChangeEvent, MAIN_BRANCH, Node, NodeEvent, NodeID,
    NodeRef, SearchProperty, branch_of, namespace_of, subject_baseline_advanced,
    subject_change_indexed, subject_change_touched, subject_node_written,
};
use serde::Serialize;
use serde_json::{Map, Value};

use crate::guard::ObsState;
use crate::lifecycle::TypeIndex;
use crate::merge::branch_head;
use crate::repo::Tx;
use crate::{Graph, Result};

/// GraphEvent is one event the graph publishes: its JSON is the event's own (Go publishes the value itself).
#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(untagged)]
#[allow(clippy::large_enum_variant)] // events are built once and published, never stored in bulk
pub enum GraphEvent {
    /// "change.updated" on goap.changed.<id>.
    Change(ChangeEvent),
    /// The document of a change for the index, on goap.changeindex.<id>.
    ChangeDoc(ChangeDocEvent),
    /// A node version written, on goap.node.<ns>.<type>.<id>.written.
    Node(NodeEvent),
    /// A baseline created, on goap.baseline.<branch>.advanced.
    Baseline(BaselineEvent),
}

/// EventSink receives the events of the graph (the node index, ADR 0026; the event stream, ADR 0053). It is
/// satisfied by the engine's publisher.
#[async_trait]
pub trait EventSink: Send + Sync {
    async fn publish(&self, ctx: &Ctx, subject: &str, ev: &GraphEvent) -> Result<()>;
}

/// One event to publish and its subject.
#[derive(Debug, Clone, PartialEq)]
pub struct Published {
    pub subject: String,
    pub event: GraphEvent,
}

/// maxIndexedImpacts bounds the node keys a change document carries.
const MAX_INDEXED_IMPACTS: usize = 200;

impl Graph {
    /// Observe makes the graph publish a NodeEvent for every node version written (at landing, ADR 0079: a node in a
    /// change is a draft, no version) and a BaselineEvent for every baseline created, once the transaction that wrote
    /// them has committed, plus the change events (ADR 0053, 0095). It sees every write path. Publishing is best
    /// effort: the index rebuilds from the graph (Republish) when an event is lost.
    pub fn observe(&mut self, sink: Arc<dyn EventSink>) {
        self.sink = Some(sink);
    }

    /// eventsOf builds the events of what a transaction wrote, inside the transaction (it reads the node types).
    pub(crate) async fn events_of(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        ot: &ObsState,
    ) -> Result<Vec<Published>> {
        let mut out = Vec::new();
        for id in &ot.changes {
            let mut c = ot.headers.get(id).cloned().unwrap_or_default();
            c.id = id.clone();
            out.push(Published {
                subject: subject_change_touched(id),
                event: GraphEvent::Change(ChangeEvent {
                    r#type: "change.updated".into(),
                    change: c,
                    baseline: None,
                    items: Vec::new(),
                    fields: BTreeMap::new(),
                    end_user: String::new(),
                }),
            });
        }
        out.extend(self.change_docs(ctx, tx, ot).await?);
        if !ot.nodes.is_empty() {
            // the type catalogue is process-global, not namespace-scoped graph data: no baseline lookup
            let ix = self.types_at();
            let mut keys: HashMap<NodeID, String> = HashMap::new();
            for r in &ot.nodes {
                let n = tx.node(ctx, r).await?;
                let project = key_of(ctx, tx, &mut keys, &n.project).await;
                let owner = key_of(ctx, tx, &mut keys, &n.owner).await;
                let (text, facets) = ix.searchable(&n);
                let ev = NodeEvent {
                    id: n.id.clone(),
                    version: n.version,
                    branch: branch_of(&n.branch).to_string(),
                    namespace: namespace_of(&n.namespace).to_string(),
                    key: n.key.clone(),
                    r#type: n.r#type.clone(),
                    state: n.state.clone(),
                    deleted: n.deleted,
                    project,
                    owner,
                    change_id: n.change_id.clone(),
                    text,
                    facets,
                    time: n.created_at,
                };
                out.push(Published {
                    subject: subject_node_written(
                        &subject_token(&ev.namespace),
                        &subject_token(&n.r#type),
                        &n.id,
                    ),
                    event: GraphEvent::Node(ev),
                });
            }
        }
        for b in &ot.baselines {
            let mut ev = BaselineEvent {
                id: b.id.clone(),
                branch: branch_of(&b.branch).to_string(),
                parent: b.parent_id.clone(),
                time: b.created_at,
                ..Default::default()
            };
            let parent = if b.parent_id.is_empty() {
                BTreeMap::new()
            } else {
                tx.baseline(ctx, &b.parent_id).await?.nodes
            };
            for (id, v) in &b.nodes {
                if parent.get(id) != Some(v) {
                    ev.set.insert(id.clone(), *v);
                }
            }
            for id in parent.keys() {
                if !b.nodes.contains_key(id) {
                    ev.removed.push(id.clone());
                }
            }
            out.push(Published {
                subject: subject_baseline_advanced(&subject_token(&ev.branch)),
                event: GraphEvent::Baseline(ev),
            });
        }
        Ok(out)
    }

    /// changeDocs builds the index documents of the changes a transaction wrote or purged (ADR 0095), inside the
    /// transaction (it reads the impacts). Only the header and the impacts trigger it: a log entry alone does not
    /// change the document.
    pub(crate) async fn change_docs(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        ot: &ObsState,
    ) -> Result<Vec<Published>> {
        let mut out = Vec::new();
        for id in &ot.indexed {
            let c = match ot.headers.get(id) {
                Some(c) => c.clone(),
                // the impacts alone were written: the header is read
                None => tx.change(ctx, id).await?,
            };
            let mut ev = ChangeDocEvent {
                id: c.id.clone(),
                title: c.title.clone(),
                intent: c.intent.clone(),
                goal: c.goal.clone(),
                methodology: c.methodology.clone(),
                namespace: c.namespace.clone(),
                status: c.status.clone(),
                state: c.state.clone(),
                project_id: c.project_id.clone(),
                owner_org: c.owner_org.clone(),
                parent_id: c.parent_id.clone(),
                branch: c.branch.clone(),
                created_at: c.created_at,
                ..Default::default()
            };
            for cn in tx.change_impacts(ctx, id).await? {
                if ev.impacts.len() == MAX_INDEXED_IMPACTS {
                    break;
                }
                ev.impacts.push(ChangeDocRef {
                    key: cn.key.clone(),
                    r#type: cn.r#type.clone(),
                });
            }
            out.push(Published {
                subject: subject_change_indexed(id),
                event: GraphEvent::ChangeDoc(ev),
            });
        }
        for id in &ot.purged {
            out.push(Published {
                subject: subject_change_indexed(id),
                event: GraphEvent::ChangeDoc(ChangeDocEvent {
                    id: id.clone(),
                    deleted: true,
                    ..Default::default()
                }),
            });
        }
        Ok(out)
    }

    /// Republish publishes the node event of every node version (all branches, every namespace) and the head of each
    /// namespace's main as a baseline event, to rebuild an index (ADR 0026). It returns the number of documents
    /// published (node versions and changes).
    pub async fn republish(&self, ctx: &Ctx, sink: &dyn EventSink) -> Result<usize> {
        let mut tx = self.begin(ctx).await?;
        let namespaces = tx.namespaces(ctx).await?;
        tx.commit(ctx).await?;
        let mut total = self.republish_changes(ctx, sink).await?;
        for ns in namespaces {
            total += self.republish_namespace(ctx, &ns, sink).await?;
        }
        Ok(total)
    }

    /// republishChanges publishes the index document of every change (ADR 0095); it returns their number.
    async fn republish_changes(&self, ctx: &Ctx, sink: &dyn EventSink) -> Result<usize> {
        let mut tx = self.begin(ctx).await?;
        let cs = tx.changes(ctx).await?;
        let mut ot = ObsState::default();
        for c in &cs {
            ot.index(&c.id);
        }
        let events = self.change_docs(ctx, &mut tx, &ot).await?;
        tx.commit(ctx).await?;
        for p in &events {
            sink.publish(ctx, &p.subject, &p.event).await?;
        }
        Ok(events.len())
    }

    /// republishNamespace is the per-namespace core of Republish; it returns the number of node events.
    async fn republish_namespace(
        &self,
        ctx: &Ctx,
        namespace: &str,
        sink: &dyn EventSink,
    ) -> Result<usize> {
        let mut tx = self.begin(ctx).await?;
        let mut ot = ObsState::default();
        for l in tx.latest_nodes(ctx, namespace, MAIN_BRANCH).await? {
            for v in tx.versions(ctx, &l.id).await? {
                ot.nodes.push(v.r#ref());
            }
        }
        if let Ok(mut head) = branch_head(ctx, &mut tx, namespace, MAIN_BRANCH).await
            && !head.id.is_empty()
        {
            head.parent_id = Default::default();
            ot.baselines.push(head);
        }
        let events = self.events_of(ctx, &mut tx, &ot).await?;
        tx.commit(ctx).await?;
        let mut nodes = 0;
        for p in &events {
            if matches!(p.event, GraphEvent::Node(_)) {
                nodes += 1;
            }
            sink.publish(ctx, &p.subject, &p.event).await?;
        }
        Ok(nodes)
    }
}

/// The key of a node the index holds for a project or an owner (the id when it cannot be read).
async fn key_of(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    keys: &mut HashMap<NodeID, String>,
    id: &NodeID,
) -> String {
    if id.is_empty() {
        return String::new();
    }
    if let Some(k) = keys.get(id) {
        return k.clone();
    }
    let k = match tx
        .node(
            ctx,
            &NodeRef {
                id: id.clone(),
                version: 0,
            },
        )
        .await
    {
        Ok(u) => u.key,
        Err(_) => id.to_string(),
    };
    keys.insert(id.clone(), k.clone());
    k
}

/// subjectToken makes a value usable as one token of a NATS subject.
pub(crate) fn subject_token(s: &str) -> String {
    s.chars()
        .map(|r| {
            if r == '.' || r == '*' || r == '>' || r == ' ' {
                '_'
            } else {
                r
            }
        })
        .collect()
}

impl TypeIndex {
    /// searchable resolves the text and facet values of a node from the search declarations of its type (own and
    /// inherited, the subtype overriding by property name).
    pub(crate) fn searchable(&self, n: &Node) -> (BTreeMap<String, String>, Map<String, Value>) {
        let mut text = BTreeMap::new();
        let mut facets = Map::new();
        for sp in self.search_of(&n.r#type) {
            let Some(v) = n.properties.get(&sp.property) else {
                continue;
            };
            if v.is_null() {
                continue;
            }
            if sp.text {
                let s = goap_dsl::sprint(std::slice::from_ref(v));
                if !s.is_empty() {
                    text.insert(sp.property.clone(), s);
                }
            }
            if sp.facet {
                facets.insert(sp.property.clone(), v.clone());
            }
        }
        (text, facets)
    }

    /// searchOf returns the search declarations of a type along its extends chain.
    pub(crate) fn search_of(&self, typ: &str) -> Vec<SearchProperty> {
        match &self.cat {
            Some(c) => c.search(typ).to_vec(),
            None => Vec::new(),
        }
    }
}
