//! Port of `changestate.go`: the current state of a change in its methodology (ADR 0112 §4). The steps are judged
//! against the change as it is (its conditions and its history), then what the live runs are doing is laid over
//! them. A step whose entry does not hold says which conditions it still needs, with what each means.

use std::collections::BTreeMap;

use goap_ctx::Ctx;
use goap_domain::{Blackboard, ChangeID};
use goap_goap::WorldState;
use goap_methodology::{ActivityView, Compiled};
use serde::{Deserialize, Serialize};

use crate::engine::Engine;
use crate::error::Result;
use crate::process::{Process, Status};
use crate::progress::{
    Lane, STEP_ACTIVE, STEP_BLOCKED, STEP_DONE, STEP_SKIPPED, STEP_WAITING, StepProgress,
    aggregate, count_steps, step_progress,
};
use crate::serde_util::{is_false, is_zero_i64};

/// Need is a condition a step waits for: its name, the value it needs and what it means.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Need {
    pub condition: String,
    pub want: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    /// ProducedBy are the actions that make it hold (an effect that is it, or implies it).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub produced_by: Vec<String>,
    /// Requires are the conditions it cannot hold without (ADR 0112 §5) that do not hold yet.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub requires: Vec<Need>,
    /// Facts are the terms of its expression with whether each holds now (ADR 0112 §5).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub facts: Vec<Fact>,
}

/// Fact is one term of the expression of a condition (goap_condition::Fact) with whether it holds now.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Fact {
    pub text: String,
    pub holds: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    /// Over is the list an exists term ranges over, Parts its predicate part by part.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub over: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub parts: Vec<Fact>,
    /// Missing marks the first term that does not hold; ProducedBy the actions establishing it.
    #[serde(skip_serializing_if = "is_false")]
    pub missing: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub produced_by: Vec<String>,
}

/// NeedState is a condition a step waits for, bound to the change (ADR 0126 step 4).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct NeedState {
    pub condition: String,
    pub want: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub requires: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub facts: Vec<Fact>,
}

/// StepState is where a step of the compiled view stands on a change (ADR 0126 step 4): the binding only, by the
/// path of the step.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct StepState {
    pub path: String,
    pub state: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub missing: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub needs: Vec<NeedState>,
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
    pub chosen: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub steps: Vec<StepState>,
}

/// ProcessState is where one process of the methodology of a change stands now.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ProcessState {
    pub process: String,
    /// State is the state of the process as a whole: done (its goal holds on the change), or the most advanced
    /// state of its steps.
    pub state: String,
    /// Runs counts the executions of the process on the change; Current is the run at work now (else the latest),
    /// Status its status, Error its error.
    pub runs: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub current: String,
    #[serde(skip_serializing_if = "Status::is_empty")]
    pub status: Status,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    pub steps: Vec<StepState>,
    #[serde(rename = "Done")]
    pub done: i64,
    #[serde(rename = "Total")]
    pub total: i64,
}

/// ChangeState is the current state of a change in its methodology: the binding of its compiled view
/// (Methodology at Version, ADR 0113) to the change.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ChangeState {
    pub change_id: ChangeID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub version: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub goal: String,
    #[serde(skip_serializing_if = "is_false")]
    pub goal_reached: bool,
    pub processes: Vec<ProcessState>,
}

/// satisfies_all reports whether every condition of want holds in the world.
pub(crate) fn satisfies_all(world: &BTreeMap<String, bool>, want: &BTreeMap<String, bool>) -> bool {
    want.iter()
        .all(|(k, v)| world.get(k).copied().unwrap_or(false) == *v)
}

