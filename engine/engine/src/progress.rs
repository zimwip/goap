//! Port of `progress.go`: where a process run stands in the steps of its process (ADR 0035 §3), a read model.

use std::collections::BTreeMap;

use goap_condition::Explanation;
use goap_ctx::Ctx;
use goap_domain::Blackboard;
use goap_methodology::{METHOD_CAPABILITY, Reference, Responsibilities, StepView};
use serde::{Deserialize, Serialize};

use crate::changestate::{Need, describe_needs};
use crate::engine::Engine;
use crate::error::{ERR_NOT_A_PROCESS, Error, Result};
use crate::foreach::FOREACH_SUFFIX;
use crate::process::{
    Process, STATUS_FAILED, STATUS_RUNNING, STATUS_STUCK, Status, TASK_AGENT, TASK_CONDITION,
};
use crate::serde_util::{is_zero_i64, nil_vec};

/// Step states of a process's progress (ADR 0035 §3).
/// Its exit criteria hold, and it ran.
pub const STEP_DONE: &str = "done";
/// Its entry holds and its exit criteria held without it running: nothing to do.
pub const STEP_SKIPPED: &str = "skipped";
/// Being carried out (its action runs, or its sub-agent / nested process works).
pub const STEP_ACTIVE: &str = "active";
/// Waiting for a person, or for conditions another process or person establishes.
pub const STEP_WAITING: &str = "waiting";
/// Its entry holds: the planner may take it.
pub const STEP_READY: &str = "ready";
/// Its entry does not hold yet (Missing says what is missing).
pub const STEP_TODO: &str = "todo";
/// The process is stuck or failed before it was done.
pub const STEP_BLOCKED: &str = "blocked";

/// ProcessProgress is where a process run stands in the steps of its process: a read model computed from the
/// definition and the run, never stored.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ProcessProgress {
    #[serde(rename = "processId")]
    pub process_id: String,
    pub methodology: String,
    pub process: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    pub status: Status,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    #[serde(serialize_with = "nil_vec")]
    pub steps: Vec<StepProgress>,
    /// Done counts the steps that run something (no sub-steps) done or skipped, out of Total (Go's untagged
    /// fields).
    #[serde(rename = "Done")]
    pub done: i64,
    #[serde(rename = "Total")]
    pub total: i64,
}

/// Lane is one stream of a step with foreach: the element it is carried out for, the method chosen for it and
/// where its agent instance stands.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Lane {
    pub item: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub method: String,
    #[serde(rename = "processId")]
    pub process_id: String,
    pub status: Status,
}

/// StepProgress is the state of one step, with its sub-steps.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct StepProgress {
    pub path: String,
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    /// Method: steps, action, agent, process or manual; Target what it runs.
    pub method: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub target: String,
    /// Chosen is the method chosen for a step that names a capability.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub chosen: String,
    /// Roles in force for the step (ADR 0035 §2).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub roles: Option<Responsibilities>,
    pub state: String,
    /// Missing are the entry conditions that do not hold ("name" when expected true, "!name" when expected false).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub missing: Vec<String>,
    /// Needs are the same conditions with the value they need and what each means (ADR 0112 §5).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub needs: Vec<Need>,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub runs: i64,
    #[serde(rename = "childProcessIds", skip_serializing_if = "Vec::is_empty")]
    pub child_process_ids: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub lanes: Vec<Lane>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub waiting: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub permission: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub guidance: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub checklist: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub references: Vec<Reference>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub steps: Vec<StepProgress>,
}

impl Engine {
    /// progress computes where the run id stands in the steps of the process its agent runs.
    pub async fn progress(&self, ctx: &Ctx, id: &str) -> Result<ProcessProgress> {
        let p = self.store.get(ctx, id).await?;
        let m = self.methodology_of(ctx, &p).await?;
        let Some(ag) = m.agent(&p.agent).filter(|a| !a.process_of().is_empty()) else {
            return Err(Error::NotAProcess(ERR_NOT_A_PROCESS.into()));
        };
        let v = m.view();
        let proc = v.process(ag.process_of()).cloned().unwrap_or_default();
        let mut out = ProcessProgress {
            process_id: p.id.clone(),
            methodology: p.methodology.clone(),
            process: proc.name.clone(),
            description: proc.description.clone(),
            status: p.status.clone(),
            error: p.error.clone(),
            ..Default::default()
        };
        for s in &proc.steps {
            out.steps.push(step_progress(&p, s));
        }
        self.add_lanes(ctx, &p, &mut out.steps).await?;
        let (done, total) = count_steps(&out.steps);
        out.done = done;
        out.total = total;
        let mut bb: Option<Blackboard> = None;
        if !p.change_id.is_empty()
            && let Ok(mut b) = self.blackboard(ctx, &p.change_id, &p.flow).await
        {
            b.supertypes = self.supertypes_of(&m).into_iter().collect();
            bb = Some(b);
        }
        describe_needs(&m, &p.world, bb.as_ref(), &mut out.steps);
        Ok(out)
    }

