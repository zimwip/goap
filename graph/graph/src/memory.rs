//! Port of `memory.go`: an in-memory [`Repo`]. Transactions are serialized (one at a time holds the state) and rolled
//! back by restoring a snapshot.

use std::any::Any;
use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, Branch, CHANGE_ABANDONED, CHANGE_APPLIED, Change, ChangeID, ChangeImpact,
    ChangeImpactID, Link, LinkID, LogEntry, LogFilter, MAIN_BRANCH, Node, NodeID, NodeRef, Tag,
    TagFilter, TagID, Version, branch_of, namespace_of,
};
use tokio::sync::{Mutex, OwnedMutexGuard};

use crate::changelog::{facts_filter, items_of};
use crate::repo::{Repo, Tx};
use crate::{Error, ErrorKind, Result};

/// Memory is an in-memory Repo. Transactions are serialized and rolled back by restoring a snapshot.
#[derive(Default)]
pub struct Memory {
    st: Arc<Mutex<MemState>>,
}

#[derive(Clone, Default)]
struct MemState {
    /// index = version-1
    versions: HashMap<NodeID, Vec<Node>>,
    keys: HashMap<String, NodeID>,
    links: Vec<Link>,
    baselines: HashMap<BaselineID, Baseline>,
    changes: BTreeMap<ChangeID, Change>,
    branches: HashMap<String, Branch>,
    /// joins are the branches a version is part of besides the one it was written on (ADR 0032), with the change
    /// whose merge made it join
    joins: HashMap<JoinKey, ChangeID>,
    nodes: HashMap<ChangeID, Vec<ChangeImpact>>,
    /// log holds the entries of every change's log, in their order (ADR 0030)
    log: Vec<LogEntry>,
    /// tags name the state a change leaves (ADR 0056), in creation order
    tags: Vec<Tag>,
}

#[derive(Clone, PartialEq, Eq, Hash)]
struct JoinKey {
    r#ref: NodeRef,
    branch: String,
}

impl Memory {
    /// NewMemory returns an empty in-memory repository.
    pub fn new() -> Memory {
        Memory::default()
    }
}

#[async_trait]
impl Repo for Memory {
    async fn begin(&self, _ctx: &Ctx) -> Result<Box<dyn Tx>> {
        let st = self.st.clone().lock_owned().await;
        let snapshot = Some(st.clone());
        Ok(Box::new(MemTx { st, snapshot }))
    }

    fn as_any(&self) -> &dyn Any {
        self
    }
}

struct MemTx {
    st: OwnedMutexGuard<MemState>,
    /// the state before the transaction, put back unless it commits
    snapshot: Option<MemState>,
}

impl Drop for MemTx {
    fn drop(&mut self) {
        if let Some(s) = self.snapshot.take() {
            *self.st = s;
        }
    }
}

/// nsKey scopes a node key to its namespace.
fn ns_key(namespace: &str, key: &str) -> String {
    format!("{}\x00{key}", namespace_of(namespace))
}

/// branchKey scopes a branch name to its namespace.
fn branch_key(namespace: &str, name: &str) -> String {
    format!("{}\x00{}", namespace_of(namespace), branch_of(name))
}

fn not_found(what: impl std::fmt::Display) -> Error {
    Error::wrap(ErrorKind::NotFound, what)
}

impl MemTx {
    /// on reports whether a version is part of a branch: written there, or joined.
    fn on(&self, n: &Node, branch: &str) -> bool {
        let branch = branch_of(branch);
        branch_of(&n.branch) == branch
            || self
                .st
                .joins
                .get(&JoinKey {
                    r#ref: n.r#ref(),
                    branch: branch.to_string(),
                })
                .is_some_and(|c| !c.is_empty())
    }

    fn latest(&self, id: &NodeID, branch: &str) -> Result<Node> {
        if let Some(vs) = self.st.versions.get(id) {
            for v in vs.iter().rev() {
                if self.on(v, branch) {
                    return Ok(v.clone());
                }
            }
        }
        Err(not_found(format!(
            "node {id} has no version on {}",
            branch_of(branch)
        )))
    }

