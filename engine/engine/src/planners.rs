//! The planners of the agents (Go's `planners.go`): goap (A*), utility, hybrid, llm and llm-scoring, fed with the
//! compiled methodology as plain operators (ADR 0126).

use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::Instant;

use goap_ctx::Ctx;
use goap_goap::{Action as Op, Goal as PlanGoal, Plan, Planner, WorldState};
use goap_llm::{Client, Message, Request};
use goap_methodology::{
    Action, Agent, Compiled, Goal, PLANNER_GOAP, PLANNER_HYBRID, PLANNER_LLM, PLANNER_LLM_SCORING,
    PLANNER_UTILITY,
};
use serde::{Deserialize, Serialize};

use crate::engine::Engine;
use crate::error::{ERR_PLANNER_ANSWER, Error, Result};
use crate::process::{LLMCall, exchange_of};
use crate::serde_util::go_marshal;

impl Engine {
    /// plan returns the next actions according to the agent's planner, and the LLM calls made while planning.
    #[allow(clippy::too_many_arguments)]
    pub(crate) async fn plan(
        &self,
        ctx: &Ctx,
        m: &Compiled,
        ag: &Agent,
        world: &WorldState,
        actions: &[Op],
        goal: &PlanGoal,
        utilities: &BTreeMap<String, f64>,
    ) -> (Result<Plan>, Vec<LLMCall>) {
        match ag.planner.as_str() {
            "" | PLANNER_GOAP => (
                self.planner.plan(world, actions, goal).map_err(Error::from),
                Vec::new(),
            ),
            PLANNER_UTILITY => (utility_plan(world, actions, goal, utilities), Vec::new()),
            PLANNER_HYBRID => (
                reweight_plan(&self.planner, world, actions, goal, utilities),
                Vec::new(),
            ),
            PLANNER_LLM => self.llm_plan(ctx, m, ag, world, actions, goal).await,
            PLANNER_LLM_SCORING => {
                let (u, calls) = self.llm_utilities(ctx, m, ag, world, actions, goal).await;
                match u {
                    Err(e) => (Err(e), calls),
                    Ok(u) => (
                        reweight_plan(&self.planner, world, actions, goal, &u),
                        calls,
                    ),
                }
            }
            other => (
                Err(Error::msg(format!("unknown planner {other:?}"))),
                Vec::new(),
            ),
        }
    }

    /// llm_plan asks the LLM (via the agent's model alias) to pick the next action directly.
    async fn llm_plan(
        &self,
        ctx: &Ctx,
        m: &Compiled,
        ag: &Agent,
        world: &WorldState,
        actions: &[Op],
        goal: &PlanGoal,
    ) -> (Result<Plan>, Vec<LLMCall>) {
        let mut rec = PlanRecorder {
            client: self.llm.clone(),
            calls: Vec::new(),
        };
        let body = go_marshal(&build_llm_plan_request(m, world, actions, goal));
        let resp = rec
            .complete(ctx, request(ag, LLM_PLANNER_SYSTEM, body))
            .await;
        let resp = match resp {
            Ok(r) => r,
            Err(e) => return (Err(e.context("llm planner")), rec.calls),
        };
        let out: LlmPlanResponse = match goap_llm::decode_json(&resp.text) {
            Ok(o) => o,
            Err(e) => {
                return (
                    Err(Error::PlannerAnswer(format!(
                        "llm planner: {e}: {ERR_PLANNER_ANSWER}"
                    ))),
                    rec.calls,
                );
            }
        };
        if out.action.is_empty() {
            return (Err(Error::Goap(goap_goap::Error::NoPlan)), rec.calls);
        }
        let Some(chosen) = actions.iter().find(|a| a.name == out.action) else {
            return (
                Err(Error::PlannerAnswer(format!(
                    "llm planner picked unavailable action {:?}: {ERR_PLANNER_ANSWER}",
                    out.action
                ))),
                rec.calls,
            );
        };
        if !world.satisfies(&chosen.pre) {
            return (
                Err(Error::PlannerAnswer(format!(
                    "llm planner picked action {:?} whose preconditions are not met: {ERR_PLANNER_ANSWER}",
                    out.action
                ))),
                rec.calls,
            );
        }
        (
            Ok(Plan {
                goal: goal.clone(),
                actions: vec![chosen.clone()],
                cost: chosen.cost,
            }),
            rec.calls,
        )
    }