    /// add_lanes lists, for the steps that run for each element of a list, the streams started for them.
    pub(crate) fn add_lanes<'a>(
        &'a self,
        ctx: &'a Ctx,
        p: &'a Process,
        steps: &'a mut [StepProgress],
    ) -> futures::future::BoxFuture<'a, Result<()>> {
        Box::pin(async move {
            for s in steps.iter_mut() {
                self.add_lanes(ctx, p, &mut s.steps).await?;
                let prefix = format!("{}{FOREACH_SUFFIX}", s.path);
                for (key, child) in &p.children {
                    let Some(item) = key.strip_prefix(&prefix) else {
                        continue;
                    };
                    let c = self.store.get(ctx, child).await?;
                    let mut lane = Lane {
                        item: item.to_string(),
                        process_id: c.id.clone(),
                        status: c.status.clone(),
                        ..Default::default()
                    };
                    if let Some(st) = &c.step {
                        lane.method = st.method.clone();
                    }
                    s.lanes.push(lane);
                }
            }
            Ok(())
        })
    }

    /// explain_condition shows how a condition of the run's world state got its value against the change of the
    /// run: the formula, the value of each part of it and of the blackboard variables it reads. None for an unknown
    /// name.
    pub async fn explain_condition(
        &self,
        ctx: &Ctx,
        id: &str,
        name: &str,
    ) -> Result<Option<Explanation>> {
        let p = self.store.get(ctx, id).await?;
        let m = self.methodology_of(ctx, &p).await?;
        let mut bb = if p.change_id.is_empty() {
            Blackboard {
                vars: p.vars.clone(),
                ..Default::default()
            }
        } else {
            let mut bb = self.blackboard(ctx, &p.change_id, &p.flow).await?;
            bb.vars = p.vars.clone();
            bb
        };
        bb.supertypes = self.supertypes_of(&m).into_iter().collect();
        Ok(m.conditions.explain(name, &bb))
    }
}

/// count_steps counts the steps that run something (no sub-steps) done or skipped, and all of them.
pub(crate) fn count_steps(steps: &[StepProgress]) -> (i64, i64) {
    let (mut done, mut total) = (0, 0);
    for s in steps {
        if !s.steps.is_empty() {
            let (d, t) = count_steps(&s.steps);
            done += d;
            total += t;
            continue;
        }
        total += 1;
        if s.state == STEP_DONE || s.state == STEP_SKIPPED {
            done += 1;
        }
    }
    (done, total)
}

fn satisfies(world: &BTreeMap<String, bool>, want: &BTreeMap<String, bool>) -> bool {
    want.iter().all(|(k, v)| world.get(k) == Some(v))
}

