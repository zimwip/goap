//! The steps of the methodology of a change that are possible NOW towards
//! its goal, the end point of the process. A step is possible when ALL its entry conditions hold, its exit criteria
//! do not, and nothing already carries it out; a step whose entry does not hold is never proposed. The proposals
//! are steps and method steps, never the actions behind them.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::Arc;

use futures::future::BoxFuture;
use goap_ctx::Ctx;
use goap_domain::{Blackboard, CHANGE_ABANDONED, CHANGE_APPLIED, CHANGE_COMMITTED, ChangeID};
use goap_methodology::{ActivityView, Compiled, METHOD_CAPABILITY, StepView, View};
use serde::{Deserialize, Serialize};

use crate::changestate::satisfies_all;
use crate::engine::Engine;
use crate::error::Result;
use crate::process::Process;
use crate::progress::{missing, satisfied};
use crate::serde_util::{is_false, is_zero_i64};

/// A step of a process.
pub const POINT_STEP: &str = "step";
/// The method a step names as a capability, or a step of that method.
pub const POINT_METHOD: &str = "method";

/// Launch is what starts a starting point: the fields of StartRequest (and of the StartProcess RPC) that run
/// exactly this point.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Launch {
    pub methodology: String,
    pub agent: String,
    pub goal: String,
    pub change_id: ChangeID,
}

/// StartingPoint is a step (or method step) that is possible now, as the engine binds it to the change (ADR 0126
/// step 5).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct StartingPoint {
    /// ID is the path of the step ("<process-or-method>/<step>/..."), unique in the methodology.
    pub id: String,
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub parent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub method: String,
    /// Why are the entry conditions that hold ("name" / "!name").
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub why: Vec<String>,
    pub launch: Launch,
    pub may_run: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub need_roles: Vec<String>,
    #[serde(skip_serializing_if = "is_false")]
    pub running: bool,
}

/// BlockedStep is a step towards the goal that is not possible yet, by its path: what its entry still misses.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct BlockedStep {
    pub id: String,
    pub missing: Vec<String>,
}

/// StartingPoints is the answer of Engine::starting_points.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct StartingPoints {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub version: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub goal: String,
    /// Reason says why there is nothing to start, when there is nothing and it is not an error.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
    pub points: Vec<StartingPoint>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub blocked: Vec<BlockedStep>,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub blocked_count: i64,
}

/// StartingPointsOptions tunes starting_points.
#[derive(Debug, Clone, Default)]
pub struct StartingPointsOptions {
    /// Methodology overrides the methodology of the change.
    pub methodology: String,
    /// Project is the project the caller would start on (empty: the one of the change).
    pub project: String,
    /// MaxPoints and MaxBlocked cap the lists (default 20 and 8).
    pub max_points: usize,
    pub max_blocked: usize,
}

impl Engine {
    /// starting_points computes the steps of the methodology of the change that are possible now towards the goal
    /// of the change. It reads the change and the methodology only; nothing is started.
    pub async fn starting_points(
        &self,
        ctx: &Ctx,
        change_id: &ChangeID,
        opts: StartingPointsOptions,
    ) -> Result<StartingPoints> {
        let mut bb = self.blackboard(ctx, change_id, "").await?;
        let ch = bb.change.clone();
        let mut out = StartingPoints {
            methodology: ch.methodology.clone(),
            goal: ch.goal.clone(),
            ..Default::default()
        };
        if !opts.methodology.is_empty() {
            out.methodology = opts.methodology.clone();
        }
        if ch.status == CHANGE_COMMITTED
            || ch.status == CHANGE_APPLIED
            || ch.status == CHANGE_ABANDONED
        {
            out.reason = format!("the change is {}", ch.status);
            return Ok(out);
        }
        if out.methodology.is_empty() {
            out.reason = "the change has no methodology".into();
            return Ok(out);
        }
        let mut r#ref = out.methodology.clone();
        if r#ref == ch.methodology {
            r#ref = ch.methodology_ref(); // the version the change follows (ADR 0113)
        }
        let m = self.methodologies.methodology(ctx, &r#ref).await?;
        out.methodology = m.name().to_string();
        out.version = m.version().to_string();
        // a change that predates the default goal (or a methodology chosen for a change that has none) is pulled by
        // the end point of the methodology
        if out.goal.is_empty()
            || (!opts.methodology.is_empty() && opts.methodology != ch.methodology)
        {
            out.goal = m.main_goal();
        }
        if out.goal.is_empty() {
            out.reason = "the methodology names no goal".into();
            return Ok(out);
        }
        let Some(goal) = m.goal(&out.goal) else {
            out.reason = format!("the goal {:?} is not known to the methodology", out.goal);
            return Ok(out);
        };
        bb.supertypes = self.supertypes_of(&m).into_iter().collect();
        let world = m.conditions.evaluate(&bb).state;
        if !goal.pre.is_empty() && satisfies_all(&world, &goal.pre) {
            out.reason = "the goal is reached".into();
            return Ok(out);
        }
        let project = if opts.project.is_empty() {
            ch.project_id.clone()
        } else {
            opts.project.clone()
        };
        let all = self.store.list(ctx).await?;
        let active: Vec<Process> = all
            .into_iter()
            .filter(|p| p.change_id == *change_id && !p.status.terminal())
            .collect();
        let v = m.view();
        let mut w = PointWalker {
            e: self,
            ctx,
            m: m.clone(),
            v: v.clone(),
            bb,
            world,
            needed: m.needed_by(&goal.pre),
            active,
            project,
            change: change_id.clone(),
            out,
            max_points: if opts.max_points == 0 {
                20
            } else {
                opts.max_points
            },
            max_blocked: if opts.max_blocked == 0 {
                8
            } else {
                opts.max_blocked
            },
        };
        for proc in &v.processes {
            if proc.nested || proc.agent.is_empty() {
                continue; // reached through the step that nests it
            }
            for s in &proc.steps {
                w.visit(s, &proc.agent, POINT_STEP, "", None).await?;
            }
        }
        let mut out = w.out;
        if out.points.is_empty() && out.reason.is_empty() {
            out.reason = if out.blocked_count > 0 {
                "no step is possible yet: the steps towards the goal wait for conditions".into()
            } else {
                "no step of the methodology works towards the goal".into()
            };
        }
        Ok(out)
    }
}