    /// llm_utilities asks the LLM to score every candidate action's utility.
    async fn llm_utilities(
        &self,
        ctx: &Ctx,
        m: &Compiled,
        ag: &Agent,
        world: &WorldState,
        actions: &[Op],
        goal: &PlanGoal,
    ) -> (Result<BTreeMap<String, f64>>, Vec<LLMCall>) {
        let mut rec = PlanRecorder {
            client: self.llm.clone(),
            calls: Vec::new(),
        };
        let body = go_marshal(&build_llm_plan_request(m, world, actions, goal));
        let resp = match rec
            .complete(ctx, request(ag, LLM_SCORING_SYSTEM, body))
            .await
        {
            Ok(r) => r,
            Err(e) => return (Err(e.context("llm-scoring planner")), rec.calls),
        };
        let out: LlmScoringResponse = match goap_llm::decode_json(&resp.text) {
            Ok(o) => o,
            Err(e) => {
                return (
                    Err(Error::PlannerAnswer(format!(
                        "llm-scoring planner: {e}: {ERR_PLANNER_ANSWER}"
                    ))),
                    rec.calls,
                );
            }
        };
        let u = actions
            .iter()
            .map(|a| {
                (
                    a.name.clone(),
                    out.utilities.get(&a.name).copied().unwrap_or(0.0),
                )
            })
            .collect();
        (Ok(u), rec.calls)
    }
}

fn request(ag: &Agent, system: &str, body: String) -> Request {
    Request {
        model: ag.model.clone(),
        system: system.to_string(),
        json: true,
        max_tokens: 1000,
        messages: vec![Message {
            role: "user".into(),
            content: body,
            ..Default::default()
        }],
        ..Default::default()
    }
}

pub(crate) fn utility_plan(
    world: &WorldState,
    actions: &[Op],
    goal: &PlanGoal,
    utilities: &BTreeMap<String, f64>,
) -> Result<Plan> {
    if world.satisfies(&goal.pre) {
        return Ok(Plan {
            goal: goal.clone(),
            actions: Vec::new(),
            cost: 0.0,
        });
    }
    let u = |n: &str| utilities.get(n).copied().unwrap_or(0.0);
    let mut candidates: Vec<&Op> = actions
        .iter()
        .filter(|a| world.satisfies(&a.pre) && u(&a.name) > 0.0 && !world.satisfies(&a.effects))
        .collect();
    if candidates.is_empty() {
        return Err(Error::Goap(goap_goap::Error::NoPlan));
    }
    candidates.sort_by(|a, b| {
        let (ua, ub) = (u(&a.name), u(&b.name));
        if ua != ub {
            return ub.partial_cmp(&ua).unwrap_or(std::cmp::Ordering::Equal);
        }
        if a.cost != b.cost {
            return a
                .cost
                .partial_cmp(&b.cost)
                .unwrap_or(std::cmp::Ordering::Equal);
        }
        a.name.cmp(&b.name)
    });
    let best = candidates[0].clone();
    Ok(Plan {
        goal: goal.clone(),
        cost: best.cost,
        actions: vec![best],
    })
}

/// reweight_plan divides each action's cost by its utility (useless actions, utility <= 0, are excluded) and runs A*
/// over the reweighted set.
pub(crate) fn reweight_plan(
    planner: &Planner,
    world: &WorldState,
    actions: &[Op],
    goal: &PlanGoal,
    utilities: &BTreeMap<String, f64>,
) -> Result<Plan> {
    let mut kept = Vec::new();
    for a in actions {
        let u = utilities.get(&a.name).copied().unwrap_or(0.0);
        if u <= 0.0 {
            continue; // useless actions are excluded
        }
        let mut a = a.clone();
        let cost = if a.cost <= 0.0 { 1.0 } else { a.cost };
        a.cost = cost / u.max(0.01);
        if !a.name.is_empty() {
            kept.push(a);
        }
    }
    Ok(planner.plan(world, &kept, goal)?)
}

/// PlanRecorder records the planning calls the way Host::complete_llm does.
pub(crate) struct PlanRecorder {
    pub(crate) client: Option<Arc<dyn Client>>,
    pub(crate) calls: Vec<LLMCall>,
}

