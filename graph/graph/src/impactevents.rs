//! The change impacts are event-sourced (ADR 0029): every operation appends an event to the
//! change's log, and the change_impact table is the projection of that log, updated in the same transaction. Nothing
//! else writes it. The drafts of the nodes the change works on (ADR 0079) are no projection: they are folded from the
//! same log when read (`draftcache.rs`).

use goap_ctx::Ctx;
use goap_domain::{ChangeID, ChangeImpact, ImpactEvent, apply_impact_event, gotime};

use crate::changelog::{append_impact_event, impact_events};
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// caller names the principal behind an operation ([`Graph::caller`] hook), "" when unset.
    pub(crate) fn caller_of(&self, ctx: &Ctx) -> String {
        match &self.caller {
            Some(f) => f(ctx),
            None => String::new(),
        }
    }

    /// emit appends events to the impact logs and applies each one to the projection. It is the only writer of the
    /// change_impact projection (ADR 0029): every operation on the change impacts goes through it.
    pub(crate) async fn emit(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        events: Vec<ImpactEvent>,
    ) -> Result<()> {
        for mut e in events {
            if e.id.is_empty() {
                e.id = self.new_id();
            }
            if e.at.is_none_or(|t| t == gotime::zero()) {
                e.at = Some(self.now());
            }
            if e.by.is_empty() {
                e.by = self.caller_of(ctx);
            }
            if let Err(err) = e.validate() {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!("change {}: {err}", e.change),
                ));
            }
            let e = append_impact_event(ctx, tx, e).await?;
            let cur = tx.change_impacts(ctx, &e.change).await?;
            let next = apply_impact_event(&cur, &e);
            // a change impact the event removes (a creation cancelled before it is accepted, an impact removed, ADR
            // 0076)
            for cn in &cur {
                if !next.iter().any(|n| n.id == cn.id) {
                    tx.delete_change_impact(ctx, &e.change, &cn.id).await?;
                }
            }
            let cur: Vec<ChangeImpact> = cur
                .into_iter()
                .filter(|c| next.iter().any(|n| n.id == c.id))
                .collect();
            // the superseded ones first: a node has one live change impact per flow (index change_impact_live)
            let changed: Vec<&ChangeImpact> = next
                .iter()
                .enumerate()
                .filter(|(i, cn)| *i >= cur.len() || &cur[*i] != *cn)
                .map(|(_, cn)| cn)
                .collect();
            for first in [true, false] {
                for cn in &changed {
                    if cn.superseded == first {
                        tx.put_change_impact(ctx, &e.change, (*cn).clone()).await?;
                    }
                }
            }
        }
        Ok(())
    }

    /// ChangeEvents returns the impact log of a change (ADR 0029).
    pub async fn change_events(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<ImpactEvent>> {
        let mut tx = self.begin(ctx).await?;
        tx.change(ctx, id).await?;
        let out = impact_events(ctx, &mut tx, id).await?;
        tx.commit(ctx).await?;
        Ok(out)
    }
}
