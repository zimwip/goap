//! What never changes once a version is written (ADR 0079, 0141): how it holds its links and the links it holds, kept
//! between transactions; and the versions a transaction read, kept for that transaction (Go's `versionCache` and the
//! reads of `guardTx`, `versioncache.go`). A commit or a merge of N nodes reads the versions it starts from again and
//! again: read once, they are not read again by the next operations on the same nodes, nor by the next transaction for
//! their links.
//!
//! Only a version read by a transaction that did not write it is kept (what a transaction wrote may roll back, its
//! number then taken again). A purge deletes versions (`delete_change`): it empties the cache, before and after its
//! statements, and a read that started before an emptying is not kept (the generation).
use chrono::Timelike;
use std::{
    collections::{BTreeMap, HashMap, HashSet},
    sync::Mutex,
    time::{Duration, Instant},
};

use graph_core::{
    model::{Link, Node, NodeRef, Target},
    store::LinkSet,
};

/// The bounds of the versions kept between transactions (Go's `Caches.Versions`: 32 MiB of estimated size, an entry no
/// one read for 5 minutes goes).
const MAX_BYTES: usize = 32 << 20;
const TTL: Duration = Duration::from_secs(5 * 60);

/// What is known of a version: that it exists, how it holds its links, the links it holds.
#[derive(Default, Clone)]
struct Entry {
    /// the version itself, read by a transaction that did not write it
    node: Option<Node>,
    set: Option<LinkSet>,
    links: Option<Vec<Link>>,
    exists: bool,
    bytes: usize,
    read: Option<Instant>,
    tick: u64,
}

/// The versions kept between the transactions of a store, the least recently read evicted first.
#[derive(Default)]
pub struct Versions {
    inner: Mutex<Inner>,
    /// The snapshot of the organisation last read, with the head of its main branch and the catalogue it was read
    /// at ([`graph_core::store::ReadTx::kept_snapshot`]).
    pub(crate) organisation: Mutex<Option<KeptSnapshot>>,
    /// The folds of the logs of the changes last read ([`graph_core::store::ReadTx::take_fold`]).
    folds: Mutex<Folds>,
    /// Per namespace, the commits of the store that moved one of its branches.
    pub(crate) branch_moves: Mutex<HashMap<String, u64>>,
    /// The root of the tree of each change last locked ([`Versions::root_of`]).
    roots: Mutex<HashMap<String, String>>,
    /// The units and projects the guard found ([`graph_core::store::ReadTx::structure_found`]): a node is never
    /// deleted once committed, so a structure found stays found.
    found: Mutex<HashSet<String>>,
    /// The headers of the baselines read: a baseline is written once and never deleted.
    headers: Mutex<HashMap<String, graph_core::model::Baseline>>,
    /// Whether a bus tells the moves of the other processes ([`Versions::follow_events`]).
    follows: std::sync::atomic::AtomicBool,
}

/// The number of change roots kept.
const MAX_ROOTS: usize = 4096;

/// The folds of change logs kept between transactions, the least recently read evicted first. A fold is the prefix of
/// a log up to an order: the writers of a change append under the lock of its tree and in order, so what a later read
/// finds past that order extends it.
#[derive(Default)]
struct Folds {
    tick: u64,
    entries: HashMap<String, (std::sync::Arc<graph_core::write::work::KeptFold>, u64)>,
}

/// The number of change folds kept.
const MAX_FOLDS: usize = 128;

/// A snapshot of the organisation, and what it was read at.
pub(crate) struct KeptSnapshot {
    pub head: String,
    pub catalog: std::sync::Arc<graph_catalog::Catalog>,
    pub snapshot: std::sync::Arc<graph_core::access::Snapshot>,
    /// the moves of branches the store had committed when its head was last checked, and when
    pub checked: Option<(u64, std::time::Instant)>,
}

/// How long the organisation checked at its head is taken as fresh while no commit of this store moved a branch, with
/// no bus (Go's `access.CommitAge`): a commit of another process is seen at most that late.
pub(crate) const SNAPSHOT_FRESH: std::time::Duration = std::time::Duration::from_secs(1);

