//! Cancelling a process (Go's `cancel.go`).

use std::sync::Arc;

use futures::future::BoxFuture;
use goap_ctx::{CancelHandle, Ctx};

use crate::engine::Engine;
use crate::error::{Error, Result};
use crate::process::{Process, STATUS_CANCELLED};

/// A run in flight: its cancel and an identity (compare-and-delete).
pub(crate) struct RunHandle {
    pub(crate) cancel: CancelHandle,
    pub(crate) id: u64,
}

impl Engine {
    /// cancel stops a process that has not ended: whoever answers for the run may (its initiator or the initiator of a
    /// run above it, the accountable role of its step, a holder of process:unblock). A run in flight is interrupted
    /// and cancel waits for it to let go; the process is then cancelled, and so are its live sub-processes.
    pub async fn cancel(self: &Arc<Self>, ctx: &Ctx, id: &str, reason: &str) -> Result<Process> {
        let p = self.store.get(ctx, id).await?;
        if p.status.terminal() {
            return Err(Error::invalid_state(format!(
                "process {id} has ended ({})",
                p.status
            )));
        }
        let who = goap_authz::principal_from(ctx);
        if !self.may_unblock(ctx, &p, &who).await? {
            return Err(Error::forbidden(format!(
                "{:?} does not answer for process {id}",
                who.subject
            )));
        }
        self.cancel_as(ctx, id, &who.subject, reason.trim()).await
    }

    pub(crate) fn cancel_as<'a>(
        self: &'a Arc<Self>,
        ctx: &'a Ctx,
        id: &'a str,
        by: &'a str,
        reason: &'a str,
    ) -> BoxFuture<'a, Result<Process>> {
        Box::pin(async move {
            self.cancelling
                .lock()
                .insert(id.to_string(), by.to_string());
            let res = self.cancel_inner(ctx, id, by, reason).await;
            self.cancelling.lock().remove(id);
            res
        })
    }

    async fn cancel_inner(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        by: &str,
        reason: &str,
    ) -> Result<Process> {
        self.interrupt(ctx, id, by).await;
        let p = {
            let _u = self.lock(id).await;
            let mut p = self.store.get(ctx, id).await?;
            if p.status.terminal() {
                return Ok(p); // it ended on its own meanwhile
            }
            p.status = STATUS_CANCELLED.into();
            p.pending = None;
            p.plan = Vec::new();
            p.queued = None;
            p.error = format!("cancelled by {by}");
            if !reason.is_empty() {
                p.error += &format!(": {reason}");
            }
            // the parent is cancelled first: a sub-process ending wakes a parent that still waits on it
            self.save(ctx, &mut p, STATUS_CANCELLED).await?;
            p
        };
        let all = self.store.list(ctx).await?;
        for c in all {
            if c.parent_id == id && !c.status.terminal() {
                self.cancel_as(ctx, &c.id, by, "its parent process was cancelled")
                    .await
                    .map_err(|e| e.context(format!("sub-process {}", c.id)))?;
            }
        }
        Ok(p)
    }

    /// cancel_requested reports whether Cancel was asked for the process.
    pub(crate) fn cancel_requested(&self, id: &str) -> bool {
        self.cancelling.lock().contains_key(id)
    }
}
