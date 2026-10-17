//! The steps of processes (ADR 0034): the actions of kind call and the context of a step.

use std::collections::BTreeMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_authz::Principal;
use goap_ctx::Ctx;
use goap_domain::Blackboard;
use goap_methodology::{
    Action, Compiled, Method, MethodChoice, Reference, Responsibilities, StepInfo,
};
use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::engine::Engine;
use crate::error::{Error, Result};
use crate::executors::{ActionContext, ActionResult, Executor};
use crate::foreach::run_foreach;
use crate::process::{Process, STATUS_COMPLETED};
use crate::scope::StepRef;
use crate::serde_util::null_default;

/// RunCall runs an action of kind call (ADR 0034): the action the compiler generated for a step of a process done by
/// a nested process or by the method providing a capability. It starts the sub-agent its Call names (or the agent of
/// the method chosen for the capability, towards its goal) on the same change and waits for it; once it has
/// completed, the step has run. A sub-agent that fails fails the step; one that is stuck keeps the step waiting.
pub async fn run_call(ctx: &Ctx, ac: &ActionContext) -> Result<ActionResult> {
    let (h, call) = match (&ac.host, &ac.action.call) {
        (None, _) => return Err(Error::msg("a call needs a process on a change")),
        _ if ac.process.change_id.is_empty() => {
            return Err(Error::msg("a call needs a process on a change"));
        }
        (_, None) => {
            return Err(Error::msg(format!(
                "action {} of kind {} states no call",
                ac.action.name, ac.action.kind
            )));
        }
        (Some(h), Some(c)) => (h.clone(), c.clone()),
    };
    let e = h.engine();
    let step = if ac.action.step.is_empty() {
        ac.action.name.clone()
    } else {
        ac.action.step.clone()
    };
    let (mut agent, mut goal) = (call.agent.clone(), call.goal.clone());
    let methodology_name = if call.methodology.is_empty() {
        ac.process.methodology.clone()
    } else {
        call.methodology.clone()
    };
    let mut sc = ac.step.clone();
    let mut method = String::new();
    if !call.foreach.is_empty() {
        return run_foreach(ctx, ac, &step, &call).await;
    }
    if !call.capability.is_empty() {
        let me = e
            .choose_method(ctx, ac, &call.capability, &ac.blackboard, true)
            .await
            .map_err(|err| err.context(format!("step {step}")))?;
        agent = me.actor_agent().to_string();
        goal = me.agent_goal.clone();
        method = me.name.clone();
        sc = Some(StepContext::with_method(sc.as_ref(), &me.method));
    }
    if agent.is_empty() || goal.is_empty() {
        return Err(Error::msg(format!(
            "step {step}: the call names no agent and goal, and no capability"
        )));
    }
    let mut intent = if ac.action.description.is_empty() {
        format!("step {step}")
    } else {
        ac.action.description.clone()
    };
    if !ac.blackboard.change.intent.is_empty() {
        intent += &format!("\n\n(change: {})", ac.blackboard.change.intent);
    }
    let res = e
        .run_child_step(
            &e.as_(ctx, &ac.process),
            &h,
            &format!("{}#step", ac.action.name),
            &methodology_name,
            &agent,
            &goal,
            &intent,
            sc,
        )
        .await;
    match res {
        Err(err) if err.is_suspended() => Ok(ActionResult {
            suspended: true,
            child: h.waiting_on(),
            method,
            output: format!("step {step}: {methodology_name}/{agent} at work"),
            ..Default::default()
        }),
        Err(err) => Err(err.context(format!("step {step}"))),
        Ok(res) if res.status != STATUS_COMPLETED => Err(Error::msg(format!(
            "step {step}: {methodology_name}/{agent} ended {} (process {})",
            res.status, res.process_id
        ))),
        Ok(res) => Ok(ActionResult {
            method,
            output: format!(
                "step {step} done by {methodology_name}/{agent} (process {})",
                res.process_id
            ),
            ..Default::default()
        }),
    }
}