/// The same with a bus telling the moves of the other processes (Go's `access.FollowAge`): a lost event is caught up
/// then.
pub(crate) const SNAPSHOT_FOLLOWED: std::time::Duration = std::time::Duration::from_secs(30);

#[derive(Default)]
struct Inner {
    era: u64,
    tick: u64,
    bytes: usize,
    entries: HashMap<NodeRef, Entry>,
    /// The entries by the tick of their last read: the first is the least recently read.
    order: BTreeMap<u64, NodeRef>,
}

impl Inner {
    fn get(&mut self, r: &NodeRef) -> Option<&Entry> {
        let now = Instant::now();
        let stale = self.entries.get(r)?.read.is_some_and(|t| now.duration_since(t) > TTL);
        if stale {
            self.remove(r);
            return None;
        }
        self.tick += 1;
        let tick = self.tick;
        let e = self.entries.get_mut(r)?;
        self.order.remove(&e.tick);
        e.tick = tick;
        e.read = Some(now);
        self.order.insert(tick, r.clone());
        self.entries.get(r)
    }

    fn remove(&mut self, r: &NodeRef) {
        if let Some(e) = self.entries.remove(r) {
            self.order.remove(&e.tick);
            self.bytes -= e.bytes;
        }
    }

    fn update(&mut self, r: &NodeRef, f: impl FnOnce(&mut Entry)) {
        let mut e = self.entries.remove(r).unwrap_or_default();
        self.order.remove(&e.tick);
        self.bytes -= e.bytes;
        f(&mut e);
        e.bytes = size(r, &e);
        if e.bytes > MAX_BYTES {
            return; // larger than the cache: not kept
        }
        self.tick += 1;
        e.tick = self.tick;
        e.read = Some(Instant::now());
        self.bytes += e.bytes;
        self.order.insert(e.tick, r.clone());
        self.entries.insert(r.clone(), e);
        while self.bytes > MAX_BYTES {
            let Some((_, oldest)) = self.order.pop_first() else {
                break;
            };
            if let Some(gone) = self.entries.remove(&oldest) {
                self.bytes -= gone.bytes;
            }
        }
    }
}

/// The estimated size of an entry (Go's size function of `newVersionCache`).
fn size(r: &NodeRef, e: &Entry) -> usize {
    const STRUCT: usize = 64;
    let mut n = STRUCT + r.id.as_str().len();
    if let Some(v) = &e.node {
        n += STRUCT * 2
            + v.key.len()
            + v.typ.len()
            + v.namespace.len()
            + v.comment.len()
            + v.props
                .as_ref()
                .map_or(0, |p| serde_json::Value::Object(p.clone()).to_string().len());
    }
    for l in e.links.iter().flatten() {
        let to = match &l.to {
            Target::Node(t) => t.id.as_str().len(),
            Target::External(u) => u.to_string().len(),
        };
        n += STRUCT + l.id.as_str().len() + l.typ.len() + l.from.id.as_str().len() + to + l.change_id.as_str().len();
        n += l
            .props
            .as_ref()
            .map_or(0, |p| serde_json::Value::Object(p.clone()).to_string().len());
    }
    n
}

impl Versions {
    /// A branch of a namespace moved in another process: its marks move ([`Store::moved`](crate::Store::moved)).
    pub fn moved(&self, namespace: &str) {
        let namespace = graph_core::model::namespace_of(namespace);
        let mut m = self.branch_moves.lock().unwrap_or_else(|e| e.into_inner());
        *m.entry(namespace.to_string()).or_default() += 1;
    }

    /// Every namespace moved.
    pub fn moved_all(&self) {
        let mut m = self.branch_moves.lock().unwrap_or_else(|e| e.into_inner());
        for v in m.values_mut() {
            *v += 1;
        }
        // a namespace with no mark yet answers 0 until its first move: give the organisation's a mark too
        drop(m);
        self.organisation.lock().unwrap_or_else(|e| e.into_inner()).take();
    }

