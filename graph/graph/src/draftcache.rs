//! Port of `draftcache.go`. The drafts of a change are derived from its log, not stored (ADR 0079 §2): the fold of the
//! impact events (`fold_drafts`, one event at a time: `apply_impact_event` then `apply_draft_event`), which `created`
//! and `checkedOut` open with a whole snapshot and the other events patch. A read folds from a cached copy: the cache
//! holds, per change, the fold up to a log position (seq); a read asks the log for the impact events after that
//! position (one indexed query, empty when nothing happened) and folds only those on top of it.
//!
//! The log is the truth and the cache a validated copy:
//!   - an entry is immutable (a fold builds a new one; the lists it returns are deep copies), keyed by the change and
//!     stamped with the seq of the last event it folds, so a second Graph on the same store (another process) or an
//!     entry that is behind catches up from the shared log;
//!   - an entry is published only by a transaction that appended nothing to that change's log: what it read is
//!     committed. A transaction that wrote (the operations that emit an event then read the draft) folds the cached
//!     prefix and its own newer events directly and publishes nothing, so a rollback leaves no trace in the cache;
//!   - the cache is bounded (LRU of `draft_cache_size` changes) and guarded by a mutex.
//!
//! Writers of one change are serialized (ADR 0127: the lock of its tree, taken before the first append; the
//! change_impact projection of ADR 0029 needs it too): with seqs handed out in commit order, "everything up to seq S"
//! is a stable prefix.

use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicI64, Ordering};

use goap_ctx::Ctx;
use goap_domain::{
    ChangeID, ChangeImpact, Draft, ImpactEvent, LOG_IMPACT, LogFilter, apply_draft_event,
    apply_impact_event,
};

use crate::changelog::decode_log;
use crate::repo::Tx;
use crate::{Graph, Result};

/// DefaultDraftCacheSize is the number of changes whose folded drafts the graph keeps in memory.
pub const DEFAULT_DRAFT_CACHE_SIZE: usize = 128;

/// draftState is the fold of a change's impact log up to seq: the change impacts (the drafts of a superseded impact
/// go) and the drafts. Immutable once published.
#[derive(Debug, Clone, Default)]
pub(crate) struct DraftState {
    pub(crate) change: ChangeID,
    pub(crate) seq: i64,
    pub(crate) impacts: Vec<ChangeImpact>,
    pub(crate) drafts: Vec<Draft>,
}

#[derive(Default)]
struct Lru {
    // the states and the tick of their last use (the most recently used has the highest)
    m: HashMap<ChangeID, (Arc<DraftState>, u64)>,
    tick: u64,
}

/// The cache of the folded drafts of the changes (Go's `draftCache`).
#[derive(Default)]
pub(crate) struct DraftCache {
    lru: parking_lot::Mutex<Lru>,
    /// folded counts the events folded since the graph started (tests: a read after N events folds the new ones
    /// only).
    pub(crate) folded: AtomicI64,
}

impl DraftCache {
    pub(crate) fn get(&self, id: &ChangeID) -> Option<Arc<DraftState>> {
        let mut l = self.lru.lock();
        l.tick += 1;
        let tick = l.tick;
        let e = l.m.get_mut(id)?;
        e.1 = tick;
        Some(e.0.clone())
    }

    /// put keeps a state when it is ahead of the one held.
    pub(crate) fn put(&self, s: Arc<DraftState>, size: usize) {
        let mut l = self.lru.lock();
        l.tick += 1;
        let tick = l.tick;
        if let Some(e) = l.m.get_mut(&s.change) {
            if e.0.seq < s.seq {
                e.0 = s;
            }
            e.1 = tick;
            return;
        }
        l.m.insert(s.change.clone(), (s, tick));
        while l.m.len() > size {
            let oldest =
                l.m.iter()
                    .min_by_key(|(_, (_, t))| *t)
                    .map(|(k, _)| k.clone());
            match oldest {
                Some(k) => {
                    l.m.remove(&k);
                }
                None => break,
            }
        }
    }

    pub(crate) fn drop_change(&self, id: &ChangeID) {
        self.lru.lock().m.remove(id);
    }

    /// The number of folds the cache made (read by the tests).
    #[cfg(test)]
    pub(crate) fn folded(&self) -> i64 {
        self.folded.load(Ordering::SeqCst)
    }
}

impl Graph {
    /// draftState folds the impact log of a change up to its last event as the transaction sees it.
    pub(crate) async fn draft_state(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
    ) -> Result<Arc<DraftState>> {
        let st = self.draft_states.get(id).unwrap_or_else(|| {
            Arc::new(DraftState {
                change: id.clone(),
                ..Default::default()
            })
        });
        let entries = tx
            .log(
                ctx,
                &LogFilter {
                    change: id.clone(),
                    types: vec![format!("{LOG_IMPACT}.")],
                    after_seq: st.seq,
                    ..Default::default()
                },
            )
            .await?;
        if entries.is_empty() {
            return Ok(st);
        }
        let events: Vec<ImpactEvent> =
            decode_log(&entries, |e: &mut ImpactEvent, l| e.seq = l.seq)?;
        let mut next = DraftState {
            change: id.clone(),
            seq: st.seq,
            impacts: st.impacts.clone(),
            drafts: st.drafts.clone(),
        };
        for e in &events {
            next.impacts = apply_impact_event(&next.impacts, e);
            next.drafts = apply_draft_event(&next.drafts, e, &next.impacts);
            next.seq = e.seq;
        }
        self.draft_states
            .folded
            .fetch_add(events.len() as i64, Ordering::SeqCst);
        let next = Arc::new(next);
        if !tx.wrote_log(id) {
            let size = if self.draft_cache_size == 0 {
                DEFAULT_DRAFT_CACHE_SIZE
            } else {
                self.draft_cache_size
            };
            self.draft_states.put(next.clone(), size);
        }
        Ok(next)
    }

    /// drafts lists the drafts of a change (every flow), by impact then flow, deep copies the caller may edit.
    pub(crate) async fn drafts(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
    ) -> Result<Vec<Draft>> {
        let st = self.draft_state(ctx, tx, id).await?;
        let mut out = st.drafts.clone();
        out.sort_by(|a, b| a.impact.cmp(&b.impact).then_with(|| a.flow.cmp(&b.flow)));
        Ok(out)
    }
}
