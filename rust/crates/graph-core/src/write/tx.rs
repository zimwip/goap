//! A write transaction of the graph: the store's transaction with the guard of the graph around it (Go's `guardTx`,
//! ADR 0054) and the collector of its events (Go's `observedTx`, ADR 0136). Every write of an operation goes through
//! it: what names a change, a unit or a project is checked before it commits, and the events of what it wrote go to the
//! outbox in the same transaction.
use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::Arc;

use chrono::{DateTime, Utc};
use graph_catalog::{Catalog, catalog::STRUCTURE_ORGANISATION, catalog::STRUCTURE_PROJECT};
use serde::Serialize;

use super::events::{self, BaselineEvent, ChangeDocEvent, ChangeEvent, ChangeJson, DocRef, EventNode, NodeEvent};
use crate::{
    GraphError, Result,
    change::Change,
    gojson,
    impact::{LOG_FACT, LOG_IMPACT, LogEntry},
    lnk::Url,
    model::{Baseline, Branch, ChangeId, Link, LinkId, Node, NodeId, NodeRef, branch_of, namespace_of},
    store::{LinkSet, OutboxMessage, WriteTx},
};

/// Who writes and when: what every operation of a request shares.
#[derive(Debug, Clone)]
pub struct Env {
    /// The caller (`by` of the log, the reviewer of a review...).
    pub by: String,
    /// The person the events are for (ADR 0114): the caller, or the one a platform service acts for.
    pub end_user: String,
    /// The stamp of the command (`X-Goap-Actor` / `X-Goap-Command`), carried by the events.
    pub actor: String,
    pub command: String,
    pub catalog: Arc<Catalog>,
    /// Whether the events go to the outbox: a bus delivers them (Go's `Graph.Observe` with a sink); none, no event.
    pub outbox: bool,
}

impl Env {
    pub fn now(&self) -> DateTime<Utc> {
        Utc::now()
    }

    /// A new id (Go's `g.newID`: a random UUID).
    pub fn new_id(&self) -> String {
        uuid::Uuid::new_v4().to_string()
    }
}

/// The most impacts the document of a change carries.
const MAX_INDEXED_IMPACTS: usize = 200;

/// A write transaction of the graph over a transaction of the store.
pub struct Tx<'s, S: WriteTx> {
    pub store: &'s S,
    pub env: &'s Env,
    locked: HashSet<String>,
    /// what the transaction wrote that names a change, a unit or a project, checked at its end
    change_refs: BTreeMap<ChangeId, String>,
    owners: BTreeMap<String, (Url, String)>,
    projects: BTreeMap<String, (Url, String)>,
    /// the headers of the changes written, checked at the end
    changes: BTreeMap<ChangeId, Change>,
    /// the headers read to stamp versions
    headers: HashMap<ChangeId, Change>,
    /// per branch, the order of the commit the versions the transaction writes or joins there commit in (ADR 0140)
    commit: BTreeMap<String, i64>,
    written: HashSet<NodeRef>,
    // what the observer collects
    touched: Vec<ChangeId>,
    written_headers: HashMap<ChangeId, Change>,
    indexed: Vec<ChangeId>,
    purged: Vec<ChangeId>,
    raised: Vec<(String, String)>,
    seq: HashMap<ChangeId, i64>,
    nodes: Vec<Node>,
    baselines: Vec<(Baseline, Option<BTreeMap<NodeId, i32>>)>,
    /// the last position the transaction left the log of each change at (the watermark, ADR 0153)
    pub positions: BTreeMap<ChangeId, i64>,
    /// the views of the changes the transaction works on, folded from their logs and kept up by its writes
    pub(super) views: HashMap<ChangeId, super::work::ChangeView>,
}