    /// A bus tells the moves of the other processes.
    pub fn follow_events(&self) {
        self.follows.store(true, std::sync::atomic::Ordering::Relaxed);
    }

    /// How long the organisation checked at its head is taken as fresh while its marks do not move.
    pub(crate) fn snapshot_fresh(&self) -> std::time::Duration {
        if self.follows.load(std::sync::atomic::Ordering::Relaxed) {
            SNAPSHOT_FOLLOWED
        } else {
            SNAPSHOT_FRESH
        }
    }

    /// The generation a read starts at: what it learns is kept only while the cache was not emptied since.
    pub fn generation(&self) -> u64 {
        self.lock().era
    }

    /// Empties the cache: the reads in flight are not kept.
    pub fn reset(&self) {
        let mut c = self.lock();
        c.era += 1;
        c.entries.clear();
        c.order.clear();
        c.bytes = 0;
    }

    /// The links a version holds, when known.
    pub fn links(&self, r: &NodeRef) -> Option<Vec<Link>> {
        let mut c = self.lock();
        let e = c.get(r)?;
        if !e.exists {
            return None;
        }
        e.links.clone()
    }

    /// The version, when kept.
    pub fn node(&self, r: &NodeRef) -> Option<Node> {
        self.lock().get(r)?.node.clone()
    }

    /// Keeps a version read at `era`.
    pub fn keep_node(&self, era: u64, n: &Node) {
        let r = n.node_ref();
        self.at(era, &r, |e| {
            e.exists = true;
            e.node = Some(n.clone());
        });
    }

    /// A version whose origin a transaction sets: read again, and the reads in flight are not kept.
    pub fn forget_node(&self, r: &NodeRef) {
        let mut c = self.lock();
        c.era += 1;
        if c.entries.contains_key(r) {
            c.update(r, |e| e.node = None);
        }
    }

    /// How a version holds its links, when known.
    pub fn set(&self, r: &NodeRef) -> Option<LinkSet> {
        self.lock().get(r)?.set
    }

    /// Records that a version exists, read at `era`.
    pub fn known(&self, era: u64, r: &NodeRef) {
        self.at(era, r, |e| e.exists = true);
    }

    /// Records the links a version holds, read at `era`: an empty set is kept only for a version known to exist (the
    /// links of a version not written yet are none until it is).
    pub fn keep_links(&self, era: u64, r: &NodeRef, links: Vec<Link>) {
        let mut c = self.lock();
        if c.era != era || (links.is_empty() && !c.entries.get(r).is_some_and(|e| e.exists)) {
            return;
        }
        c.update(r, |e| {
            e.exists = true;
            e.links = Some(links);
        });
    }

    /// Records how a version holds its links, read at `era` (a set read is a version that exists).
    pub fn keep_set(&self, era: u64, r: &NodeRef, set: LinkSet) {
        self.at(era, r, |e| {
            e.set = Some(set);
            e.exists = true;
        });
    }

    fn at(&self, era: u64, r: &NodeRef, f: impl FnOnce(&mut Entry)) {
        let mut c = self.lock();
        if c.era == era {
            c.update(r, f);
        }
    }

    /// The fold of the log of a change, when kept.
    pub fn fold(&self, change: &str) -> Option<std::sync::Arc<graph_core::write::work::KeptFold>> {
        let mut f = self.folds.lock().unwrap_or_else(|e| e.into_inner());
        f.tick += 1;
        let tick = f.tick;
        let e = f.entries.get_mut(change)?;
        e.1 = tick;
        Some(e.0.clone())
    }

    /// Keeps the fold of the log of a change, unless one further along is kept already.
    pub fn keep_fold(&self, change: &str, fold: std::sync::Arc<graph_core::write::work::KeptFold>) {
        let mut f = self.folds.lock().unwrap_or_else(|e| e.into_inner());
        f.tick += 1;
        let tick = f.tick;
        if f.entries.get(change).is_some_and(|(k, _)| k.last >= fold.last) {
            return;
        }
        f.entries.insert(change.to_string(), (fold, tick));
        if f.entries.len() > MAX_FOLDS
            && let Some(old) = f.entries.iter().min_by_key(|(_, (_, t))| *t).map(|(k, _)| k.clone())
        {
            f.entries.remove(&old);
        }
    }