    fn get(&self, r: &NodeRef) -> Result<Node> {
        let vs = match self.st.versions.get(&r.id) {
            Some(vs) if !vs.is_empty() => vs,
            _ => return Err(not_found(format!("node {}", r.id))),
        };
        if r.version == 0 {
            return self.latest(&r.id, MAIN_BRANCH);
        }
        if r.version < 1 || r.version as usize > vs.len() {
            return Err(not_found(format!("node {r}")));
        }
        Ok(vs[(r.version - 1) as usize].clone())
    }

    fn change_of(&self, id: &ChangeID) -> Result<Change> {
        let Some(c) = self.st.changes.get(id) else {
            return Err(not_found(format!("change {id}")));
        };
        let mut c = c.clone();
        let facts = self.log_of(&facts_filter(id));
        c.items = items_of(&facts)?;
        c.nodes = self.st.nodes.get(id).cloned().unwrap_or_default();
        Ok(c)
    }

    fn log_of(&self, f: &LogFilter) -> Vec<LogEntry> {
        let mut out = Vec::new();
        for e in &self.st.log {
            if f.matches(e) {
                out.push(e.clone());
                if f.limit > 0 && out.len() as i64 == f.limit {
                    break;
                }
            }
        }
        out
    }

    fn created_at(&self, id: &ChangeID) -> Option<chrono::DateTime<chrono::Utc>> {
        self.st.changes.get(id).map(|c| c.created_at)
    }
}

#[async_trait]
impl Tx for MemTx {
    async fn node(&mut self, _ctx: &Ctx, r: &NodeRef) -> Result<Node> {
        self.get(r)
    }

    async fn latest_on(&mut self, _ctx: &Ctx, id: &NodeID, branch: &str) -> Result<Node> {
        self.latest(id, branch)
    }

    async fn versions(&mut self, _ctx: &Ctx, id: &NodeID) -> Result<Vec<Node>> {
        let Some(vs) = self.st.versions.get(id) else {
            return Err(not_found(format!("node {id}")));
        };
        let mut vs = vs.clone();
        for k in self.st.joins.keys() {
            if &k.r#ref.id == id && k.r#ref.version >= 1 && (k.r#ref.version as usize) <= vs.len() {
                vs[(k.r#ref.version - 1) as usize]
                    .joined
                    .push(k.branch.clone());
            }
        }
        for v in &mut vs {
            v.joined.sort();
        }
        Ok(vs)
    }

    async fn derived_nodes(&mut self, _ctx: &Ctx, r: &NodeRef) -> Result<Vec<Node>> {
        let mut out = Vec::new();
        for vs in self.st.versions.values() {
            for v in vs {
                if v.origins
                    .iter()
                    .any(|o| o.id == r.id && (r.version == 0 || o.version == r.version))
                {
                    out.push(v.clone());
                }
            }
        }
        out.sort_by(|a, b| a.key.cmp(&b.key).then(a.version.cmp(&b.version)));
        Ok(out)
    }

    async fn branch(&mut self, _ctx: &Ctx, namespace: &str, name: &str) -> Result<Branch> {
        self.st
            .branches
            .get(&branch_key(namespace, name))
            .cloned()
            .ok_or_else(|| not_found(format!("branch {}/{name}", namespace_of(namespace))))
    }

    async fn branches(&mut self, _ctx: &Ctx, namespace: &str) -> Result<Vec<Branch>> {
        let namespace = namespace_of(namespace);
        let mut out: Vec<Branch> = self
            .st
            .branches
            .values()
            .filter(|b| b.namespace == namespace)
            .cloned()
            .collect();
        out.sort_by(|a, b| a.created_at.cmp(&b.created_at).then(a.name.cmp(&b.name)));
        Ok(out)
    }

    async fn put_branch(&mut self, _ctx: &Ctx, mut b: Branch) -> Result<()> {
        b.namespace = namespace_of(&b.namespace).to_string();
        self.st
            .branches
            .insert(branch_key(&b.namespace, &b.name), b);
        Ok(())
    }

    async fn node_by_key(&mut self, _ctx: &Ctx, namespace: &str, key: &str) -> Result<Node> {
        let Some(id) = self.st.keys.get(&ns_key(namespace, key)).cloned() else {
            return Err(not_found(format!("node key {key:?}")));
        };
        self.get(&NodeRef { id, version: 0 })
    }

    async fn node_id_by_key(&mut self, _ctx: &Ctx, namespace: &str, key: &str) -> Result<NodeID> {
        self.st
            .keys
            .get(&ns_key(namespace, key))
            .cloned()
            .ok_or_else(|| not_found(format!("node key {key:?}")))
    }

    async fn nodes_in(
        &mut self,
        ctx: &Ctx,
        baseline: &BaselineID,
        node_type: &str,
    ) -> Result<Vec<Node>> {
        let b = self.baseline(ctx, baseline).await?;
        let mut out = Vec::new();
        for (id, v) in &b.nodes {
            let n = self.get(&NodeRef {
                id: id.clone(),
                version: *v,
            })?;
            if node_type.is_empty() || n.r#type == node_type {
                out.push(n);
            }
        }
        out.sort_by(|a, b| a.key.cmp(&b.key));
        Ok(out)
    }