impl Engine {
    /// change_state computes where the processes of the methodology of a change stand now. It reads the change,
    /// the methodology and the runs; nothing is stored.
    pub async fn change_state(&self, ctx: &Ctx, change_id: &ChangeID) -> Result<ChangeState> {
        let mut bb = self.blackboard(ctx, change_id, "").await?;
        let mut out = ChangeState {
            change_id: change_id.clone(),
            methodology: bb.change.methodology.clone(),
            goal: bb.change.goal.clone(),
            ..Default::default()
        };
        if out.methodology.is_empty() {
            return Ok(out);
        }
        // the version the change follows (ADR 0113)
        let m = self
            .methodologies
            .methodology(ctx, &bb.change.methodology_ref())
            .await?;
        out.version = m.version().to_string();
        bb.supertypes = self.supertypes_of(&m).into_iter().collect();
        let world = m.conditions.evaluate(&bb).state;
        if out.goal.is_empty() {
            out.goal = m.main_goal();
        }
        if let Some(g) = m.goal(&out.goal)
            && !g.pre.is_empty()
        {
            out.goal_reached = satisfies_all(&world, &g.pre);
        }
        let all = self.store.list(ctx).await?;
        let mut runs: Vec<Process> = all
            .into_iter()
            .filter(|p| p.change_id == *change_id && p.methodology == m.name())
            .collect();
        runs.sort_by_key(|p| p.created_at);
        let v = m.view();
        for proc in &v.processes {
            if proc.nested {
                continue; // shown in the step that nests it
            }
            let ps = self.process_state(ctx, &m, proc, &bb, &world, &runs).await;
            out.processes.push(ps);
        }
        Ok(out)
    }

    /// process_state lays the live runs of a process over its steps judged against the change.
    async fn process_state(
        &self,
        ctx: &Ctx,
        m: &Compiled,
        proc: &ActivityView,
        bb: &Blackboard,
        world: &BTreeMap<String, bool>,
        runs: &[Process],
    ) -> ProcessState {
        let mut ps = ProcessState {
            process: proc.name.clone(),
            ..Default::default()
        };
        // the runs of the process (its agent; a nested process or a method has its own)
        let mine: Vec<&Process> = runs
            .iter()
            .filter(|p| {
                m.agent(&p.agent)
                    .is_some_and(|ag| ag.process_of() == proc.name)
            })
            .collect();
        // the change as it is: every step run so far, no run at work
        let mut now = Process {
            world: WorldState(world.clone()),
            ..Default::default()
        };
        for p in &mine {
            now.steps.extend(p.steps.iter().cloned());
            ps.runs += 1 + p.runs;
        }
        let mut progress: Vec<StepProgress> =
            proc.steps.iter().map(|s| step_progress(&now, s)).collect();
        let mut live: Option<&Process> = None;
        for p in &mine {
            if p.status.terminal() {
                continue;
            }
            live = Some(*p);
            for (i, s) in proc.steps.iter().enumerate() {
                overlay(&mut progress[i], &step_progress(p, s));
            }
            if let Err(err) = self.add_lanes(ctx, p, &mut progress).await {
                tracing::warn!(process = %p.id, err = %err, "change state lanes");
            }
        }
        if let Some(l) = live.or(mine.last().copied()) {
            ps.current = l.id.clone();
            ps.status = l.status.clone();
            ps.error = l.error.clone();
        }
        let (done, total) = count_steps(&progress);
        ps.done = done;
        ps.total = total;
        ps.state = if !proc.done.is_empty() && satisfies_all(world, &proc.done) {
            STEP_DONE.into()
        } else {
            aggregate(&progress).into()
        };
        ps.steps = bind_steps(m, world, Some(bb), &progress);
        ps
    }
}

/// bind_steps keeps of the progress of steps what binds them to the change, their needs bound too.
fn bind_steps(
    m: &Compiled,
    world: &BTreeMap<String, bool>,
    bb: Option<&Blackboard>,
    steps: &[StepProgress],
) -> Vec<StepState> {
    let mut out = Vec::with_capacity(steps.len());
    for s in steps {
        let mut st = StepState {
            path: s.path.clone(),
            state: s.state.clone(),
            missing: s.missing.clone(),
            runs: s.runs,
            child_process_ids: s.child_process_ids.clone(),
            lanes: s.lanes.clone(),
            waiting: s.waiting.clone(),
            permission: s.permission.clone(),
            chosen: s.chosen.clone(),
            ..Default::default()
        };
        for c in &s.missing {
            st.needs.push(bind_need(m, world, bb, c));
        }
        if !s.steps.is_empty() {
            st.steps = bind_steps(m, world, bb, &s.steps);
        }
        out.push(st);
    }
    out
}