    /// The root of the tree of a change, as last locked: a guess its lock checks once held.
    pub fn root_of(&self, change: &str) -> Option<String> {
        self.roots
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .get(change)
            .cloned()
    }

    /// The header of a baseline, when read before.
    pub fn header(&self, id: &str) -> Option<graph_core::model::Baseline> {
        self.headers.lock().unwrap_or_else(|e| e.into_inner()).get(id).cloned()
    }

    /// Keeps the header of a baseline committed.
    pub fn keep_header(&self, b: &graph_core::model::Baseline) {
        let mut h = self.headers.lock().unwrap_or_else(|e| e.into_inner());
        if h.len() >= MAX_ROOTS * 4 {
            h.clear();
        }
        h.insert(b.id.0.clone(), b.clone());
    }

    /// Whether the guard found a unit or a project.
    pub fn found(&self, key: &str) -> bool {
        self.found.lock().unwrap_or_else(|e| e.into_inner()).contains(key)
    }

    /// The units and projects a committed transaction found.
    pub fn keep_found(&self, keys: Vec<String>) {
        let mut f = self.found.lock().unwrap_or_else(|e| e.into_inner());
        if f.len() + keys.len() > MAX_ROOTS {
            f.clear();
        }
        f.extend(keys);
    }

    pub fn keep_root(&self, change: &str, root: &str) {
        let mut r = self.roots.lock().unwrap_or_else(|e| e.into_inner());
        if r.len() >= MAX_ROOTS && !r.contains_key(change) {
            r.clear();
        }
        r.insert(change.to_string(), root.to_string());
    }

    /// Takes the fold of the log of a change out of what is kept.
    pub fn take_fold(&self, change: &str) -> Option<std::sync::Arc<graph_core::write::work::KeptFold>> {
        self.folds
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .entries
            .remove(change)
            .map(|(k, _)| k)
    }

    /// Forgets the fold of a change whose log is deleted.
    pub fn forget_fold(&self, change: &str) {
        self.folds
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .entries
            .remove(change);
    }

    fn lock(&self) -> std::sync::MutexGuard<'_, Inner> {
        self.inner.lock().unwrap_or_else(|e| e.into_inner())
    }
}

/// What one transaction keeps of the versions it reads (Go's `guardTx.nodes` and `written`).
#[derive(Default)]
pub struct Read {
    nodes: Mutex<HashMap<NodeRef, Node>>,
    written: Mutex<HashSet<NodeRef>>,
    /// The changes whose log the transaction wrote or deleted: a fold read after is not kept (a savepoint may undo
    /// what it holds).
    logs: Mutex<HashSet<String>>,
    /// The folds a write transaction keeps once it commits.
    folds: Mutex<Vec<(String, std::sync::Arc<graph_core::write::work::KeptFold>)>>,
    /// The namespaces of the branches the transaction wrote.
    branches: Mutex<HashSet<String>>,
    /// The branches the transaction read since its last lock or write of one: a lock may let another writer's move
    /// in, a write keeps columns of the row it does not name.
    branch_rows: Mutex<HashMap<(String, String), Option<graph_core::model::Branch>>>,
    /// The rows of `change_impact` the transaction writes, sent in one statement before its next statement but an
    /// entry of a log (which reads none of them): a batched operation writes an impact per node.
    impacts: Mutex<HeldImpacts>,
    /// The last position of the log of each change locked, read with its lock: no other writer appends to it while
    /// the lock is held.
    log_tops: Mutex<HashMap<String, i64>>,
    /// The units and projects the guard found, kept once the transaction commits.
    found: Mutex<Vec<String>>,
    /// The entries the transaction appended to the log of each change, as the log holds them.
    appended: Mutex<HashMap<String, Vec<graph_core::impact::LogEntry>>>,
    /// The versions whose origin the transaction set, read again by the next transactions once it commits.
    origins: Mutex<Vec<NodeRef>>,
    /// The baselines the transaction wrote: not kept, they may roll back.
    baselines: Mutex<HashSet<String>>,
    /// Whether a savepoint was undone: what it appended is not known any more.
    undone: std::sync::atomic::AtomicBool,
    /// What the transaction read of the commits of a branch in a range of orders, by node (none: the range holds no
    /// commit of it), until its next write (Go's `guardTx.baselineReads`): a merge reads the fork, the target and the
    /// branch of a change for its rebase, its checks and its fast-forward.
    selections: Mutex<HashMap<Selection, HashMap<graph_core::model::NodeId, Option<i32>>>>,
}