impl<'s, S: WriteTx> Tx<'s, S> {
    pub fn new(store: &'s S, env: &'s Env) -> Tx<'s, S> {
        Tx {
            store,
            env,
            locked: HashSet::new(),
            change_refs: BTreeMap::new(),
            owners: BTreeMap::new(),
            projects: BTreeMap::new(),
            changes: BTreeMap::new(),
            headers: HashMap::new(),
            commit: BTreeMap::new(),
            written: HashSet::new(),
            touched: Vec::new(),
            written_headers: HashMap::new(),
            indexed: Vec::new(),
            purged: Vec::new(),
            raised: Vec::new(),
            seq: HashMap::new(),
            nodes: Vec::new(),
            baselines: Vec::new(),
            positions: BTreeMap::new(),
            views: HashMap::new(),
        }
    }

    pub fn catalog(&self) -> &Catalog {
        &self.env.catalog
    }

    /// Takes the lock of the tree of a change once per transaction (ADR 0127).
    pub async fn lock_change(&mut self, id: &ChangeId) -> Result<()> {
        if id.is_empty() || !self.locked.insert(format!("change:{id}")) {
            return Ok(());
        }
        self.store.lock_change(id).await
    }

    /// Takes the lock of a branch once per transaction.
    pub async fn lock_branch(&mut self, namespace: &str, name: &str) -> Result<()> {
        if !self.locked.insert(format!("branch:{}/{name}", namespace_of(namespace))) {
            return Ok(());
        }
        self.store.lock_branch(namespace, name).await
    }

    fn touch(&mut self, id: &ChangeId) {
        if !id.is_empty() && !self.touched.contains(id) {
            self.touched.push(id.clone());
        }
    }

    fn index(&mut self, id: &ChangeId) {
        if !id.is_empty() && !self.indexed.contains(id) {
            self.indexed.push(id.clone());
        }
    }

    fn need_change(&mut self, id: &ChangeId, what: &str) -> Result<()> {
        if id.is_empty() {
            return Err(GraphError::invalid(format!(
                "{what} is written outside of a change (ADR 0049, 0054)"
            )));
        }
        self.change_refs.entry(id.clone()).or_insert_with(|| what.to_string());
        Ok(())
    }

    /// The header of a change, read once per transaction.
    pub async fn change_head(&mut self, id: &ChangeId) -> Result<Change> {
        if let Some(c) = self.headers.get(id) {
            return Ok(c.clone());
        }
        let c = self
            .store
            .change(id)
            .await?
            .ok_or_else(|| GraphError::not_found(format!("change {id}")))?;
        self.headers.insert(id.clone(), c.clone());
        Ok(c)
    }

    pub async fn put_change(&mut self, c: &Change) -> Result<()> {
        if c.owner_org.is_zero() || c.project_id.is_zero() {
            return Err(GraphError::invalid(format!(
                "change {} names no owner unit or no project (ADR 0054)",
                c.id
            )));
        }
        if !c.parent_id.is_empty() {
            self.lock_change(&c.parent_id).await?;
        }
        self.lock_change(&c.id).await?;
        self.store.put_change(c).await?;
        self.changes.insert(c.id.clone(), c.clone());
        self.headers.insert(c.id.clone(), c.clone());
        self.written_headers.insert(c.id.clone(), c.clone());
        self.touch(&c.id);
        self.index(&c.id);
        Ok(())
    }

    pub async fn put_branch(&mut self, b: &Branch) -> Result<()> {
        self.store.put_branch(b).await
    }

    /// Appends an entry to the log of its change; returns its position.
    pub async fn append_log(&mut self, e: &LogEntry) -> Result<i64> {
        self.lock_change(&e.change).await?;
        let seq = self.store.append_log(e).await?;
        self.touch(&e.change);
        if e.typ.starts_with(&format!("{LOG_IMPACT}.")) || e.typ.starts_with(&format!("{LOG_FACT}.")) {
            self.seq.insert(e.change.clone(), seq);
            self.positions.insert(e.change.clone(), seq);
        }
        Ok(seq)
    }

    pub async fn put_change_impact(&mut self, change: &ChangeId, cn: &crate::impact::ChangeImpact) -> Result<()> {
        self.lock_change(change).await?;
        self.store.put_change_impact(change, cn).await?;
        self.touch(change);
        self.index(change);
        Ok(())
    }

    pub async fn delete_change_impact(&mut self, change: &ChangeId, id: &str) -> Result<()> {
        self.lock_change(change).await?;
        self.store.delete_change_impact(change, id).await
    }

    /// The order of the commit the versions written or joined on a branch take, allocated by the first of them.
    pub async fn commit_on(&mut self, branch: &str) -> Result<i64> {
        let branch = branch_of(branch).to_string();
        if let Some(s) = self.commit.get(&branch) {
            return Ok(*s);
        }
        let s = self.store.next_commit().await?;
        self.commit.insert(branch, s);
        Ok(s)
    }

    /// Closes the commit of a branch that keeps no baseline (the branch of a change, ADR 0140).
    pub fn end_commit(&mut self, branch: &str) {
        self.commit.remove(branch_of(branch));
    }

    /// Writes a version, stamped with its owner and its project (from the version it follows, else from its change)
    /// and with the order of its commit.
    pub async fn put_node(&mut self, n: &Node) -> Result<()> {
        let mut n = n.clone();
        let what = format!("node {} v{}", n.key, n.version);
        self.need_change(&n.change_id, &what)?;
        let mut prev: Option<Node> = None;
        if n.version > 1 {
            let p = match n.parents.first() {
                Some(&p) if p >= 1 && p < n.version => self.store.node(&NodeRef::new(n.id.clone(), p)).await?,
                _ => self.store.version_range(&n.id, 0, 1).await?.into_iter().next(),
            };
            if let Some(p) = p {
                if !n.project.is_zero() && n.project != p.project {
                    return Err(GraphError::invalid(format!(
                        "{what}: the project of a node never changes ({}, not {})",
                        p.project, n.project
                    )));
                }
                n.project = p.project.clone();
                prev = Some(p);
            }
        }
        if n.owner.is_zero()
            && let Some(p) = &prev
        {
            n.owner = p.owner.clone();
        }
        if n.owner.is_zero() || n.project.is_zero() {
            let c = match self.change_head(&n.change_id).await {
                Err(e) if e.is_not_found() => {
                    return Err(GraphError::invalid(format!(
                        "{what}: change {} does not exist",
                        n.change_id
                    )));
                }
                r => r?,
            };
            if n.owner.is_zero() {
                n.owner = c.owner_org.clone();
            }
            if n.project.is_zero() {
                n.project = c.project_id.clone();
            }
        }
        self.owners.insert(n.owner.to_string(), (n.owner.clone(), what.clone()));
        self.projects
            .insert(n.project.to_string(), (n.project.clone(), what.clone()));
        let committed = self.commit_on(&n.branch).await?;
        self.store.put_node(&n, committed).await?;
        self.written.insert(n.node_ref());
        self.nodes.push(n);
        Ok(())
    }

    pub async fn put_links(&mut self, ls: &[Link]) -> Result<()> {
        for l in ls {
            self.need_change(&l.change_id, &format!("link {}", l.typ))?;
            if !self.written.contains(&l.from) {
                return Err(GraphError::conflict(format!(
                    "link {}: version {} is not written by this transaction, a version never changes (ADR 0079)",
                    l.typ, l.from
                )));
            }
        }
        self.store.put_links(ls).await
    }

    pub async fn drop_link(&mut self, from: &NodeRef, row: &LinkId, l: &Link) -> Result<()> {
        self.need_change(&l.change_id, &format!("link {}", l.typ))?;
        if !self.written.contains(from) {
            return Err(GraphError::conflict(format!(
                "link {}: version {from} is not written by this transaction, a version never changes (ADR 0079)",
                l.typ
            )));
        }
        self.store.drop_link(from, row, l).await
    }

    pub async fn put_link_set(&mut self, from: &NodeRef, set: LinkSet) -> Result<()> {
        if !self.written.contains(from) {
            return Err(GraphError::conflict(format!(
                "the links of version {from}: it is not written by this transaction, a version never changes (ADR 0079)"
            )));
        }
        for v in [set.of, set.base] {
            if v >= from.version {
                return Err(GraphError::invalid(format!(
                    "the links of version {from}: v{v} is not an earlier version of the node"
                )));
            }
        }
        if set.of != 0 && set.base != 0 {
            return Err(GraphError::invalid(format!(
                "the links of version {from}: a version shares a set or writes a delta, not both"
            )));
        }
        self.store.put_link_set(from, set).await
    }

    pub async fn join_branch(&mut self, r: &NodeRef, branch: &str, change: &ChangeId) -> Result<()> {
        self.need_change(change, &format!("node {r} joining {branch}"))?;
        let committed = self.commit_on(branch).await?;
        self.store.join_branch(r, branch, change, committed).await
    }

    /// Writes the header of a baseline, giving it the order of the commit of its branch, which ends. `delta` is what it
    /// changed of its parent's state (its event carries it), none when its event names it only.
    pub async fn put_baseline(&mut self, b: &Baseline, delta: Option<BTreeMap<NodeId, i32>>) -> Result<Baseline> {
        self.need_change(&b.change_id, &format!("baseline {}", b.name))?;
        let mut b = b.clone();
        b.seq = self.commit_on(&b.branch).await?;
        self.commit.remove(branch_of(&b.branch));
        self.store.put_baseline(&b).await?;
        self.baselines.push((b.clone(), delta));
        Ok(b)
    }

    pub async fn delete_change(&mut self, id: &ChangeId, namespace: &str, branch: &str) -> Result<()> {
        self.lock_change(id).await?;
        self.store.delete_change(id, namespace, branch).await?;
        self.changes.remove(id);
        self.headers.remove(id);
        self.indexed.retain(|x| x != id);
        if !self.purged.contains(id) {
            self.purged.push(id.clone());
        }
        Ok(())
    }

    /// Emits an event of a change from the transaction that writes its fact (Go's `Graph.raise`).
    pub fn raise(&mut self, typ: &str, c: &Change, baseline: Option<&Baseline>, branch: &str, nodes: &[EventNode]) {
        let ev = ChangeEvent {
            typ,
            change: ChangeJson::of(c, 0),
            baseline: baseline.map(events::BaselineJson::of),
            end_user: &self.env.end_user,
            branch,
            nodes,
        };
        self.raised
            .push((events::change_subject(&c.id, typ), gojson::to_string(&ev)));
    }

    /// Ends the transaction: checks what it wrote names what exists (Go's `guardTx.verify`), then writes the events
    /// of its writes in the outbox (Go's `observedRepo.InTx`). The caller commits the store's transaction after.
    pub async fn finish(mut self) -> Result<BTreeMap<ChangeId, i64>> {
        self.verify().await?;
        if !self.env.outbox {
            return Ok(self.positions);
        }
        let msgs = self.events().await?;
        if !msgs.is_empty() {
            self.store.append_outbox(&msgs).await?;
        }
        Ok(self.positions)
    }

    async fn verify(&mut self) -> Result<()> {
        if let Some(branch) = self.commit.keys().next() {
            return Err(GraphError::invalid(format!(
                "versions committed on branch {branch} in no baseline: a version is written when its change commits (ADR 0079, 0140)"
            )));
        }
        let refs: Vec<(ChangeId, String)> = self.change_refs.iter().map(|(k, v)| (k.clone(), v.clone())).collect();
        for (id, what) in refs {
            if self.changes.contains_key(&id) {
                continue;
            }
            match self.change_head(&id).await {
                Err(e) if e.is_not_found() => {
                    return Err(GraphError::invalid(format!(
                        "{what} names change {id}, which does not exist"
                    )));
                }
                r => {
                    r?;
                }
            }
        }
        let owners: Vec<(Url, String)> = self.owners.values().cloned().collect();
        for (u, what) in owners {
            self.in_structure(STRUCTURE_ORGANISATION, &u, &format!("{what}: owner"))
                .await?;
        }
        let projects: Vec<(Url, String)> = self.projects.values().cloned().collect();
        for (u, what) in projects {
            self.in_structure(STRUCTURE_PROJECT, &u, &format!("{what}: project"))
                .await?;
        }
        let changes: Vec<Change> = self.changes.values().cloned().collect();
        for c in changes {
            self.in_structure(STRUCTURE_ORGANISATION, &c.owner_org, &format!("change {}: owner", c.id))
                .await?;
            self.in_structure(STRUCTURE_PROJECT, &c.project_id, &format!("change {}: project", c.id))
                .await?;
        }
        Ok(())
    }

    /// Checks that a URL names a node of the structure of a kind (Go's `guardTx.inStructure`, read in the transaction).
    async fn in_structure(&self, kind: &str, u: &Url, what: &str) -> Result<()> {
        let st = self
            .catalog()
            .structure(kind)
            .ok_or_else(|| GraphError::internal(format!("no {kind} structure in the catalogue")))?;
        if u.source != st.namespace {
            return Err(GraphError::invalid(format!(
                "{what} {u}: a {kind} is read in the source {}",
                st.namespace
            )));
        }
        if !self.catalog().is_a(&format!("{}@{}", st.namespace, u.typ), &st.typ) {
            return Err(GraphError::invalid(format!(
                "{what} {u}: {} is not a type of the {kind}",
                u.typ
            )));
        }
        let Some(id) = self.store.node_id_by_key(&st.namespace, &u.key).await? else {
            return Err(GraphError::invalid(format!(
                "{what} {:?} is not a node of namespace {}",
                u.key, st.namespace
            )));
        };
        let Some(n) = self.store.version_range(&id, 0, 1).await?.into_iter().next() else {
            return Err(GraphError::invalid(format!("{what} {} does not exist", u.key)));
        };
        let in_st = namespace_of(&n.namespace) == st.namespace && self.catalog().is_a(&n.typ, &st.typ);
        if !in_st || !self.catalog().is_a(&n.typ, &format!("{}@{}", st.namespace, u.typ)) {
            return Err(GraphError::invalid(format!(
                "{what} {} is a {} of namespace {}, not a {}",
                n.key,
                n.typ,
                namespace_of(&n.namespace),
                st.typ
            )));
        }
        Ok(())
    }

    fn message(&self, subject: String, data: String, at: DateTime<Utc>) -> OutboxMessage {
        let mut headers = BTreeMap::new();
        if !self.env.command.is_empty() {
            headers.insert("X-Goap-Actor".to_string(), self.env.actor.clone());
            headers.insert("X-Goap-Command".to_string(), self.env.command.clone());
        }
        OutboxMessage {
            subject,
            data,
            headers,
            created_at: at,
        }
    }

    /// The events of what the transaction wrote, in Go's order (`Graph.eventsOf`).
    async fn events(&mut self) -> Result<Vec<OutboxMessage>> {
        let now = self.env.now();
        let mut out = Vec::new();
        for id in self.touched.clone() {
            let seq = self.seq.get(&id).copied().unwrap_or(0);
            let c = self.written_headers.get(&id).cloned().unwrap_or_else(|| Change {
                id: id.clone(),
                ..Change::default()
            });
            out.push(self.message(events::subject_change_touched(&id), touched_json(&c, seq), now));
        }
        for id in self.indexed.clone() {
            let c = match self.written_headers.get(&id) {
                Some(c) => c.clone(),
                None => self.change_head(&id).await?,
            };
            let keys = self.store.change_impact_keys(&id, MAX_INDEXED_IMPACTS).await?;
            let doc = ChangeDocEvent {
                id: &c.id,
                deleted: false,
                title: &c.title,
                intent: &c.intent,
                goal: &c.goal,
                methodology: &c.methodology,
                namespace: &c.namespace,
                status: &c.status,
                state: &c.state,
                project_id: &c.project_id.key,
                owner_org: &c.owner_org.key,
                parent_id: &c.parent_id,
                branch: &c.branch,
                impacts: keys.into_iter().map(|(key, typ)| DocRef { key, typ }).collect(),
                created_at: c.created_at.unwrap_or_else(gojson::zero_time),
            };
            out.push(self.message(events::subject_change_indexed(&id), gojson::to_string(&doc), now));
        }
        for id in self.purged.clone() {
            let doc = ChangeDocEvent {
                id: &id,
                deleted: true,
                title: "",
                intent: "",
                goal: "",
                methodology: "",
                namespace: "",
                status: "",
                state: "",
                project_id: "",
                owner_org: "",
                parent_id: &ChangeId::default(),
                branch: "",
                impacts: Vec::new(),
                created_at: gojson::zero_time(),
            };
            out.push(self.message(events::subject_change_indexed(&id), gojson::to_string(&doc), now));
        }
        for (subject, data) in std::mem::take(&mut self.raised) {
            out.push(self.message(subject, data, now));
        }
        for n in std::mem::take(&mut self.nodes) {
            let (text, facets) = searchable(self.catalog(), &n);
            let ns = namespace_of(&n.namespace).to_string();
            let ev = NodeEvent {
                id: &n.id,
                version: n.version,
                branch: branch_of(&n.branch),
                namespace: &ns,
                key: &n.key,
                typ: &n.typ,
                state: &n.state,
                deleted: n.deleted,
                project: &n.project.key,
                owner: &n.owner.key,
                change_id: &n.change_id,
                text,
                facets,
                time: n.created_at.unwrap_or_else(gojson::zero_time),
            };
            out.push(self.message(
                events::subject_node_written(&ns, &n.typ, &n.id),
                gojson::to_string(&ev),
                now,
            ));
        }
        for (b, delta) in std::mem::take(&mut self.baselines) {
            let mut ev = BaselineEvent {
                id: &b.id,
                namespace: namespace_of(&b.namespace),
                branch: events::event_branch(&b),
                parent: &b.parent_id,
                set: BTreeMap::new(),
                removed: Vec::new(),
                time: b.created_at.unwrap_or_else(gojson::zero_time),
            };
            if let Some(delta) = &delta {
                for (id, v) in delta {
                    if *v == 0 {
                        ev.removed.push(id.clone());
                    } else {
                        ev.set.insert(id.clone(), *v);
                    }
                }
            }
            out.push(self.message(
                events::subject_baseline_advanced(ev.branch),
                gojson::to_string(&ev),
                now,
            ));
        }
        Ok(out)
    }
}

