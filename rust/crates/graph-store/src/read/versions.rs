//! What never changes once a version is written (ADR 0079, 0141): how it holds its links and the links it holds, kept
//! between transactions; and the versions a transaction read, kept for that transaction (Go's `versionCache` and the
//! reads of `guardTx`, `versioncache.go`). A commit or a merge of N nodes reads the versions it starts from again and
//! again: read once, they are not read again by the next operations on the same nodes, nor by the next transaction for
//! their links.
//!
//! Only a version read by a transaction that did not write it is kept (what a transaction wrote may roll back, its
//! number then taken again). A purge deletes versions (`delete_change`): it empties the cache, before and after its
//! statements, and a read that started before an emptying is not kept (the generation).
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
    /// The commits of the store that moved a branch.
    pub(crate) branch_moves: std::sync::atomic::AtomicU64,
    /// The root of the tree of each change last locked ([`Versions::root_of`]).
    roots: Mutex<HashMap<String, String>>,
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

/// How long the organisation checked at its head is taken as fresh while no commit of this store moved a branch.
pub(crate) const SNAPSHOT_FRESH: std::time::Duration = std::time::Duration::from_secs(1);

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
    /// Whether the transaction wrote a branch.
    branches: std::sync::atomic::AtomicBool,
    /// The branches the transaction read since its last lock or write of one: a lock may let another writer's move
    /// in, a write keeps columns of the row it does not name.
    branch_rows: Mutex<HashMap<(String, String), Option<graph_core::model::Branch>>>,
    /// The rows of `change_impact` the transaction writes, sent in one statement before its next statement but an
    /// entry of a log (which reads none of them): a batched operation writes an impact per node.
    impacts: Mutex<HeldImpacts>,
}

/// The rows of `change_impact` a transaction holds: each with the rank of its first write among the writes held, so
/// that a row written twice takes the position its first write would have (each write takes one of the sequence,
/// as its own statement would).
#[derive(Default)]
pub struct HeldImpacts {
    pub rows: Vec<(graph_core::model::ChangeId, graph_core::impact::ChangeImpact, usize)>,
    pub upserts: usize,
}

impl Read {
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
    pub fn wrote_branch(&self) {
        self.branches.store(true, std::sync::atomic::Ordering::Relaxed);
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

    /// Whether the transaction wrote a branch.
    pub fn wrote_branches(&self) -> bool {
        self.branches.load(std::sync::atomic::Ordering::Relaxed)
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