/// A range of commits of a branch: namespace, branch, after, up to.
pub type Selection = (String, String, i64, i64);

/// The rows of `change_impact` a transaction holds: each with the rank of its first write among the writes held, so
/// that a row written twice takes the position its first write would have (each write takes one of the sequence,
/// as its own statement would).
#[derive(Default)]
pub struct HeldImpacts {
    pub rows: Vec<(graph_core::model::ChangeId, graph_core::impact::ChangeImpact, usize)>,
    pub upserts: usize,
}

impl Read {
    /// What the transaction read of a range of commits for some nodes, and the nodes it did not read yet.
    pub fn selected(
        &self,
        key: &Selection,
        ids: &[graph_core::model::NodeId],
    ) -> (HashMap<graph_core::model::NodeId, i32>, Vec<graph_core::model::NodeId>) {
        let s = self.selections.lock().unwrap_or_else(|e| e.into_inner());
        let known = s.get(key);
        let mut out = HashMap::new();
        let mut missing = Vec::new();
        for id in ids {
            match known.and_then(|k| k.get(id)) {
                Some(Some(v)) => {
                    out.insert(id.clone(), *v);
                }
                Some(None) => {}
                None => missing.push(id.clone()),
            }
        }
        (out, missing)
    }

    /// Keeps what a range of commits gave for some nodes, until the next write.
    pub fn keep_selected(
        &self,
        key: Selection,
        ids: &[graph_core::model::NodeId],
        got: &HashMap<graph_core::model::NodeId, i32>,
    ) {
        let mut s = self.selections.lock().unwrap_or_else(|e| e.into_inner());
        let k = s.entry(key).or_default();
        for id in ids {
            k.insert(id.clone(), got.get(id).copied());
        }
    }

    /// A write: the commits read may have moved.
    pub fn written_any(&self) {
        let mut s = self.selections.lock().unwrap_or_else(|e| e.into_inner());
        if !s.is_empty() {
            s.clear();
        }
    }

    /// Whether what the transaction reads of a version cannot change any more: an exact version it did not write.
    pub fn fixed(&self, r: &NodeRef) -> bool {
        r.version > 0 && !self.written.lock().unwrap_or_else(|e| e.into_inner()).contains(r)
    }

    /// A version the transaction wrote: never kept.
    pub fn wrote(&self, r: &NodeRef) {
        self.written.lock().unwrap_or_else(|e| e.into_inner()).insert(r.clone());
        self.forget(r);
    }

    /// The version, when the transaction read it.
    pub fn node(&self, r: &NodeRef) -> Option<Node> {
        self.nodes.lock().unwrap_or_else(|e| e.into_inner()).get(r).cloned()
    }

    pub fn keep(&self, n: &Node) {
        self.nodes
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .insert(n.node_ref(), n.clone());
    }

    /// A version whose origin the transaction sets: read again.
    pub fn forget(&self, r: &NodeRef) {
        self.nodes.lock().unwrap_or_else(|e| e.into_inner()).remove(r);
    }

    /// A change whose log the transaction writes.
    pub fn wrote_log(&self, change: &str) {
        self.logs
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .insert(change.to_string());
    }

    /// A branch the transaction writes.
    pub fn wrote_branch(&self, namespace: &str) {
        self.branches
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .insert(namespace.to_string());
        self.forget_branches();
    }

    /// A branch the transaction read, when it reads it again.
    pub fn branch_row(&self, namespace: &str, name: &str) -> Option<Option<graph_core::model::Branch>> {
        self.branch_rows
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .get(&(namespace.to_string(), name.to_string()))
            .cloned()
    }