    async fn latest_nodes(
        &mut self,
        _ctx: &Ctx,
        namespace: &str,
        branch: &str,
    ) -> Result<Vec<Node>> {
        let namespace = namespace_of(namespace);
        let mut out = Vec::new();
        for id in self.st.versions.keys() {
            let Ok(n) = self.latest(id, branch) else {
                continue;
            };
            if n.namespace == namespace {
                out.push(n);
            }
        }
        out.sort_by(|a, b| a.key.cmp(&b.key));
        Ok(out)
    }

    async fn namespaces(&mut self, _ctx: &Ctx) -> Result<Vec<String>> {
        let mut out: Vec<String> = self
            .st
            .versions
            .values()
            .filter_map(|vs| vs.first().map(|v| v.namespace.clone()))
            .collect();
        out.sort();
        out.dedup();
        Ok(out)
    }

    async fn out_links(&mut self, _ctx: &Ctx, r: &NodeRef) -> Result<Vec<Link>> {
        Ok(self
            .st
            .links
            .iter()
            .filter(|l| &l.from == r)
            .cloned()
            .collect())
    }

    async fn in_links(&mut self, _ctx: &Ctx, r: &NodeRef) -> Result<Vec<Link>> {
        Ok(self
            .st
            .links
            .iter()
            .filter(|l| &l.to == r)
            .cloned()
            .collect())
    }

    async fn baseline(&mut self, _ctx: &Ctx, id: &BaselineID) -> Result<Baseline> {
        self.st
            .baselines
            .get(id)
            .cloned()
            .ok_or_else(|| not_found(format!("baseline {id}")))
    }

    async fn baselines(&mut self, _ctx: &Ctx, namespace: &str) -> Result<Vec<Baseline>> {
        let namespace = namespace_of(namespace);
        let mut out: Vec<Baseline> = self
            .st
            .baselines
            .values()
            .filter(|b| b.namespace == namespace)
            .cloned()
            .collect();
        out.sort_by(|a, b| a.created_at.cmp(&b.created_at).then(a.id.cmp(&b.id)));
        Ok(out)
    }

    async fn change(&mut self, _ctx: &Ctx, id: &ChangeID) -> Result<Change> {
        self.change_of(id)
    }

    async fn changes(&mut self, _ctx: &Ctx) -> Result<Vec<Change>> {
        let mut out = Vec::with_capacity(self.st.changes.len());
        for id in self.st.changes.keys() {
            if let Ok(c) = self.change_of(id) {
                out.push(c);
            }
        }
        out.sort_by_key(|c| c.created_at);
        Ok(out)
    }

