//! Port of `baselinedelta.go`. Baselines are stored as deltas (ADR 0032): a baseline keeps the entries that differ
//! from its parent, and every `checkpoint_every` baselines along a parent chain one is stored whole (a checkpoint),
//! which bounds what reading a baseline replays. The SQL stores use this; the memory store keeps whole maps. (Go's
//! `baselineEntriesSQL` is in `sqlbuild.rs`; `baselineEntry` / `baselineHeader` are [`BaselineEntry`] /
//! [`BaselineHeader`] of `repo.rs`, the `baselineRewriter` interface two default methods of [`Tx`].)

use std::collections::{BTreeMap, HashMap};
use std::sync::atomic::{AtomicI64, Ordering};

use goap_ctx::Ctx;
use goap_domain::{Baseline, BaselineID, NodeID, Version};

use crate::repo::{BaselineEntry, BaselineHeader, Tx};
use crate::{Graph, Result};

static CHECKPOINT_EVERY: AtomicI64 = AtomicI64::new(50);

/// checkpointEvery is the longest chain of deltas: a baseline whose parent is that many deltas away from its
/// checkpoint is stored whole.
pub(crate) fn checkpoint_every() -> i64 {
    CHECKPOINT_EVERY.load(Ordering::Relaxed)
}

/// Sets the checkpoint interval (tests; Go assigns `checkpointEvery`). It is process-wide: a test that changes it
/// holds [`CHECKPOINT_LOCK`].
#[cfg(test)]
pub(crate) fn set_checkpoint_every(n: i64) {
    CHECKPOINT_EVERY.store(n, Ordering::Relaxed);
}

/// Serializes the tests that change the checkpoint interval.
#[cfg(test)]
pub(crate) static CHECKPOINT_LOCK: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());

/// storedEntries returns the depth of a new baseline (0: a checkpoint) and the entries to store for it: its whole
/// content without a parent or at a checkpoint, else what differs from its parent. parent is None for a baseline
/// without one.
pub(crate) fn stored_entries(
    parent: Option<&Baseline>,
    parent_depth: i64,
    nodes: &BTreeMap<NodeID, Version>,
) -> (i64, Vec<BaselineEntry>) {
    let mut out = Vec::new();
    let mut depth = 0;
    match parent {
        Some(parent) if parent_depth + 1 < checkpoint_every() => {
            depth = parent_depth + 1;
            for (id, v) in nodes {
                if parent.nodes.get(id).copied().unwrap_or(0) != *v {
                    out.push(BaselineEntry {
                        node: id.clone(),
                        version: *v,
                        removed: false,
                    });
                }
            }
            for (id, v) in &parent.nodes {
                if !nodes.contains_key(id) {
                    out.push(BaselineEntry {
                        node: id.clone(),
                        version: *v,
                        removed: true,
                    });
                }
            }
        }
        _ => {
            for (id, v) in nodes {
                out.push(BaselineEntry {
                    node: id.clone(),
                    version: *v,
                    removed: false,
                });
            }
        }
    }
    out.sort_by(|a, b| a.node.cmp(&b.node));
    (depth, out)
}

impl Graph {
    /// CompactBaselines stores every baseline the way PutBaseline would today (ADR 0032 §3): the whole copies written
    /// before baselines were deltas become deltas, and the checkpoints fall every checkpoint_every baselines along
    /// each chain. The content of a baseline never changes, so it can run at any time; each baseline is rewritten in
    /// a transaction of its own. It returns how many were rewritten (0 on a store that keeps whole maps).
    pub async fn compact_baselines(&self, ctx: &Ctx) -> Result<usize> {
        let mut tx = self.begin(ctx).await?;
        let headers = tx.baseline_headers(ctx).await?;
        tx.commit(ctx).await?;
        let Some(headers) = headers else {
            return Ok(0);
        };
        let by_id: HashMap<BaselineID, BaselineHeader> =
            headers.iter().map(|h| (h.id.clone(), h.clone())).collect();
        let mut depth: HashMap<BaselineID, i64> = HashMap::new();
        fn desired(
            h: &BaselineHeader,
            hops: i64,
            by_id: &HashMap<BaselineID, BaselineHeader>,
            depth: &mut HashMap<BaselineID, i64>,
        ) -> i64 {
            if let Some(d) = depth.get(&h.id) {
                return *d;
            }
            let mut d = 0;
            if let Some(p) = by_id.get(&h.parent)
                && hops < 1 << 20
            {
                let pd = desired(p, hops + 1, by_id, depth);
                if pd + 1 < checkpoint_every() {
                    d = pd + 1;
                }
            }
            depth.insert(h.id.clone(), d);
            d
        }
        let mut n = 0;
        for h in &headers {
            // parents first: desired walks up before a child is rewritten
            let d = desired(h, 0, &by_id, &mut depth);
            if d == h.depth {
                continue;
            }
            let res: Result<()> = async {
                let mut tx = self.begin(ctx).await?;
                let b = tx.baseline(ctx, &h.id).await?;
                let parent = if d > 0 {
                    Some(tx.baseline(ctx, &h.parent).await?)
                } else {
                    None
                };
                let (_, entries) = stored_entries(parent.as_ref(), d - 1, &b.nodes);
                tx.rewrite_baseline(ctx, &h.id, d, &entries).await?;
                tx.commit(ctx).await
            }
            .await;
            if let Err(e) = res {
                return Err(e.context(format!("compact baseline {}", h.id)));
            }
            n += 1;
        }
        Ok(n)
    }
}