/// bind_need binds a missing entry condition ("name" / "!name") to the change: its prerequisites that do not hold
/// yet, and its facts against the blackboard, for one wanted true.
fn bind_need(
    m: &Compiled,
    world: &BTreeMap<String, bool>,
    bb: Option<&Blackboard>,
    missing: &str,
) -> NeedState {
    let (name, neg) = match missing.strip_prefix('!') {
        Some(n) => (n, true),
        None => (missing, false),
    };
    let mut n = NeedState {
        condition: name.to_string(),
        want: !neg,
        ..Default::default()
    };
    if !n.want {
        return n;
    }
    for p in m.view().condition(name).requires {
        if !world.get(&p).copied().unwrap_or(false) {
            n.requires.push(p);
        }
    }
    if let Some(bb) = bb {
        n.facts = facts_of(m, name, bb);
    }
    n
}

/// overlay lays what a live run is doing at a step over its state on the change: a step the change has done stays
/// done; one the run is at (active, waiting, blocked) takes the run's state.
fn overlay(dst: &mut StepProgress, run: &StepProgress) {
    for (i, d) in dst.steps.iter_mut().enumerate() {
        if let Some(r) = run.steps.get(i) {
            overlay(d, r);
        }
    }
    for c in &run.child_process_ids {
        if !dst.child_process_ids.contains(c) {
            dst.child_process_ids.push(c.clone());
        }
    }
    if dst.state == STEP_DONE || dst.state == STEP_SKIPPED {
        return;
    }
    if !dst.steps.is_empty() {
        dst.state = aggregate(&dst.steps).into();
        return;
    }
    if run.state == STEP_ACTIVE || run.state == STEP_WAITING || run.state == STEP_BLOCKED {
        dst.state = run.state.clone();
        dst.waiting = run.waiting.clone();
        dst.permission = run.permission.clone();
        if !run.chosen.is_empty() {
            dst.chosen = run.chosen.clone();
        }
    }
}

/// describe_needs fills the needs of the steps from what they miss, with what each condition means, the actions
/// producing it and, for one wanted true, the conditions it cannot hold without that do not hold yet.
pub(crate) fn describe_needs(
    m: &Compiled,
    world: &BTreeMap<String, bool>,
    bb: Option<&Blackboard>,
    steps: &mut [StepProgress],
) {
    let v = m.view();
    for s in steps.iter_mut() {
        describe_needs(m, world, bb, &mut s.steps);
        s.needs = Vec::new();
        for c in &s.missing {
            let b = bind_need(m, world, bb, c);
            let cv = v.condition(&b.condition);
            let mut n = Need {
                condition: b.condition.clone(),
                want: b.want,
                description: cv.description.clone(),
                facts: b.facts.clone(),
                ..Default::default()
            };
            if n.want {
                n.produced_by = cv.produced_by.clone();
            }
            for p in &b.requires {
                let pv = v.condition(p);
                n.requires.push(Need {
                    condition: p.clone(),
                    want: true,
                    description: pv.description.clone(),
                    produced_by: pv.produced_by.clone(),
                    ..Default::default()
                });
            }
            s.needs.push(n);
        }
    }
}

/// facts_of reads a condition into its terms against the blackboard, the first missing one with its producers.
fn facts_of(m: &Compiled, name: &str, bb: &Blackboard) -> Vec<Fact> {
    let Some(cf) = m.conditions.facts(name, bb) else {
        return Vec::new();
    };
    let miss = goap_condition::missing(&cf);
    let mut pending = miss.is_some();
    fn conv(
        m: &Compiled,
        fs: &[goap_condition::Fact],
        miss: Option<&goap_condition::Fact>,
        pending: &mut bool,
    ) -> Vec<Fact> {
        let mut out = Vec::with_capacity(fs.len());
        for f in fs {
            let mut v = Fact {
                text: f.text.clone(),
                holds: f.holds,
                error: f.error.clone(),
                over: f.over.clone(),
                parts: conv(m, &f.parts, miss, pending),
                ..Default::default()
            };
            if let Some(mf) = miss
                && *pending
                && !f.holds
                && f.text == mf.text
                && f.parts.is_empty()
            {
                v.missing = true;
                *pending = false;
                v.produced_by = m.producers_where(|effect| m.conditions.establishes(effect, mf));
            }
            out.push(v);
        }
        out
    }
    conv(m, &cf, miss.as_ref(), &mut pending)
}