    async fn put_node(&mut self, _ctx: &Ctx, mut n: Node) -> Result<()> {
        let len = self.st.versions.get(&n.id).map_or(0, Vec::len);
        if n.version as usize != len + 1 {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("node {}: expected version {}", n.r#ref(), len + 1),
            ));
        }
        if n.version == 1 && !n.key.is_empty() {
            let k = ns_key(&n.namespace, &n.key);
            if self.st.keys.contains_key(&k) {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("node key {:?} already used", n.key),
                ));
            }
            self.st.keys.insert(k, n.id.clone());
        }
        if n.version > 1 {
            // the project is the node's, set by its first version
            n.project = self.st.versions[&n.id][0].project.clone();
        }
        self.st.versions.entry(n.id.clone()).or_default().push(n);
        Ok(())
    }

    async fn put_link(&mut self, _ctx: &Ctx, l: Link) -> Result<()> {
        self.st.links.push(l);
        Ok(())
    }

    async fn link(&mut self, _ctx: &Ctx, id: &LinkID) -> Result<Link> {
        self.st
            .links
            .iter()
            .find(|l| &l.id == id)
            .cloned()
            .ok_or_else(|| not_found(format!("link {id}")))
    }

    async fn put_baseline(&mut self, _ctx: &Ctx, mut b: Baseline) -> Result<()> {
        if self.st.baselines.contains_key(&b.id) {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("baseline {}", b.id),
            ));
        }
        b.namespace = namespace_of(&b.namespace).to_string();
        for id in b.nodes.keys() {
            let Some(first) = self.st.versions.get(id).and_then(|vs| vs.first()) else {
                return Err(not_found(format!("baseline {}: node {id}", b.id)));
            };
            if first.namespace != b.namespace {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!(
                        "baseline {} is of namespace {}, node {id} is of namespace {}",
                        b.id, b.namespace, first.namespace
                    ),
                ));
            }
        }
        if b.gap > 0 {
            b.nodes = BTreeMap::new(); // only the header is kept: the graph computes the state
        }
        self.st.baselines.insert(b.id.clone(), b);
        Ok(())
    }

    async fn branch_joins(
        &mut self,
        _ctx: &Ctx,
        namespace: &str,
        branch: &str,
        change: &ChangeID,
    ) -> Result<Vec<NodeRef>> {
        let (namespace, branch) = (namespace_of(namespace), branch_of(branch));
        let mut out = Vec::new();
        for (k, c) in &self.st.joins {
            if c != change || k.branch != branch {
                continue;
            }
            if let Some(first) = self.st.versions.get(&k.r#ref.id).and_then(|vs| vs.first())
                && namespace_of(&first.namespace) == namespace
            {
                out.push(k.r#ref.clone());
            }
        }
        Ok(out)
    }

    async fn materialize_baseline(
        &mut self,
        _ctx: &Ctx,
        id: &BaselineID,
        nodes: &BTreeMap<NodeID, Version>,
    ) -> Result<()> {
        let Some(b) = self.st.baselines.get_mut(id) else {
            return Err(not_found(format!("baseline {id}")));
        };
        b.nodes = nodes.clone();
        b.gap = 0;
        Ok(())
    }

    async fn put_tag(&mut self, _ctx: &Ctx, t: Tag) -> Result<()> {
        if !self.st.changes.contains_key(&t.change_id) {
            return Err(not_found(format!(
                "tag {:?}: change {}",
                t.name, t.change_id
            )));
        }
        if !t.baseline_id.is_empty() && !self.st.baselines.contains_key(&t.baseline_id) {
            return Err(not_found(format!(
                "tag {:?}: baseline {}",
                t.name, t.baseline_id
            )));
        }
        if let Some(old) = self.st.tags.iter_mut().find(|o| o.id == t.id) {
            *old = t;
            return Ok(());
        }
        self.st.tags.push(t);
        Ok(())
    }

    async fn delete_tag(&mut self, _ctx: &Ctx, id: &TagID) -> Result<()> {
        let Some(i) = self.st.tags.iter().position(|t| &t.id == id) else {
            return Err(not_found(format!("tag {id}")));
        };
        self.st.tags.remove(i);
        Ok(())
    }

    async fn tags(&mut self, _ctx: &Ctx, f: &TagFilter) -> Result<Vec<Tag>> {
        Ok(self
            .st
            .tags
            .iter()
            .filter(|t| {
                (f.namespace.is_empty() || t.namespace == namespace_of(&f.namespace))
                    && (f.name.is_empty() || t.name == f.name)
                    && (f.change.is_empty() || t.change_id == f.change)
            })
            .cloned()
            .collect())
    }

    async fn put_change(&mut self, _ctx: &Ctx, mut c: Change) -> Result<()> {
        // the facts are in the log
        c.items = Vec::new();
        c.nodes = Vec::new();
        self.st.changes.insert(c.id.clone(), c);
        Ok(())
    }

    async fn append_log(&mut self, _ctx: &Ctx, mut e: LogEntry) -> Result<LogEntry> {
        if !self.st.changes.contains_key(&e.change) {
            // a reference to an unknown change, as the SQL foreign key
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("change {}", e.change),
            ));
        }
        if self.st.log.iter().any(|x| x.id == e.id) {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("log entry {}", e.id),
            ));
        }
        e.seq = self.st.log.len() as i64 + 1;
        self.st.log.push(e.clone());
        Ok(e)
    }

    async fn log(&mut self, _ctx: &Ctx, f: &LogFilter) -> Result<Vec<LogEntry>> {
        Ok(self.log_of(f))
    }

    async fn log_counts(&mut self, _ctx: &Ctx, f: &LogFilter) -> Result<BTreeMap<String, i64>> {
        let mut f = f.clone();
        f.after_seq = 0;
        let mut out = BTreeMap::new();
        for e in &self.st.log {
            if f.matches(e) {
                *out.entry(e.r#type.clone()).or_insert(0) += 1;
            }
        }
        Ok(out)
    }

    async fn open_change_ids(&mut self, _ctx: &Ctx) -> Result<Vec<ChangeID>> {
        let mut out: Vec<ChangeID> = self
            .st
            .changes
            .iter()
            .filter(|(_, c)| c.status != CHANGE_APPLIED && c.status != CHANGE_ABANDONED)
            .map(|(id, _)| id.clone())
            .collect();
        out.sort_by_key(|id| self.created_at(id));
        Ok(out)
    }

    async fn put_change_impact(
        &mut self,
        _ctx: &Ctx,
        change: &ChangeID,
        cn: ChangeImpact,
    ) -> Result<()> {
        if !self.st.changes.contains_key(change) {
            return Err(not_found(format!("change {change}")));
        }
        let list = self.st.nodes.entry(change.clone()).or_default();
        for x in list.iter_mut() {
            if x.id == cn.id {
                *x = cn;
                return Ok(());
            }
            if let (Some(p), Some(xp)) = (&cn.pre, &x.pre)
                && xp.id == p.id
                && x.flow == cn.flow
                && !x.superseded
                && !cn.superseded
            {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("change impact {}: node already in the change", cn.key),
                ));
            }
        }
        list.push(cn);
        Ok(())
    }

    async fn delete_change_impact(
        &mut self,
        _ctx: &Ctx,
        change: &ChangeID,
        id: &ChangeImpactID,
    ) -> Result<()> {
        let list = self.st.nodes.entry(change.clone()).or_default();
        let Some(i) = list.iter().position(|cn| &cn.id == id) else {
            return Err(not_found(format!("change impact {id}")));
        };
        list.remove(i);
        Ok(())
    }

    async fn change_impacts(&mut self, _ctx: &Ctx, change: &ChangeID) -> Result<Vec<ChangeImpact>> {
        Ok(self.st.nodes.get(change).cloned().unwrap_or_default())
    }

    async fn node_change_impacts(&mut self, _ctx: &Ctx, node: &NodeID) -> Result<Vec<ChangeID>> {
        let mut out: Vec<ChangeID> = self
            .st
            .nodes
            .iter()
            .filter(|(_, list)| {
                list.iter().any(|cn| {
                    cn.pre.as_ref().is_some_and(|p| &p.id == node)
                        || cn.post.as_ref().is_some_and(|p| &p.id == node)
                })
            })
            .map(|(id, _)| id.clone())
            .collect();
        out.sort_by_key(|id| self.created_at(id));
        Ok(out)
    }

    async fn set_node_origin(
        &mut self,
        _ctx: &Ctx,
        r: &NodeRef,
        change: &ChangeID,
        cn: &ChangeImpactID,
        comment: &str,
    ) -> Result<()> {
        let Some(vs) = self.st.versions.get_mut(&r.id) else {
            return Err(not_found(format!("node {r}")));
        };
        if r.version < 1 || r.version as usize > vs.len() {
            return Err(not_found(format!("node {r}")));
        }
        let v = &mut vs[(r.version - 1) as usize];
        v.change_id = change.clone();
        v.change_impact = cn.clone();
        v.comment = comment.to_string();
        Ok(())
    }

    async fn join_branch(
        &mut self,
        _ctx: &Ctx,
        r: &NodeRef,
        branch: &str,
        change: &ChangeID,
    ) -> Result<()> {
        let Some(vs) = self.st.versions.get(&r.id) else {
            return Err(not_found(format!("node {r}")));
        };
        if r.version < 1 || r.version as usize > vs.len() {
            return Err(not_found(format!("node {r}")));
        }
        let branch = branch_of(branch);
        if branch_of(&vs[(r.version - 1) as usize].branch) != branch {
            self.st
                .joins
                .entry(JoinKey {
                    r#ref: r.clone(),
                    branch: branch.to_string(),
                })
                .or_insert_with(|| change.clone());
        }
        Ok(())
    }

    async fn delete_change(
        &mut self,
        _ctx: &Ctx,
        id: &ChangeID,
        namespace: &str,
        branch: &str,
    ) -> Result<()> {
        let used = || {
            Error::wrap(
                ErrorKind::Conflict,
                format!("change {id}: what it wrote is used by the graph"),
            )
        };
        if self.st.baselines.values().any(|b| &b.change_id == id) {
            return Err(used());
        }
        // what the change wrote must be the tail of its nodes: nothing was built on it
        let mut cut: HashMap<NodeID, usize> = HashMap::new(); // first version to remove
        for (nid, vs) in &self.st.versions {
            for (i, v) in vs.iter().enumerate() {
                if &v.change_id != id {
                    continue;
                }
                cut.entry(nid.clone()).or_insert(i);
                if self.st.baselines.values().any(|b| b.contains(&v.r#ref())) {
                    return Err(used());
                }
            }
            if let Some(&i) = cut.get(nid)
                && vs[i..].iter().any(|v| &v.change_id != id)
            {
                return Err(used());
            }
        }
        let removed = |r: &NodeRef| cut.get(&r.id).is_some_and(|&i| r.version as usize > i);
        let mut kept = Vec::new();
        for l in &self.st.links {
            if &l.change_id == id || removed(&l.from) {
                continue;
            }
            if removed(&l.to) {
                return Err(used());
            }
            kept.push(l.clone());
        }
        let st = &mut *self.st;
        st.links = kept;
        for (nid, &i) in &cut {
            if let Some(vs) = st.versions.get(nid) {
                for v in &vs[i..] {
                    st.joins.remove(&JoinKey {
                        r#ref: v.r#ref(),
                        branch: branch_of(&v.branch).to_string(),
                    });
                }
            }
            if i == 0 {
                st.keys.retain(|_, kid| kid != nid);
                st.versions.remove(nid);
            } else if let Some(vs) = st.versions.get_mut(nid) {
                vs.truncate(i);
            }
        }
        st.joins.retain(|k, c| !(removed(&k.r#ref) || c == id));
        st.log.retain(|e| &e.change != id);
        st.nodes.remove(id);
        st.tags.retain(|t| &t.change_id != id);
        if !branch.is_empty() {
            st.branches.remove(&branch_key(namespace, branch));
        }
        if st.changes.remove(id).is_none() {
            return Err(not_found(format!("change {id}")));
        }
        Ok(())
    }

    async fn lock_change(&mut self, _ctx: &Ctx, _id: &ChangeID) -> Result<()> {
        Ok(()) // the transactions of the memory store are serialized
    }

    async fn lock_branch(&mut self, _ctx: &Ctx, _namespace: &str, _name: &str) -> Result<()> {
        Ok(()) // the transactions of the memory store are serialized
    }

    async fn commit(mut self: Box<Self>, _ctx: &Ctx) -> Result<()> {
        self.snapshot = None;
        Ok(())
    }
}
