//! Port of `state.go`. The state of a namespace after a change follows from what the change did (ADR 0056), so
//! storing the entries of a baseline (materialising it, a snapshot) is an acceleration: a baseline with a gap has only
//! its header stored. Its state is the state of the baseline before it with the change's work put in, according to its
//! kind: the `landed` events of the change for a commit or a 3-way merge, the versions the change joined to the branch
//! for a fast-forward. Every `materialize_every` baselines along a chain one is materialised.

use std::collections::{BTreeMap, HashMap, VecDeque};
use std::sync::atomic::{AtomicUsize, Ordering};

use goap_ctx::Ctx;
use goap_domain::{
    BASELINE_COMMIT, BASELINE_FAST_FORWARD, BASELINE_MERGE, BASELINE_SNAPSHOT, Baseline,
    BaselineID, IMPACT_LANDED, ImpactEvent, LOG_IMPACT, LogFilter, NodeID, NodeRef, Version,
    derived_kind,
};
use parking_lot::Mutex;

use crate::guard::GraphTx;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// The default of `DEFAULT_MATERIALIZE_EVERY` when GOAP_MATERIALIZE_EVERY does not set one.
const MATERIALIZE_EVERY_FALLBACK: usize = 16;

static DEFAULT_MATERIALIZE_EVERY: AtomicUsize = AtomicUsize::new(0);

/// DefaultMaterializeEvery is how often a baseline is materialised along a chain of baselines when the graph is given
/// no `materialize_every`: the others are computed. GOAP_MATERIALIZE_EVERY overrides it (1: every baseline is
/// materialised); [`set_default_materialize_every`] too (tests).
pub fn default_materialize_every() -> usize {
    let v = DEFAULT_MATERIALIZE_EVERY.load(Ordering::Relaxed);
    if v > 0 {
        return v;
    }
    let v = std::env::var("GOAP_MATERIALIZE_EVERY")
        .ok()
        .and_then(|s| s.trim().parse::<usize>().ok())
        .filter(|n| *n > 0)
        .unwrap_or(MATERIALIZE_EVERY_FALLBACK);
    DEFAULT_MATERIALIZE_EVERY.store(v, Ordering::Relaxed);
    v
}

/// Sets the process-wide default of `materialize_every` (Go assigns `DefaultMaterializeEvery`).
pub fn set_default_materialize_every(n: usize) {
    DEFAULT_MATERIALIZE_EVERY.store(n, Ordering::Relaxed);
}

impl Graph {
    pub(crate) fn materialize_every(&self) -> usize {
        if self.materialize_every > 0 {
            return self.materialize_every;
        }
        default_materialize_every()
    }

    /// Materialize stores the state of a baseline kept as a header only (a snapshot to speed up reading what follows
    /// it); a materialised one is left as it is.
    pub async fn materialize(&self, ctx: &Ctx, id: &BaselineID) -> Result<()> {
        let mut tx = self.begin(ctx).await?;
        tx.materialize(ctx, id).await?;
        tx.commit(ctx).await
    }
}

type State = BTreeMap<NodeID, Version>;

/// stateCache keeps the states computed by replaying the log: a state never changes once its baseline exists, so an
/// entry is never stale (an id written in a transaction that rolled back is never asked again). It is bounded.
#[derive(Default)]
pub struct StateCache {
    inner: Mutex<(HashMap<BaselineID, State>, VecDeque<BaselineID>)>,
}

const STATE_CACHE_SIZE: usize = 32;

impl StateCache {
    pub(crate) fn get(&self, id: &BaselineID) -> Option<State> {
        self.inner.lock().0.get(id).cloned()
    }

    pub(crate) fn put(&self, id: &BaselineID, s: &State) {
        let mut g = self.inner.lock();
        let (m, order) = &mut *g;
        if !m.contains_key(id) {
            order.push_back(id.clone());
            if order.len() > STATE_CACHE_SIZE
                && let Some(old) = order.pop_front()
            {
                m.remove(&old);
            }
        }
        m.insert(id.clone(), s.clone());
    }
}

