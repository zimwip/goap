//! The plan an agent would make from a world state, for the methodology editor.

use std::collections::BTreeMap;

use goap_domain::Blackboard;
use goap_goap::{Planner, WorldState};
use goap_methodology::{
    Compiled, PLANNER_GOAP, PLANNER_HYBRID, PLANNER_LLM, PLANNER_LLM_SCORING, PLANNER_UTILITY,
};
use serde::{Deserialize, Serialize};

use crate::error::{ERR_LIVE_PLANNER, Error, Result};
use crate::planners::{operators, planning_goal, reweight_plan, utility_plan};

/// PlanStep is one action of a previewed plan (Go's untagged fields).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct PlanStep {
    /// Name is the planner action name (a step path, ":<alternative>" suffixed for an alternative action); Step is
    /// the step path it was generated from, Kind the action kind.
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "Step")]
    pub step: String,
    #[serde(rename = "Kind")]
    pub kind: String,
    #[serde(rename = "Cost")]
    pub cost: f64,
}

/// PlanPreview is the result of planning a goal from a (possibly overridden) world state: the methodology editor's
/// insight into the planner actually configured for an agent, without running it on a real change.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct PlanPreview {
    #[serde(rename = "Agent")]
    pub agent: String,
    #[serde(rename = "Goal")]
    pub goal: String,
    #[serde(rename = "Planner")]
    pub planner: String,
    /// Reached: the goal already holds in the given world; Actions is then empty.
    #[serde(rename = "Reached")]
    pub reached: bool,
    #[serde(rename = "Actions", serialize_with = "crate::serde_util::nil_vec")]
    pub actions: Vec<PlanStep>,
    #[serde(rename = "Cost")]
    pub cost: f64,
    /// Awaiting lists the conditions missing that no action of this agent establishes ("name" / "!name"), set only
    /// when no plan reaches the goal (ADR 0036 §3).
    #[serde(rename = "Awaiting", serialize_with = "crate::serde_util::nil_vec")]
    pub awaiting: Vec<String>,
}

/// preview_plan plans toward goal from bb's conditions, overridden by the given names, using the planner the agent
/// is configured with (goap, utility or hybrid). For a process, pass its name as both agent and goal; for a step
/// naming an agent or a capability, pass the step's agent / goal. bb may be empty.
pub fn preview_plan(
    m: &Compiled,
    bb: &Blackboard,
    agent: &str,
    goal_name: &str,
    overrides: &BTreeMap<String, bool>,
) -> Result<PlanPreview> {
    let Some(ag) = m.agent(agent) else {
        return Err(Error::msg(format!("unknown agent {agent:?}")));
    };
    let Some(goal) = m.goal(goal_name) else {
        return Err(Error::msg(format!("unknown goal {goal_name:?}")));
    };
    let res = m.conditions.evaluate(bb);
    let mut world = WorldState(res.state.clone());
    for (k, v) in overrides {
        world.0.insert(k.clone(), *v);
    }
    let actions = operators(&m.agent_actions(ag));
    let target = planning_goal(&goal);
    let planner_kind = if ag.planner.is_empty() {
        PLANNER_GOAP.to_string()
    } else {
        ag.planner.clone()
    };
    let mut out = PlanPreview {
        agent: ag.name.clone(),
        goal: goal.name.clone(),
        planner: planner_kind,
        ..Default::default()
    };
    if world.satisfies(&target.pre) {
        out.reached = true;
        return Ok(out);
    }
    let planner = Planner::default();
    let plan = match ag.planner.as_str() {
        "" | PLANNER_GOAP => planner.plan(&world, &actions, &target).map_err(Error::from),
        PLANNER_UTILITY => utility_plan(&world, &actions, &target, &m.utilities(bb)),
        PLANNER_HYBRID => reweight_plan(&planner, &world, &actions, &target, &m.utilities(bb)),
        PLANNER_LLM | PLANNER_LLM_SCORING => {
            return Err(Error::LivePlanner(ERR_LIVE_PLANNER.into()));
        }
        other => return Err(Error::msg(format!("unknown planner {other:?}"))),
    };
    let plan = match plan {
        Err(err) if err.is_no_plan() => {
            out.awaiting =
                crate::awaiting::awaited_with(&planner, &world, &actions, &actions, &target);
            return Ok(out);
        }
        r => r?,
    };
    for a in &plan.actions {
        let full = m.action(&a.name).cloned().unwrap_or_default();
        out.actions.push(PlanStep {
            name: a.name.clone(),
            step: full.step,
            kind: full.kind,
            cost: a.cost,
        });
    }
    out.cost = plan.cost;
    Ok(out)
}