    pub fn keep_branch_row(&self, namespace: &str, name: &str, b: &Option<graph_core::model::Branch>) {
        self.branch_rows
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .insert((namespace.to_string(), name.to_string()), b.clone());
    }

    /// Holds a row of `change_impact` until the next statement, the last write of a row replacing the one held.
    pub fn hold_impact(&self, change: &graph_core::model::ChangeId, cn: &graph_core::impact::ChangeImpact) {
        let mut held = self.impacts.lock().unwrap_or_else(|e| e.into_inner());
        let at = held.upserts;
        held.upserts += 1;
        match held.rows.iter_mut().find(|r| r.1.id == cn.id) {
            Some(r) => r.1 = cn.clone(),
            None => held.rows.push((change.clone(), cn.clone(), at)),
        }
    }

    /// The rows of `change_impact` held, to write now.
    pub fn take_impacts(&self) -> HeldImpacts {
        std::mem::take(&mut *self.impacts.lock().unwrap_or_else(|e| e.into_inner()))
    }

    pub fn holds_impacts(&self) -> bool {
        !self.impacts.lock().unwrap_or_else(|e| e.into_inner()).rows.is_empty()
    }

    /// The branches read are read again: a lock taken, a branch written or deleted, a savepoint undone.
    pub fn forget_branches(&self) {
        self.branch_rows.lock().unwrap_or_else(|e| e.into_inner()).clear();
    }

    /// An entry the transaction appended at a position, as the log holds it (its time to the microsecond, an empty
    /// payload as null).
    pub fn appended(&self, e: &graph_core::impact::LogEntry, seq: i64) {
        let mut e = e.clone();
        e.seq = seq;
        e.at = e.at.with_nanosecond(e.at.nanosecond() / 1000 * 1000).unwrap_or(e.at);
        if e.payload.is_empty() {
            e.payload = "null".into();
        }
        self.appended
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .entry(e.change.0.clone())
            .or_default()
            .push(e);
    }

    /// A version whose origin the transaction sets.
    pub fn set_origin(&self, r: &NodeRef) {
        self.origins.lock().unwrap_or_else(|e| e.into_inner()).push(r.clone());
        self.forget(r);
    }

    /// The versions whose origin the transaction set.
    pub fn take_origins(&self) -> Vec<NodeRef> {
        std::mem::take(&mut *self.origins.lock().unwrap_or_else(|e| e.into_inner()))
    }

    /// A baseline the transaction writes.
    pub fn wrote_baseline(&self, id: &str) {
        self.baselines
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .insert(id.to_string());
    }

    /// Whether the transaction wrote a baseline.
    pub fn wrote_baseline_of(&self, id: &str) -> bool {
        self.baselines.lock().unwrap_or_else(|e| e.into_inner()).contains(id)
    }

    /// A savepoint undone.
    pub fn undone(&self) {
        self.undone.store(true, std::sync::atomic::Ordering::Relaxed);
        self.written_any();
    }

    /// The entries the transaction appended to the log of a change after the position its lock read, all of them:
    /// none when the lock read none or a savepoint was undone.
    pub fn tail_of(&self, change: &str, after: i64) -> Option<Vec<graph_core::impact::LogEntry>> {
        if self.undone.load(std::sync::atomic::Ordering::Relaxed)
            || self.log_tops.lock().unwrap_or_else(|e| e.into_inner()).get(change) != Some(&after)
        {
            return None;
        }
        let mut t = self.appended.lock().unwrap_or_else(|e| e.into_inner()).remove(change)?;
        t.sort_by_key(|e| e.seq);
        Some(t)
    }

    /// A unit or project the guard found.
    pub fn found(&self, key: &str) {
        self.found
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .push(key.to_string());
    }

    /// The units and projects the guard found, to keep at commit.
    pub fn take_found(&self) -> Vec<String> {
        std::mem::take(&mut *self.found.lock().unwrap_or_else(|e| e.into_inner()))
    }