impl PlanRecorder {
    pub(crate) async fn complete(&mut self, ctx: &Ctx, req: Request) -> Result<goap_llm::Response> {
        let Some(client) = &self.client else {
            return Err(Error::msg("no model gateway configured"));
        };
        let mut meta = goap_llm::meta_from(ctx); // stamped by plan_step (ADR 0089)
        meta.call = self.calls.len() as i64;
        let start = Instant::now();
        let res = client
            .complete(&goap_llm::with_meta(ctx, meta), req.clone())
            .await;
        let resp = res.as_ref().cloned().unwrap_or_default();
        let mut call = LLMCall {
            provider: resp.provider.clone(),
            model: resp.model.clone(),
            input_tokens: resp.usage.input_tokens,
            output_tokens: resp.usage.output_tokens,
            duration_ms: start.elapsed().as_millis() as i64,
            exchange: Some(exchange_of(&req, &resp)),
            ..Default::default()
        };
        if call.model.is_empty() {
            call.model = req.model.clone();
        }
        if let Err(e) = &res {
            call.error = e.to_string();
        }
        self.calls.push(call);
        Ok(res?)
    }
}

#[derive(Serialize)]
struct LlmPlanAction {
    name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    description: String,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pre: BTreeMap<String, bool>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    effects: BTreeMap<String, bool>,
    #[serde(skip_serializing_if = "is_zero_f64")]
    cost: f64,
}

fn is_zero_f64(v: &f64) -> bool {
    *v == 0.0
}

#[derive(Serialize)]
struct LlmPlanGoal {
    name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    description: String,
    pre: BTreeMap<String, bool>,
}

#[derive(Serialize)]
struct LlmPlanRequest {
    world: BTreeMap<String, bool>,
    goal: LlmPlanGoal,
    #[serde(serialize_with = "crate::serde_util::nil_vec")]
    actions: Vec<LlmPlanAction>,
}

fn build_llm_plan_request(
    m: &Compiled,
    world: &WorldState,
    actions: &[Op],
    goal: &PlanGoal,
) -> LlmPlanRequest {
    LlmPlanRequest {
        world: world.0.clone(),
        goal: LlmPlanGoal {
            name: goal.name.clone(),
            description: m
                .goal(&goal.name)
                .map(|g| g.description)
                .unwrap_or_default(),
            pre: goal.pre.clone(),
        },
        actions: actions
            .iter()
            .map(|a| LlmPlanAction {
                name: a.name.clone(),
                description: m
                    .action(&a.name)
                    .map(|f| f.description.clone())
                    .unwrap_or_default(),
                pre: a.pre.clone(),
                effects: a.effects.clone(),
                cost: a.cost,
            })
            .collect(),
    }
}

const LLM_PLANNER_SYSTEM: &str = concat!(
    "You are the planner of an autonomous GOAP agent. Given the current world state ",
    "(booleans), a goal (preconditions to reach) and the available actions (name, description, preconditions, ",
    "effects, cost), answer ONLY with a JSON object {\"action\": \"<name>\", \"reason\": \"<why, briefly>\"} naming ",
    "exactly one action from the given list whose preconditions the world state already satisfies and that makes ",
    "progress toward the goal. If no such action exists, answer {\"action\": \"\", \"reason\": \"<why>\"}. Never invent ",
    "an action name that is not in the list."
);

#[derive(Deserialize, Default)]
#[serde(default)]
struct LlmPlanResponse {
    action: String,
    #[allow(dead_code)]
    reason: String,
}

const LLM_SCORING_SYSTEM: &str = concat!(
    "You are scoring the actions of an autonomous GOAP agent for usefulness. Given the ",
    "current world state, the goal and the available actions (name, description, preconditions, effects, cost), ",
    "answer ONLY with a JSON object {\"utilities\": {\"<action>\": <score>, ...}} with one non-negative number per ",
    "action: higher means more useful toward the goal from the current state; 0 means useless or inapplicable now. ",
    "Score every action listed."
);

#[derive(Deserialize, Default)]
#[serde(default)]
struct LlmScoringResponse {
    utilities: BTreeMap<String, f64>,
}

/// operator is an action as the planners see it (ADR 0126).
pub(crate) fn operator(a: &Action) -> Op {
    Op {
        name: a.name.clone(),
        pre: a.pre.clone(),
        effects: a.effects.clone(),
        cost: a.cost,
    }
}

/// operators are the actions an agent may run, as its planner plans over them.
pub(crate) fn operators(actions: &[Action]) -> Vec<Op> {
    actions.iter().map(operator).collect()
}

/// planning_goal is a goal of the methodology as the planners reach it.
pub(crate) fn planning_goal(g: &Goal) -> PlanGoal {
    PlanGoal {
        name: g.name.clone(),
        pre: g.pre.clone(),
        value: g.value,
    }
}