impl Engine {
    /// choose_method picks the method of a capability for the step being run (ADR 0035 §1): the applicable one with
    /// the highest priority whose agent the unit holding the change can run (the MCPs the agent declares are bound).
    /// A step resumed after its sub-agent keeps the sub-agent it started.
    pub(crate) async fn choose_method(
        self: &Arc<Self>,
        ctx: &Ctx,
        ac: &ActionContext,
        capability: &str,
        bb: &Blackboard,
        resumable: bool,
    ) -> Result<MethodChoice> {
        let m = self.methodology_of(ctx, &ac.process).await?;
        if resumable
            && ac
                .process
                .children
                .contains_key(&format!("{}#step", ac.action.name))
        {
            // the sub-agent started for the step is at work: keep the method it was started for
            for st in ac.process.steps.iter().rev() {
                if st.action == ac.action.name && !st.specialization.is_empty() {
                    for me in m.methods_for(capability, bb) {
                        if me.name == st.specialization {
                            return Ok(me);
                        }
                    }
                    if let Some(me) = m.method_by_name(&st.specialization) {
                        return Ok(MethodChoice {
                            method: me.clone(),
                            agent_goal: m.method_goal(&me.name),
                        });
                    }
                }
            }
        }
        let candidates = m.methods_for(capability, bb);
        let mut bound: Option<BTreeMap<String, bool>> = None;
        let n = candidates.len();
        for me in candidates {
            let mcps = m
                .agent(me.actor_agent())
                .map(|a| a.mcps.clone())
                .unwrap_or_default();
            if !mcps.is_empty() && bound.is_none() {
                bound = Some(self.bound_mcps(ctx, &ac.process).await?);
            }
            let empty = BTreeMap::new();
            let b = bound.as_ref().unwrap_or(&empty);
            if !mcps.iter().any(|n| !b.get(n).copied().unwrap_or(false)) {
                return Ok(me);
            }
        }
        Err(Error::msg(format!(
            "no method for {capability:?} applies here ({n} in this context, none the unit can run)"
        )))
    }

    /// step_allowed reports whether who may act on the step of a context: "perform" (its responsible role) or
    /// "approve" (its accountable role), in the unit holding the change (ADR 0035 §2). A step that assigns no such
    /// role leaves the decision to the process's own permissions (true).
    pub(crate) async fn step_allowed(
        &self,
        ctx: &Ctx,
        p: &Process,
        who: &Principal,
        sc: Option<&StepContext>,
        act: &str,
    ) -> Result<bool> {
        let Some(roles) = sc.and_then(|s| s.roles.as_ref()) else {
            return Ok(true);
        };
        let sc = sc.unwrap();
        let role = if act == "approve" {
            &roles.accountable
        } else {
            &roles.responsible
        };
        if role.is_empty() {
            return Ok(true);
        }
        self.scope()
            .may_step(
                ctx,
                &self.r#ref(p),
                who,
                &StepRef {
                    path: sc.path.clone(),
                    responsible: roles.responsible.clone(),
                    accountable: roles.accountable.clone(),
                },
                act,
            )
            .await
    }
}

/// StepContext is the step of a process an action or a human task carries out (ADR 0034, ADR 0035 §2): what it is
/// for and how to go about it, for the person doing it or the agent.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct StepContext {
    pub process: String,
    pub path: String,
    /// Roles assign the step (ADR 0035 §2): the responsible role performs its tasks, the accountable role may
    /// approve its gates.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub roles: Option<Responsibilities>,
    /// Method is the method chosen to carry the step out, whose guidance and references are included.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub method: String,
    /// Item is the element a stream of a step with foreach is carried out for, and ItemKey its identity (ADR 0050).
    #[serde(skip_serializing_if = "Value::is_null")]
    pub item: Value,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub item_key: String,
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub guidance: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub checklist: Vec<String>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub deliverables: Vec<String>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub references: Vec<Reference>,
}