    /// The last position of the log of a change read under its lock.
    pub fn keep_log_top(&self, change: &str, top: i64) {
        self.log_tops
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .insert(change.to_string(), top);
    }

    /// The last position of the log of a change as its lock read it, while the transaction wrote none of it.
    pub fn log_top(&self, change: &str) -> Option<i64> {
        if self.wrote_log_of(change) {
            return None;
        }
        self.log_tops
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .get(change)
            .copied()
    }

    /// The namespaces of the branches the transaction wrote.
    pub fn wrote_branches(&self) -> Vec<String> {
        self.branches
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .iter()
            .cloned()
            .collect()
    }

    /// Whether the transaction wrote a branch of a namespace.
    pub fn wrote_branch_of(&self, namespace: &str) -> bool {
        self.branches
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .contains(namespace)
    }

    /// Whether the transaction wrote the log of a change.
    pub fn wrote_log_of(&self, change: &str) -> bool {
        self.logs.lock().unwrap_or_else(|e| e.into_inner()).contains(change)
    }

    /// A fold to keep once the transaction commits.
    pub fn stage_fold(&self, change: &str, fold: std::sync::Arc<graph_core::write::work::KeptFold>) {
        self.folds
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .push((change.to_string(), fold));
    }

    /// Takes back the fold of a change the transaction staged last.
    pub fn take_staged(&self, change: &str) -> Option<std::sync::Arc<graph_core::write::work::KeptFold>> {
        let mut f = self.folds.lock().unwrap_or_else(|e| e.into_inner());
        let i = f.iter().rposition(|(c, _)| c == change)?;
        Some(f.remove(i).1)
    }

    /// What the transaction keeps once it commits: the folds staged, the logs it wrote.
    pub fn take_folds(
        &self,
    ) -> (
        Vec<(String, std::sync::Arc<graph_core::write::work::KeptFold>)>,
        Vec<String>,
    ) {
        let folds = std::mem::take(&mut *self.folds.lock().unwrap_or_else(|e| e.into_inner()));
        let logs = self
            .logs
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .iter()
            .cloned()
            .collect();
        (folds, logs)
    }

    pub fn clear(&self) {
        self.nodes.lock().unwrap_or_else(|e| e.into_inner()).clear();
        self.forget_branches();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use graph_core::model::{LinkId, NodeId};

    fn r(v: i32) -> NodeRef {
        NodeRef::new(NodeId("n".into()), v)
    }

    fn link(id: &str) -> Link {
        Link {
            id: LinkId(id.into()),
            typ: "t".into(),
            from: r(1),
            to: Target::Node(NodeRef::new(NodeId("m".into()), 1)),
            props: None,
            change_id: Default::default(),
        }
    }

    #[test]
    fn an_empty_set_is_kept_only_for_a_version_known_to_exist() {
        let c = Versions::default();
        let g = c.generation();
        c.keep_links(g, &r(1), Vec::new());
        assert!(c.links(&r(1)).is_none());
        c.known(g, &r(1));
        assert!(
            c.links(&r(1)).is_none(),
            "the empty set read before the version was known is not kept"
        );
        c.keep_links(g, &r(1), Vec::new());
        assert_eq!(c.links(&r(1)), Some(Vec::new()));
        c.keep_links(g, &r(2), vec![link("a")]);
        assert_eq!(c.links(&r(2)).map(|l| l.len()), Some(1));
    }

    #[test]
    fn a_read_before_an_emptying_is_not_kept() {
        let c = Versions::default();
        let g = c.generation();
        c.reset();
        c.keep_set(g, &r(1), LinkSet::default());
        assert!(c.set(&r(1)).is_none());
        let g = c.generation();
        c.keep_set(g, &r(1), LinkSet::default());
        assert!(c.set(&r(1)).is_some());
    }

    #[test]
    fn what_a_transaction_wrote_is_not_fixed() {
        let t = Read::default();
        assert!(t.fixed(&r(1)));
        assert!(!t.fixed(&r(0)));
        t.wrote(&r(1));
        assert!(!t.fixed(&r(1)));
    }
}
