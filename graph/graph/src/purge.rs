//! Port of `purge.go`: a change that landed nothing is removed with its whole log (ADR 0037).

use goap_ctx::Ctx;
use goap_domain::{
    CHANGE_ABANDONED, CHANGE_ACTIVE, CHANGE_DRAFT, Change, ChangeID, LOG_CHANGE, LOG_FACT,
    LOG_IMPACT, LogEntry, LogFilter,
};

use crate::changebranch::own_branch;
use crate::repo::Tx;
use crate::subchange::sub_changes;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// PurgeChange removes a change that landed nothing, with its whole log (ADR 0037). It is the exception to the
    /// insert-only log of ADR 0030: what is removed was never part of the graph. A change is refused (Conflict) unless
    /// it is draft, active or abandoned, has produced no baseline (no commit), has no sub-change, and its own branch,
    /// when it has one, has not moved since it was forked. A change whose log records an execution (an entry of a
    /// stream the graph does not write itself: the journal of a run, the exchanges with a model) is refused too: that
    /// record is the trace of what an AI did on whose behalf (EU AI Act Art. 12, 19), kept with the change, which can
    /// still be abandoned.
    pub async fn purge_change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change> {
        let mut tx = self.change_tx(ctx, id).await?;
        let c = tx.change(ctx, id).await?;
        if c.status != CHANGE_DRAFT && c.status != CHANGE_ACTIVE && c.status != CHANGE_ABANDONED {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {id} is {}, it landed in the graph", c.status),
            ));
        }
        if !c.result_baseline_id.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {id} produced baseline {}", c.result_baseline_id),
            ));
        }
        if !sub_changes(ctx, &mut tx, id).await?.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {id} has sub-changes"),
            ));
        }
        let kept = records_execution(ctx, &mut tx, id).await?;
        if !kept.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "change {id} records an execution ({kept} entries in its log), kept for audit: abandon it instead"
                ),
            ));
        }
        let mut branch = String::new();
        if let Some(own) = own_branch(ctx, &mut tx, &c).await? {
            if own.head != own.fork_baseline {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("change {id}: its branch received a merge"),
                ));
            }
            branch = own.name;
        }
        self.draft_states.drop_change(id);
        tx.delete_change(ctx, id, &c.namespace, &branch).await?;
        tx.commit(ctx).await?;
        Ok(c)
    }
}

/// recordsExecution names the first stream of the log of a change the graph did not write itself (empty: none).
pub(crate) async fn records_execution(ctx: &Ctx, tx: &mut dyn Tx, id: &ChangeID) -> Result<String> {
    let counts = tx
        .log_counts(
            ctx,
            &LogFilter {
                change: id.clone(),
                ..Default::default()
            },
        )
        .await?;
    for (typ, n) in counts {
        if n == 0 {
            continue;
        }
        let s = LogEntry {
            r#type: typ,
            ..Default::default()
        }
        .stream()
        .to_string();
        if s != LOG_FACT && s != LOG_IMPACT && s != LOG_CHANGE {
            return Ok(s);
        }
    }
    Ok(String::new())
}