impl StepContext {
    /// with_method adds what the chosen method says to the step's context: the method is the documentary reference
    /// of how the step is carried out here.
    pub fn with_method(s: Option<&StepContext>, me: &Method) -> StepContext {
        let mut out = match s {
            Some(s) => s.clone(),
            None => StepContext::default(),
        };
        out.method = me.name.clone();
        if !me.guidance.is_empty() {
            out.guidance = format!("{}\n\n{}", out.guidance.trim(), me.guidance)
                .trim()
                .to_string();
        }
        out.checklist.extend(me.checklist.iter().cloned());
        out.deliverables.extend(me.deliverables.iter().cloned());
        out.references.extend(me.references.iter().cloned());
        if me.roles.is_some() {
            out.roles = me.roles.clone(); // the method is the more precise reference
        }
        out
    }

    /// section is the step as the system prompt of an LLM action tells it ("" when there is none).
    pub fn section(s: Option<&StepContext>) -> String {
        let Some(s) = s else {
            return String::new();
        };
        let mut b = format!(
            "\n\nYou are carrying out the step {:?} of the process {:?}",
            s.path, s.process
        );
        if !s.method.is_empty() {
            b += &format!(" with the method {:?}", s.method);
        }
        if !s.description.is_empty() {
            b += &format!(": {}", s.description);
        }
        b += ".\n";
        if let Some(r) = s.roles.as_ref().filter(|r| !r.responsible.is_empty()) {
            b += &format!("You act as the role {:?} for this step.\n", r.responsible);
        }
        // ADR 0050: an agent instance is created for the step, it has no memory of the earlier ones
        b += "You start this step fresh: nothing was said to you before. What matters is on the change; read it (the brief below, goap-change/brief, trace) and record what you produce on the change as items, not only in your answer.\n";
        if !s.guidance.is_empty() {
            b += &format!("Guidance for this step:\n{}\n", s.guidance.trim());
        }
        if !s.deliverables.is_empty() {
            b += &format!(
                "Deliverables of this step: {}.\n",
                s.deliverables.join(", ")
            );
        }
        if !s.references.is_empty() {
            b += "Reference documents of this step:\n";
            for r in &s.references {
                b += &format!("- {r}\n");
            }
        }
        b
    }
}

/// step_context is the step an action of p carries out: the step it was generated for, else the step p itself
/// carries out for its parent (None: none).
pub(crate) fn step_context(m: &Compiled, p: &Process, a: &Action) -> Option<StepContext> {
    if a.step.is_empty() {
        return p.step.clone();
    }
    match m.step_by_path(&a.step) {
        Some(s) => Some(context_of_step(&s)),
        None => p.step.clone(),
    }
}

/// context_of_step is the context a run gets for a compiled step.
pub(crate) fn context_of_step(s: &StepInfo) -> StepContext {
    StepContext {
        process: s.owner.clone(),
        path: s.path.clone(),
        name: s.step.name.clone(),
        description: s.step.description.clone(),
        guidance: s.step.guidance.clone(),
        checklist: s.step.checklist.clone(),
        deliverables: s.step.deliverables.clone(),
        references: s.step.references.clone(),
        roles: s.effective.clone(),
        ..Default::default()
    }
}

/// CallExecutor runs the actions of kind call (run_call).
pub(crate) struct CallExecutor;

#[async_trait]
impl Executor for CallExecutor {
    async fn execute(&self, ctx: &Ctx, ac: ActionContext) -> crate::executors::ExecResult {
        run_call(ctx, &ac)
            .await
            .map_err(crate::executors::ExecError::from)
    }
}

/// brief_section is the compact brief of the change an LLM action works on (ADR 0036 §2): the most information in
/// the fewest tokens, one line per fact.
pub(crate) fn brief_section(ac: &ActionContext) -> String {
    if ac.blackboard.change.id.is_empty() {
        return String::new();
    }
    let st = ac.step.as_ref().map(|s| {
        let mut st = goap_brief::Step {
            process: s.process.clone(),
            path: s.path.clone(),
            method: s.method.clone(),
            ..Default::default()
        };
        if let Some(r) = &s.roles {
            st.responsible = r.responsible.clone();
            st.accountable = r.accountable.clone();
        }
        st
    });
    format!(
        "\n\nThe change you work on, in brief (goap-change/trace follows an item, a risk or a node through it):\n{}",
        goap_brief::of(&ac.blackboard, st.as_ref())
    )
}