/// The `change.updated` event of a change the transaction touched: its header when it wrote it, else its id.
fn touched_json(c: &Change, seq: i64) -> String {
    #[derive(Serialize)]
    struct Touched<'a> {
        #[serde(rename = "type")]
        typ: &'a str,
        change: ChangeJson<'a>,
    }
    gojson::to_string(&Touched {
        typ: "change.updated",
        change: ChangeJson::of(c, seq),
    })
}

/// The text and facet values of a node, from the search declarations of its type (Go's `typeIndex.searchable`).
fn searchable(cat: &Catalog, n: &Node) -> (BTreeMap<String, String>, serde_json::Map<String, serde_json::Value>) {
    let mut text = BTreeMap::new();
    let mut facets = serde_json::Map::new();
    let Some(props) = &n.props else {
        return (text, facets);
    };
    for sp in cat.search(&n.typ) {
        let Some(v) = props.get(&sp.property).filter(|v| !v.is_null()) else {
            continue;
        };
        if sp.text {
            let s = go_sprint(v);
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

/// `fmt.Sprint` of a decoded JSON value.
pub fn go_sprint(v: &serde_json::Value) -> String {
    use serde_json::Value;
    match v {
        Value::String(s) => s.clone(),
        Value::Bool(b) => b.to_string(),
        Value::Number(n) => match n.as_f64() {
            // %v of a float64: the shortest form, an exponent from 1e21 and below 1e-4
            Some(f) => go_float_v(f),
            None => n.to_string(),
        },
        Value::Null => "<nil>".into(),
        Value::Array(a) => format!("[{}]", a.iter().map(go_sprint).collect::<Vec<_>>().join(" ")),
        Value::Object(m) => format!(
            "map[{}]",
            m.iter()
                .map(|(k, v)| format!("{k}:{}", go_sprint(v)))
                .collect::<Vec<_>>()
                .join(" ")
        ),
    }
}

/// `%v` of a float64 (`strconv.FormatFloat(f, 'g', -1, 64)` with Go's exponent threshold of 21 digits).
fn go_float_v(f: f64) -> String {
    let a = f.abs();
    if a != 0.0 && !(1e-4..1e21).contains(&a) {
        let s = format!("{f:e}");
        let (m, e) = s.split_once('e').unwrap_or((&s, "0"));
        let (sign, d) = match e.strip_prefix('-') {
            Some(d) => ('-', d),
            None => ('+', e),
        };
        let d = if d.len() < 2 { format!("0{d}") } else { d.to_string() };
        return format!("{m}e{sign}{d}");
    }
    format!("{f}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn prints_values_as_go() {
        use serde_json::json;
        assert_eq!(go_sprint(&json!(1.0)), "1");
        assert_eq!(go_sprint(&json!(0.00001)), "1e-05");
        assert_eq!(go_sprint(&json!(1e21)), "1e+21");
        assert_eq!(go_sprint(&json!("x")), "x");
        assert_eq!(go_sprint(&json!(true)), "true");
    }
}