impl GraphTx<'_> {
    /// step returns the state of a baseline kept as a header only, from the state of its parent.
    async fn step(&mut self, ctx: &Ctx, b: &Baseline, from: &State) -> Result<State> {
        let mut out = from.clone();
        async fn put(tx: &mut dyn Tx, ctx: &Ctx, out: &mut State, r: &NodeRef) -> Result<()> {
            let n = tx.node(ctx, r).await?;
            if n.deleted {
                out.remove(&n.id);
            } else {
                out.insert(n.id, n.version);
            }
            Ok(())
        }
        if b.kind == BASELINE_COMMIT || b.kind == BASELINE_MERGE {
            let entries = self
                .inner()
                .log(
                    ctx,
                    &LogFilter {
                        change: b.change_id.clone(),
                        types: vec![format!("{LOG_IMPACT}.{IMPACT_LANDED}")],
                        ..Default::default()
                    },
                )
                .await?;
            for e in entries {
                let ev: ImpactEvent = serde_json::from_value(e.payload.clone()).map_err(|err| {
                    Error::msg(format!(
                        "log entry {} of change {}: {err}",
                        e.seq, b.change_id
                    ))
                })?;
                if ev.baseline == b.id
                    && let Some(l) = &ev.landed
                {
                    put(self.inner(), ctx, &mut out, l).await?;
                }
            }
        } else if b.kind == BASELINE_FAST_FORWARD {
            let refs = self
                .inner()
                .branch_joins(ctx, &b.namespace, &b.branch, &b.change_id)
                .await?;
            // a node joined twice: the last version counts
            let mut latest: BTreeMap<NodeID, NodeRef> = BTreeMap::new();
            for r in refs {
                let cur = latest.get(&r.id).map(|x| x.version).unwrap_or(0);
                if r.version > cur {
                    latest.insert(r.id.clone(), r);
                }
            }
            for r in latest.values() {
                put(self.inner(), ctx, &mut out, r).await?;
            }
        } else {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "baseline {} is a {:?}: its state is not derived, it is always stored",
                    b.id, b.kind
                ),
            ));
        }
        Ok(out)
    }

    /// nodesOf returns the state of a baseline read from the store: its entries, or, for a baseline kept as a header
    /// only, the state computed from the nearest snapshot before it.
    pub(crate) async fn nodes_of(&mut self, ctx: &Ctx, b: &Baseline) -> Result<State> {
        if b.gap == 0 {
            return Ok(b.nodes.clone());
        }
        if let Some(s) = self.g.states.get(&b.id) {
            return Ok(s);
        }
        // walk up to a state known (a snapshot, a cached one, the empty state), then apply the moves down
        let mut chain = vec![b.clone()];
        let mut cur = b.clone();
        let mut base: State = loop {
            if cur.parent_id.is_empty() {
                break State::new();
            }
            if let Some(s) = self.g.states.get(&cur.parent_id) {
                break s;
            }
            let p = self.inner().baseline(ctx, &cur.parent_id).await?;
            if p.gap == 0 {
                break p.nodes;
            }
            chain.push(p.clone());
            cur = p;
        };
        for b in chain.iter().rev() {
            base = self.step(ctx, b, &base).await?;
            self.g.states.put(&b.id, &base);
        }
        Ok(base)
    }

    /// Baseline reads a baseline, its nodes included whether it is materialised or computed; the empty id is the empty
    /// state, what the first change of a namespace starts from (ADR 0056): no baseline holds it, it is nothing.
    pub(crate) async fn guarded_baseline(
        &mut self,
        ctx: &Ctx,
        id: &BaselineID,
    ) -> Result<Baseline> {
        if id.is_empty() {
            return Ok(Baseline::default());
        }
        let mut b = self.inner().baseline(ctx, id).await?;
        if b.gap == 0 {
            return Ok(b);
        }
        b.nodes = self.nodes_of(ctx, &b).await?;
        Ok(b)
    }

    /// Baselines lists the baselines of a namespace, their nodes included.
    pub(crate) async fn guarded_baselines(
        &mut self,
        ctx: &Ctx,
        namespace: &str,
    ) -> Result<Vec<Baseline>> {
        let mut bs = self.inner().baselines(ctx, namespace).await?;
        for b in &mut bs {
            if b.gap > 0 {
                b.nodes = self.nodes_of(ctx, b).await?;
            }
        }
        Ok(bs)
    }

    /// materialize stores the state of a baseline kept as a header only.
    pub(crate) async fn materialize(&mut self, ctx: &Ctx, id: &BaselineID) -> Result<()> {
        let b = self.inner().baseline(ctx, id).await?;
        if b.gap == 0 {
            return Ok(());
        }
        let nodes = self.nodes_of(ctx, &b).await?;
        self.inner().materialize_baseline(ctx, id, &nodes).await
    }

    /// NodesIn lists the nodes of a baseline of a type: the store reads them from the entries, so a baseline kept as a
    /// header only is materialised first.
    pub(crate) async fn guarded_nodes_in(
        &mut self,
        ctx: &Ctx,
        id: &BaselineID,
        node_type: &str,
    ) -> Result<Vec<goap_domain::Node>> {
        if id.is_empty() {
            return Ok(Vec::new());
        }
        self.materialize(ctx, id).await?;
        self.inner().nodes_in(ctx, id, node_type).await
    }

    /// PutBaseline writes a baseline, materialised only every `materialize_every` baselines along its chain, and
    /// always when its state is not derived from what its change did (a snapshot, the first baseline of a chain) (ADR
    /// 0056). It returns the baseline as stored (its gap).
    pub(crate) async fn guarded_put_baseline(
        &mut self,
        ctx: &Ctx,
        mut b: Baseline,
    ) -> Result<Baseline> {
        self.need_change(&b.change_id, &format!("baseline {}", b.name))?;
        b.gap = 0;
        if !b.parent_id.is_empty() && derived_kind(&b.kind) {
            let parent = self.inner().baseline(ctx, &b.parent_id).await?;
            b.gap = parent.gap + 1;
            if b.gap >= self.g.materialize_every() as i64 {
                b.gap = 0;
            }
        }
        self.inner().put_baseline(ctx, b.clone()).await?;
        self.g.states.put(&b.id, &b.nodes);
        Ok(b)
    }
}

/// kindOf is the kind stored for a baseline: one written without a kind is a snapshot.
pub(crate) fn kind_of(b: &Baseline) -> String {
    if b.kind.is_empty() {
        BASELINE_SNAPSHOT.to_string()
    } else {
        b.kind.clone()
    }
}