struct PointWalker<'a> {
    e: &'a Engine,
    ctx: &'a Ctx,
    m: Arc<Compiled>,
    v: Arc<View>,
    bb: Blackboard,
    world: BTreeMap<String, bool>,
    needed: BTreeSet<String>,
    active: Vec<Process>,
    project: String,
    change: ChangeID,
    out: StartingPoints,
    max_points: usize,
    max_blocked: usize,
}

impl<'a> PointWalker<'a> {
    /// visit examines a step from the top: a step that does not work towards the goal, that is done, or whose entry
    /// does not hold is not proposed (the last one is reported as blocked) and its sub-steps are not looked at; a
    /// possible step is proposed as a whole. A step naming a capability is proposed through the method that
    /// applies. me is set for the steps of a method (kind method).
    fn visit<'b>(
        &'b mut self,
        s: &'b StepView,
        agent: &'b str,
        kind: &'b str,
        parent: &'b str,
        me: Option<&'b ActivityView>,
    ) -> BoxFuture<'b, Result<()>>
    where
        'a: 'b,
    {
        Box::pin(async move {
            if kind == POINT_STEP && !self.towards_goal(s) {
                return Ok(());
            }
            if !s.exit.is_empty() && satisfies_all(&self.world, &s.exit) {
                return Ok(()); // done, or skipped
            }
            let miss = missing(&self.world, &s.entry, &s.exit);
            if !miss.is_empty() {
                self.block(s, miss);
                return Ok(());
            }
            if kind == POINT_STEP && s.method == METHOD_CAPABILITY && s.foreach.is_empty() {
                // which method applies depends on the change: bound here, among the methods of the view
                let mut choice: Option<ActivityView> = None;
                for c in self.m.methods_for(&s.capability, &self.bb) {
                    if let Some(mv) = self.v.method(&c.name) {
                        choice = Some(mv.clone());
                        break;
                    }
                }
                let Some(choice) = choice.filter(|c| !c.name.is_empty()) else {
                    self.block(s, s.unavailable.clone());
                    return Ok(());
                };
                if !choice.steps.is_empty() {
                    for ms in &choice.steps {
                        self.visit(ms, &choice.agent, POINT_METHOD, &s.path, Some(&choice))
                            .await?;
                    }
                    return Ok(());
                }
                let pt = StartingPoint {
                    kind: POINT_METHOD.into(),
                    method: choice.name.clone(),
                    launch: Launch {
                        agent: choice.agent.clone(),
                        goal: choice.goal.clone(),
                        ..Default::default()
                    },
                    ..Default::default()
                };
                return self.add(s, pt).await;
            }
            let mut pt = StartingPoint {
                kind: kind.into(),
                parent: parent.into(),
                launch: Launch {
                    agent: agent.into(),
                    goal: s.path.clone(),
                    ..Default::default()
                },
                ..Default::default()
            };
            if let Some(me) = me {
                pt.method = me.name.clone();
            }
            self.add(s, pt).await
        })
    }

    /// towards_goal reports whether the step reaches something the goal needs.
    fn towards_goal(&self, s: &StepView) -> bool {
        s.exit.keys().any(|k| self.needed.contains(k))
    }

    fn block(&mut self, s: &StepView, miss: Vec<String>) {
        self.out.blocked_count += 1;
        if self.out.blocked.len() < self.max_blocked {
            self.out.blocked.push(BlockedStep {
                id: s.path.clone(),
                missing: miss,
            });
        }
    }

    /// add completes and records a possible step.
    async fn add(&mut self, s: &StepView, mut pt: StartingPoint) -> Result<()> {
        if self.out.points.len() >= self.max_points {
            return Ok(());
        }
        pt.id = s.path.clone();
        pt.why = satisfied(&self.world, &s.entry, &s.exit);
        pt.launch.methodology = self.m.name().to_string();
        pt.launch.change_id = self.change.clone();
        pt.running = self.running(&pt, &s.owner);
        let (ok, roles) = self
            .e
            .may_run_agent(self.ctx, self.m.name(), &pt.launch.agent, &self.project)
            .await?;
        pt.may_run = ok;
        pt.need_roles = roles;
        self.out.points.push(pt);
        Ok(())
    }

    /// running reports whether a live process of the change already carries the step out.
    fn running(&self, pt: &StartingPoint, owner: &str) -> bool {
        for p in &self.active {
            if p.goal == pt.id
                || (p.agent == pt.launch.agent
                    && p.goal == pt.launch.goal
                    && p.methodology == pt.launch.methodology)
            {
                return true;
            }
            if let Some(st) = &p.step
                && (goap_methodology::step_within(&st.path, &pt.id)
                    || goap_methodology::step_within(&pt.id, &st.path))
            {
                return true;
            }
            if p.methodology == self.m.name() && p.goal == owner && p.agent == owner {
                return true; // the whole process is being run towards its goal: its planner sequences the step
            }
        }
        false
    }
}