/// step_progress binds a step of the compiled view to a run: where the run stands in it.
pub(crate) fn step_progress(p: &Process, s: &StepView) -> StepProgress {
    let mut sp = StepProgress {
        path: s.path.clone(),
        name: s.name.clone(),
        description: s.description.clone(),
        method: s.method.clone(),
        target: s.target.clone(),
        guidance: s.guidance.clone(),
        checklist: s.checklist.clone(),
        references: s.references.clone(),
        roles: s.roles.clone(),
        ..Default::default()
    };
    sp.missing = missing(&p.world, &s.entry, &s.exit);
    let done = !s.exit.is_empty() && satisfies(&p.world, &s.exit);
    if !s.steps.is_empty() {
        let mut ran = false;
        for c in &s.steps {
            let cp = step_progress(p, c);
            ran = ran || cp.runs > 0 || cp.state == STEP_DONE;
            sp.steps.push(cp);
        }
        sp.state = if done && ran {
            STEP_DONE.into()
        } else if done && sp.missing.is_empty() {
            STEP_SKIPPED.into()
        } else if !sp.missing.is_empty() && !ran {
            STEP_TODO.into() // not entered yet, whatever its criteria
        } else {
            aggregate(&sp.steps).into()
        };
        return sp;
    }
    for st in &p.steps {
        if s.planned.contains(&st.action) {
            sp.runs += 1;
            for c in &st.children {
                if !sp.child_process_ids.contains(c) {
                    sp.child_process_ids.push(c.clone());
                }
            }
        }
    }
    if sp.method == METHOD_CAPABILITY {
        for st in p.steps.iter().rev() {
            if s.planned.contains(&st.action) && !st.specialization.is_empty() {
                sp.chosen = st.specialization.clone(); // the method chosen for it
                break;
            }
        }
    }
    let pend = p.pending.as_ref().filter(|t| s.planned.contains(&t.action));
    if let Some(t) = pend
        && !t.child_process_id.is_empty()
        && !sp.child_process_ids.contains(&t.child_process_id)
    {
        sp.child_process_ids.push(t.child_process_id.clone());
    }
    let waits_here = p.waits_for_conditions()
        && p.pending
            .as_ref()
            .is_some_and(|t| sp.missing.iter().any(|c| t.conditions.contains(c)));
    if done && sp.runs > 0 {
        sp.state = STEP_DONE.into();
    } else if done && sp.missing.is_empty() {
        sp.state = STEP_SKIPPED.into(); // its turn came, and there was nothing to do
    } else if let Some(t) = pend {
        if t.kind == TASK_AGENT {
            sp.state = STEP_ACTIVE.into();
        } else {
            sp.state = STEP_WAITING.into();
            sp.waiting = t.kind.clone();
            sp.permission = t.permission.clone();
        }
    } else if waits_here {
        sp.state = STEP_WAITING.into();
        sp.waiting = TASK_CONDITION.into(); // its Missing say for what
    } else if p.status == STATUS_RUNNING && p.plan.first().is_some_and(|a| s.planned.contains(a)) {
        sp.state = STEP_ACTIVE.into();
    } else if p.status == STATUS_STUCK || p.status == STATUS_FAILED {
        sp.state = STEP_BLOCKED.into();
    } else if sp.missing.is_empty() {
        sp.state = STEP_READY.into();
    } else {
        sp.state = STEP_TODO.into();
    }
    sp
}

/// aggregate is the state of a step of sub-steps that is not done: the most advanced state among them.
pub(crate) fn aggregate(steps: &[StepProgress]) -> &'static str {
    for st in [STEP_WAITING, STEP_ACTIVE, STEP_BLOCKED, STEP_READY] {
        if steps.iter().any(|s| s.state == st) {
            return st;
        }
    }
    if steps.iter().any(|s| s.state == STEP_DONE) {
        return STEP_ACTIVE; // started, not finished
    }
    STEP_TODO
}

/// missing lists the entry conditions that do not hold in a world (the one a run last observed, or the one of a
/// change), leaving out the step's own "not done yet" guards.
pub(crate) fn missing(
    world: &BTreeMap<String, bool>,
    entry: &BTreeMap<String, bool>,
    exit: &BTreeMap<String, bool>,
) -> Vec<String> {
    entry_conditions(world, entry, exit, false)
}

/// satisfied lists the entry conditions that hold in the world, the counterpart of missing.
pub(crate) fn satisfied(
    world: &BTreeMap<String, bool>,
    entry: &BTreeMap<String, bool>,
    exit: &BTreeMap<String, bool>,
) -> Vec<String> {
    entry_conditions(world, entry, exit, true)
}

fn entry_conditions(
    world: &BTreeMap<String, bool>,
    entry: &BTreeMap<String, bool>,
    exit: &BTreeMap<String, bool>,
    holding: bool,
) -> Vec<String> {
    let mut out = Vec::new();
    for (k, v) in entry {
        if let Some(w) = exit.get(k)
            && w != v
        {
            continue; // the step's own "not done yet" guard
        }
        if (world.get(k) == Some(v)) == holding {
            out.push(if *v { k.clone() } else { format!("!{k}") });
        }
    }
    out.sort();
    out
}
