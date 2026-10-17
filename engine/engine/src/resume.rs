//! Steps replayed after a recovery never write twice (ADR 0124, Go's `resume.go`). With shared locks the engine
//! records a step on the process before running it (checkpoint); a run that finds such a step left by a replica that
//! stopped asks the change what that execution wrote before going on: its action record (the step ended: closed as
//! it is), other entries only (cut halfway: failed, not run again), nothing (dropped: the planner runs it again).

use std::collections::BTreeSet;

use goap_ctx::Ctx;
use goap_domain::LogFilter;
use goap_methodology::Compiled;

use crate::engine::Engine;
use crate::error::Result;
use crate::journal::action_record;
use crate::process::{Process, STATUS_RUNNING};

/// What an interrupted step wrote before its replica stopped.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Wrote {
    Nothing,
    Part,
    Ended,
}

impl Engine {
    /// checkpoint records the steps about to run on the process, when the replicas share it.
    pub(crate) async fn checkpoint(&self, ctx: &Ctx, p: &Process) -> Result<()> {
        if self.locks.is_none() {
            return Ok(());
        }
        self.store.put(ctx, p).await
    }

    /// settle_interrupted closes the steps a stopped replica left in flight on p. It changes p only.
    pub(crate) async fn settle_interrupted(
        &self,
        ctx: &Ctx,
        p: &mut Process,
        m: &Compiled,
    ) -> Result<()> {
        if p.status != STATUS_RUNNING || p.pending.is_some() {
            return Ok(());
        }
        let mut drop = BTreeSet::new();
        for i in 0..p.steps.len() {
            let s = &p.steps[i];
            if s.execution.is_empty() || s.ended_at.is_some() || !s.error.is_empty() {
                continue;
            }
            let execution = s.execution.clone();
            match self.wrote(ctx, p, &execution).await? {
                Wrote::Nothing => {
                    drop.insert(i);
                    tracing::info!(process = %p.id, action = %p.steps[i].action, "an interrupted step wrote nothing: it runs again");
                }
                Wrote::Ended => {
                    let now = self.clock();
                    let s = &mut p.steps[i];
                    s.ended_at = Some(now);
                    s.output = "recovered: the step had ended when its engine stopped".into();
                    tracing::info!(process = %p.id, action = %s.action, "an interrupted step had ended");
                }
                Wrote::Part => {
                    let now = self.clock();
                    let s = &mut p.steps[i];
                    s.ended_at = Some(now);
                    s.error = "interrupted after writing part of its work: its engine stopped; not run again".into();
                    let action = s.action.clone();
                    tracing::warn!(process = %p.id, action = %action, "an interrupted step wrote part of its work: failed");
                    let (kind, r) = match m.action(&action) {
                        Some(a) => (a.kind.clone(), a.step.clone()),
                        None => (String::new(), String::new()),
                    };
                    let rec = action_record(p, i, &kind, &r, &execution);
                    self.journal(ctx, p, vec![rec]).await;
                    self.record_failure(p, &action);
                }
            }
        }
        if !drop.is_empty() {
            let mut i = 0;
            p.steps.retain(|_| {
                i += 1;
                !drop.contains(&(i - 1))
            });
        }
        Ok(())
    }

    /// wrote tells what the execution of a step wrote on the change of p.
    async fn wrote(&self, ctx: &Ctx, p: &Process, execution: &str) -> Result<Wrote> {
        if p.change_id.is_empty() {
            return Ok(Wrote::Nothing);
        }
        let (entries, _) = self
            .graph
            .change_log(
                ctx,
                LogFilter {
                    change: p.change_id.clone(),
                    execution: execution.to_string(),
                    limit: 100,
                    ..Default::default()
                },
            )
            .await?;
        if entries.is_empty() {
            return Ok(Wrote::Nothing);
        }
        let action = format!(
            "{}.{}",
            goap_journal::STREAM_JOURNAL,
            goap_journal::KIND_ACTION
        );
        if entries.iter().any(|en| en.r#type == action) {
            return Ok(Wrote::Ended);
        }
        Ok(Wrote::Part)
    }
}
