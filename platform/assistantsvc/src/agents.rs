//! Running agents (ADR 0090, `agents.go`). The assistant never starts one by itself: start_agent records a proposal as
//! an action of its message, and only the person's ConfirmAction starts it, as them.

use chrono::SecondsFormat;
use goap_access::{is_personal, is_personal_to};
use goap_authz::{Principal, principal_from, with_principal};
use goap_convsvc as convsvc;
use goap_ctx::Ctx;
use goap_domain::{CHANGE_ACTIVE, CHANGE_DRAFT, Change, ChangeID, ChangeStatus};
use goap_graphapi::NewChange;
use serde::Serialize;
use serde_json::{Map, Value, json};

use crate::engine::{Point, Points, StartProcess};
use crate::relate::{ACTION_ATTACH_CHANGE, ACTION_DETACH_CHANGE, describe_relate, is_proposal};
use crate::searchtool::ACTION_SEARCH_CHANGES;
use crate::service::{Error, MAX_ERROR_TEXT, PRINCIPAL, Service};
use crate::turn::{Turn, action};
use crate::uitools::{
    ACTION_UI_TOOL, PREFIX_UI, STATUS_ACCEPTED, STATUS_DONE, STATUS_REQUESTED, short_args,
};
use crate::util::{Args, clip, go_v, map_of, q, sv};

/// The tool listing the agents the caller may run here.
pub const TOOL_LIST_AGENTS: &str = "list_agents";
/// The tool proposing to start one.
pub const TOOL_START_AGENT: &str = "start_agent";

/// The action type of a proposal to start an agent.
pub const ACTION_START_AGENT: &str = "start_agent";

/// StatusProposed waits for the person; StatusStarting is the claim of an accept in progress; the others are final.
pub const STATUS_PROPOSED: &str = "proposed";
pub const STATUS_STARTING: &str = "starting";
pub const STATUS_STARTED: &str = "started";
pub const STATUS_REJECTED: &str = "rejected";
pub const STATUS_FAILED: &str = "failed";

pub const DECISION_ACCEPT: &str = "accept";
pub const DECISION_REJECT: &str = "reject";

const MAX_AGENTS: usize = 30;
const MAX_AGENT_GOALS: usize = 5;
const MAX_RUNNING_SHOWN: usize = 10;
const MAX_STEPS: usize = 12;

/// agentEntry is an agent the caller may run here.
#[derive(Debug, Clone, Default, Serialize)]
pub(crate) struct AgentEntry {
    pub(crate) methodology: String,
    pub(crate) agent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub(crate) description: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub(crate) examples: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub(crate) goals: Vec<GoalEntry>,
    #[serde(rename = "requiredRoles", skip_serializing_if = "Vec::is_empty")]
    pub(crate) roles: Vec<String>,
    #[serde(rename = "needsChange")]
    pub(crate) needs_change: bool,
    pub(crate) running: bool,
    /// with a change in context, a step this agent would carry out is possible now (see possibleSteps)
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub(crate) ready: bool,
    /// every goal name of the agent, whatever the cap of goals
    #[serde(skip)]
    pub(crate) all_goals: Vec<String>,
}

#[derive(Debug, Clone, Default, Serialize)]
pub(crate) struct GoalEntry {
    pub(crate) name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub(crate) description: String,
}

/// changeView is the change of the context, as the caller reads it.
#[derive(Debug, Clone, Default, Serialize)]
pub(crate) struct ChangeView {
    pub(crate) id: String,
    pub(crate) title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub(crate) methodology: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub(crate) namespace: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub(crate) project: String,
    pub(crate) status: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub(crate) lifecycle: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub(crate) state: String,
    #[serde(rename = "runningProcesses", skip_serializing_if = "Vec::is_empty")]
    pub(crate) processes: Vec<ProcessView>,
}

#[derive(Debug, Clone, Default, Serialize)]
pub(crate) struct ProcessView {
    pub(crate) id: String,
    pub(crate) agent: String,
    pub(crate) status: String,
}

/// candidates is what the caller may run here: the agents after the three filters (the change of the context, or its
/// absence; the methodologies applicable to the project; the roles of the caller on it).
#[derive(Debug, Clone, Default)]
pub(crate) struct Candidates {
    pub(crate) project: String,
    pub(crate) change: Option<ChangeView>,
    pub(crate) note: String,
    pub(crate) agents: Vec<AgentEntry>,
    /// the steps possible now on the change of the context (ADR 0097), None with no change
    pub(crate) points: Option<Points>,
}

/// stepEntry is a possible step as list_agents shows it: the only things the assistant may propose on a change.
#[derive(Debug, Clone, Default, Serialize)]
pub(crate) struct StepEntry {
    id: String,
    kind: String,
    name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    description: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    method: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    why: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    produces: Vec<String>,
    #[serde(rename = "responsibleRole", skip_serializing_if = "String::is_empty")]
    responsible: String,
    #[serde(rename = "mayRun")]
    may_run: bool,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    running: bool,
}

fn step_entries(p: &Points) -> Vec<StepEntry> {
    p.points
        .iter()
        .take(MAX_STEPS)
        .map(|pt| StepEntry {
            id: pt.id.clone(),
            kind: pt.kind.clone(),
            name: pt.name.clone(),
            description: clip(&pt.description, 200),
            method: pt.method.clone(),
            why: pt.why.clone(),
            produces: pt.produces.clone(),
            responsible: pt.responsible.clone(),
            may_run: pt.may_run,
            running: pt.running,
        })
        .collect()
}

impl Candidates {
    pub(crate) fn find(&self, methodology: &str, agent: &str) -> Option<AgentEntry> {
        self.agents
            .iter()
            .find(|a| a.methodology == methodology && a.agent == agent)
            .cloned()
    }
}

/// startable reports whether a change in that status can still take a process.
pub(crate) fn startable(s: &ChangeStatus) -> bool {
    *s == CHANGE_DRAFT || *s == CHANGE_ACTIVE
}

impl Service {
    /// readChange reads a change as the caller; one that does not exist or is a personal change of someone else is
    /// the same error.
    pub(crate) async fn read_change(
        &self,
        ctx: &Ctx,
        p: &Principal,
        id: &str,
    ) -> Result<Change, String> {
        match self
            .graph
            .change(&with_principal(ctx, p.clone()), &ChangeID::from(id))
            .await
        {
            Ok(ch) if !(is_personal(&ch) && !is_personal_to(&ch, &p.subject)) => Ok(ch),
            _ => Err(format!(
                "change {} does not exist or is not visible to you",
                q(id)
            )),
        }
    }

    /// runnable computes the agents the caller p may run on the active project, in the context of a change ("" for
    /// none).
    pub(crate) async fn runnable(
        &self,
        ctx: &Ctx,
        p: &Principal,
        project: &str,
        change_id: &str,
    ) -> Result<Candidates, String> {
        let Some(engine) = &self.engine else {
            return Err("agents cannot be run from here: the engine is not reachable".into());
        };
        let pctx = with_principal(ctx, p.clone());
        let mut c = Candidates {
            project: project.into(),
            ..Default::default()
        };
        let names: Vec<String>;
        if !change_id.is_empty() {
            let ch = self.read_change(ctx, p, change_id).await?;
            if !ch.project_id.is_empty() {
                c.project = ch.project_id.clone();
            }
            let mut cv = ChangeView {
                id: ch.id.as_str().to_string(),
                title: clip(&ch.title, 200),
                methodology: ch.methodology.clone(),
                namespace: ch.namespace.clone(),
                project: ch.project_id.clone(),
                status: ch.status.as_str().to_string(),
                lifecycle: ch.lifecycle.clone(),
                state: ch.state.clone(),
                processes: Vec::new(),
            };
            let active = engine
                .active(&pctx)
                .await
                .map_err(|e| format!("the processes cannot be read: {e}"))?;
            for pr in active {
                if pr.change_id == change_id && cv.processes.len() < MAX_RUNNING_SHOWN {
                    cv.processes.push(ProcessView {
                        id: pr.id,
                        agent: pr.agent,
                        status: pr.status,
                    });
                }
            }
            c.change = Some(cv);
            if startable(&ch.status) && !ch.methodology.is_empty() {
                let pts = engine
                    .starting_points(&pctx, change_id)
                    .await
                    .map_err(|e| format!("the possible steps cannot be read: {e}"))?;
                c.points = Some(pts);
            }
            if !startable(&ch.status) {
                c.note = format!(
                    "the change is {}: no agent can work on it any more",
                    ch.status.as_str()
                );
                return Ok(c);
            }
            if ch.methodology.is_empty() {
                c.note = "the change has no methodology, so it names no agent".into();
                return Ok(c);
            }
            names = vec![ch.methodology.clone()];
        } else {
            if !c.project.is_empty()
                && !matches!(
                    self.projects.may_access_project(ctx, p, &c.project).await,
                    Ok(true)
                )
            {
                return Err(format!("you may not work on project {}", q(&c.project)));
            }
            names = self
                .projects
                .applicable_methodologies(ctx, &c.project)
                .await
                .map_err(|e| format!("the organisation cannot be read: {e}"))?;
        }
        let applicable = self
            .projects
            .applicable_methodologies(ctx, &c.project)
            .await
            .map_err(|e| format!("the organisation cannot be read: {e}"))?;
        for n in &names {
            if !applicable.contains(n) {
                c.note = format!(
                    "methodology {} does not apply to project {}",
                    q(n),
                    q(&c.project)
                );
                continue;
            }
            let Ok(m) = self.methodologies.methodology(ctx, n).await else {
                continue;
            };
            let checks = engine
                .check_agents(&pctx, n, &c.project, &[])
                .await
                .map_err(|e| format!("the roles cannot be checked: {e}"))?;
            if !checks.may_start {
                continue;
            }
            for ag in m.agent_list() {
                if !checks.may_run.get(&ag.name).copied().unwrap_or(false) {
                    continue;
                }
                let mut e = AgentEntry {
                    methodology: n.clone(),
                    agent: ag.name.clone(),
                    description: clip(&ag.description, 300),
                    examples: ag.examples.iter().take(3).cloned().collect(),
                    roles: ag.roles.clone(),
                    needs_change: c.change.is_some(),
                    ..Default::default()
                };
                for g in m.agent_goals(&ag) {
                    e.all_goals.push(g.name.clone());
                    if e.goals.len() < MAX_AGENT_GOALS {
                        e.goals.push(GoalEntry {
                            name: g.name.clone(),
                            description: clip(&g.description, 200),
                        });
                    }
                }
                if let Some(cv) = &c.change {
                    e.running = cv.processes.iter().any(|pv| pv.agent == ag.name);
                }
                if let Some(pts) = &c.points {
                    e.ready = pts
                        .points
                        .iter()
                        .any(|pt| pt.agent == ag.name && pt.possible());
                }
                c.agents.push(e);
            }
        }
        c.agents.truncate(MAX_AGENTS);
        Ok(c)
    }
}

impl Turn {
    pub(crate) async fn list_agents(&mut self, ctx: &Ctx) -> Result<Value, String> {
        let c = self
            .s
            .runnable(ctx, &self.p, &self.project, &self.in_.context.change())
            .await?;
        let mut res = Map::new();
        res.insert("project".into(), json!(c.project));
        res.insert("agents".into(), json!(c.agents));
        if let Some(cv) = &c.change {
            res.insert("change".into(), json!(cv));
        }
        if let Some(pts) = &c.points {
            // the steps possible now towards the goal of the change, in the same call (ADR 0097): the only ones to
            // propose
            res.insert("goal".into(), json!(pts.goal));
            res.insert("possibleSteps".into(), json!(step_entries(pts)));
            if pts.blocked_count > 0 {
                let first = if pts.blocked.is_empty() {
                    Value::Null
                } else {
                    json!(pts.blocked)
                };
                res.insert(
                    "waitingSteps".into(),
                    json!({"count": pts.blocked_count, "firstMissing": first}),
                );
            }
            if pts.points.is_empty() {
                res.insert(
                    "stepsNote".into(),
                    json!(format!(
                        "no step is possible now: {}. Say what is awaited; propose nothing.",
                        pts.reason
                    )),
                );
            }
        }
        if !c.note.is_empty() {
            res.insert("note".into(), json!(c.note));
        }
        if c.agents.is_empty() && c.note.is_empty() && c.points.is_none() {
            res.insert(
                "note".into(),
                json!("no agent of the methodologies of this project is one you may run here"),
            );
        }
        Ok(Value::Object(res))
    }

    /// startAgent validates and records a proposal; it starts nothing, creates nothing and calls no engine.
    pub(crate) async fn start_agent(&mut self, ctx: &Ctx, a: &Args) -> Result<Value, String> {
        if self
            .actions
            .iter()
            .any(|prev| sv(prev, "type") == ACTION_START_AGENT)
        {
            return Err(
                "one proposal per answer: the person decides the first before another is made"
                    .into(),
            );
        }
        let (mut method, mut agent, mut goal, intent) = (
            a.str("methodology"),
            a.str("agent"),
            a.str("goal"),
            a.str("intent"),
        );
        let step = a.str("step");
        let ctx_change = self.in_.context.change();
        if step.is_empty() && ctx_change.is_empty() && (method.is_empty() || agent.is_empty()) {
            return Err(
                r#""methodology" and "agent" are required (take them from list_agents)"#.into(),
            );
        }
        if intent.len() > 4000 {
            return Err("the intent is too long (4000 bytes)".into());
        }
        let mut change_id = a.str("changeId");
        let nc = match a.0.get("newChange") {
            Some(Value::Object(m)) => Some(Args(m.clone())),
            _ => None,
        };
        if !change_id.is_empty() && nc.is_some() {
            return Err(r#"give "changeId" or "newChange", not both"#.into());
        } else if !ctx_change.is_empty() && nc.is_some() {
            return Err(
                "the person is looking at a change: propose the agent on it (no newChange)".into(),
            );
        } else if !ctx_change.is_empty() && change_id.is_empty() {
            change_id = ctx_change.clone();
        } else if !ctx_change.is_empty() && change_id != ctx_change {
            return Err(format!(
                "the only change agents are listed for is {}, the one the person is looking at",
                q(&ctx_change)
            ));
        } else if ctx_change.is_empty() && !change_id.is_empty() {
            return Err(
                "no change is open: propose the agent with a newChange {title, intent}".into(),
            );
        } else if ctx_change.is_empty() && nc.is_none() {
            return Err(r#"no change is open: "newChange" {title, intent} is required"#.into());
        }
        if nc.is_some() && !self.searched {
            // a new change is proposed only once the open changes were searched (frame.rs)
            return Err("a new change is proposed only once the open changes were searched: call search_changes on the intent first and show the person what was found".into());
        }
        let mut new_change: Option<Value> = None;
        if let Some(nc) = &nc {
            let (title, why) = (nc.str("title"), nc.str("intent"));
            if title.is_empty() || why.is_empty() {
                return Err(r#""newChange" needs a "title" and an "intent""#.into());
            }
            if title.len() > 200 || why.len() > 4000 {
                return Err("the title (200 bytes) or the intent (4000 bytes) of the new change is too long".into());
            }
            new_change = Some(json!({"title": title, "intent": why}));
        }
        let c = self
            .s
            .runnable(ctx, &self.p, &self.project, &ctx_change)
            .await?;
        if c.change.is_some() && step.is_empty() {
            // on a change the only proposals are the steps possible now (ADR 0097)
            let Some(pts) = &c.points else {
                return Err(format!("no step can be started: {}", c.note));
            };
            return Err(not_possible("", pts));
        }
        let mut entry = AgentEntry::default();
        let mut pt = Point::default();
        if !step.is_empty() {
            // on a change only the steps possible now are proposed, with the launch the engine gives them
            let Some(pts) = &c.points else {
                return Err(format!("no step can be started: {}", c.note));
            };
            let Some(found) = pts.find(&step) else {
                return Err(not_possible(&step, pts));
            };
            pt = found;
            if pt.running {
                return Err(format!(
                    "step {} is already being carried out on this change",
                    q(&step)
                ));
            }
            if !pt.may_run {
                return Err(format!(
                    "you may not start step {}: it needs the role {} on this project",
                    q(&step),
                    q(&pt.responsible)
                ));
            }
            method = c
                .change
                .as_ref()
                .map(|cv| cv.methodology.clone())
                .unwrap_or_default();
            agent = pt.agent.clone();
            goal = pt.agent_goal.clone();
        } else {
            match c.find(&method, &agent) {
                Some(e) => entry = e,
                None => {
                    return Err(format!(
                        "agent {} of {} is not one you may run here (see list_agents)",
                        q(&agent),
                        q(&method)
                    ));
                }
            }
        }
        if step.is_empty() && !goal.is_empty() && !entry.all_goals.contains(&goal) {
            return Err(format!(
                "agent {} has no goal {} (its goals: {})",
                q(&agent),
                q(&goal),
                entry.all_goals.join(", ")
            ));
        }
        if step.is_empty() && goal.is_empty() && intent.is_empty() {
            if entry.all_goals.len() != 1 {
                return Err(r#"give a "goal" (one of the agent's) or an "intent" the engine can identify the goal from"#.into());
            }
            goal = entry.all_goals[0].clone();
        }
        let mut label = format!("Start agent {agent} ({method})");
        if !step.is_empty() {
            label = format!("Start step {} ({step})", pt.name);
        }
        if new_change.is_some() {
            label.push_str(" on a new change");
        } else {
            label.push_str(" on ");
            label.push_str(&change_id);
        }
        let rationale = a.str("rationale");
        let mut params = Map::new();
        params.insert("methodology".into(), json!(method));
        params.insert("agent".into(), json!(agent));
        params.insert("project".into(), json!(c.project));
        for (k, v) in [
            ("goal", &goal),
            ("intent", &intent),
            ("changeId", &change_id),
        ] {
            if !v.is_empty() {
                params.insert(k.into(), json!(v));
            }
        }
        if let Some(nc) = new_change {
            params.insert("newChange".into(), nc);
        }
        if !step.is_empty() {
            params.insert("step".into(), json!(step));
        }
        let mut act = action(
            json!({"type": ACTION_START_AGENT, "status": STATUS_PROPOSED,
            "label": clip(&label, 200), "args": Value::Object(params)}),
        );
        if !rationale.is_empty() {
            act.insert("rationale".into(), json!(clip(&rationale, 500)));
        }
        self.actions.push(act);
        Ok(
            json!({"status": STATUS_PROPOSED, "note": "proposed, awaiting the person's confirmation in the interface: nothing was started. Say what you propose and why, and do not say it started."}),
        )
    }
}

/// notPossible is the refusal of a step that is not possible now: the reason, and what is.
pub(crate) fn not_possible(step: &str, p: &Points) -> String {
    let ids: Vec<&str> = p
        .points
        .iter()
        .filter(|pt| pt.possible())
        .map(|pt| pt.id.as_str())
        .collect();
    let step = if step.is_empty() {
        "(none given)"
    } else {
        step
    };
    let mut why = "its entry conditions do not all hold, or it does not work towards the goal of the change, or it is done";
    if p.points.is_empty() && !p.reason.is_empty() {
        why = &p.reason;
    }
    if ids.is_empty() {
        return format!(
            "step {} is not possible now on this change ({why}); no step is possible now: say what is awaited",
            q(step)
        );
    }
    format!(
        "step {} is not possible now on this change ({why}); on a change propose a step of possibleSteps (\"step\": its id): {}",
        q(step),
        ids.join(", ")
    )
}

/// ConfirmInput is the decision of the person on a proposal.
#[derive(Debug, Clone, Default)]
pub struct ConfirmInput {
    pub conversation_id: String,
    pub message_id: String,
    pub action_index: i64,
    /// accept or reject.
    pub decision: String,
    /// The active project of the interface (the token's when empty).
    pub project: String,
}

/// decide's mutation of the action once moved.
pub(crate) type ActionEdit = Option<Box<dyn FnOnce(&mut convsvc::Action) + Send>>;

/// The compare-and-set that makes a decision final: it moves the action from the status it must be in to another.
pub(crate) struct Decider<'a> {
    pub(crate) s: &'a Service,
    /// the platform's context: the decision is written by it, whatever becomes of the caller's
    pub(crate) sys: Ctx,
    pub(crate) by: String,
    pub(crate) decision: String,
    pub(crate) message_id: String,
    pub(crate) index: i64,
}

impl Decider<'_> {
    pub(crate) async fn decide(
        &self,
        from: &str,
        to: &str,
        edit: ActionEdit,
    ) -> Result<convsvc::Message, Error> {
        let (from, to) = (from.to_string(), to.to_string());
        let decided = json!({"by": self.by,
            "at": self.s.now().to_rfc3339_opts(SecondsFormat::Secs, true),
            "decision": self.decision});
        let r = self
            .s
            .convs
            .update_action(
                &self.sys,
                &PRINCIPAL,
                &self.message_id,
                self.index,
                Box::new(move |mut a: convsvc::Action| {
                    if sv(&a, "status") != from {
                        return Err(convsvc::Error::other(Error::Decided));
                    }
                    a.insert("status".into(), json!(to));
                    a.insert("decided".into(), decided);
                    if let Some(f) = edit {
                        f(&mut a);
                    }
                    Ok(a)
                }),
            )
            .await;
        r.map_err(Error::from)
    }
}

impl Service {
    /// Confirm decides a proposal of the assistant as its conversation's owner (ADR 0090). Reject records it. Accept
    /// re-validates everything now, as the caller, then claims the action (a second decision is Decided), creates the
    /// change when the proposal names a new one, starts the process through the engine as the caller and records the
    /// outcome in the action: started, with its process and change, or failed, with the error. A proposal that no
    /// longer holds is refused with Stale and stays proposed.
    pub async fn confirm(&self, ctx: &Ctx, in_: ConfirmInput) -> Result<convsvc::Message, Error> {
        let mut p = principal_from(ctx);
        if p.anonymous() {
            return Err(convsvc::Error::Anonymous.into());
        }
        if p.is_system() {
            return Err(convsvc::Error::Forbidden.into());
        }
        if in_.decision != DECISION_ACCEPT && in_.decision != DECISION_REJECT {
            return Err(Error::Invalid(format!(
                "the decision is {DECISION_ACCEPT} or {DECISION_REJECT}"
            )));
        }
        if !in_.project.is_empty() && in_.project != p.project {
            match self
                .projects
                .may_access_project(ctx, &p, &in_.project)
                .await
            {
                Ok(true) => {}
                Ok(false) => return Err(Error::Forbidden(String::new())),
                Err(e) => return Err(Error::Forbidden(e.to_string())),
            }
            p.project = in_.project.clone();
        }
        let ctx = with_principal(ctx, p.clone());
        let (_, msgs) = self.convs.get(&ctx, &p, &in_.conversation_id).await?; // the owner only
        let Some(msg) = msgs.iter().find(|m| m.id == in_.message_id) else {
            return Err(convsvc::Error::NotFound.into());
        };
        if msg.role != convsvc::ROLE_ASSISTANT
            || in_.action_index < 0
            || in_.action_index as usize >= msg.actions.len()
            || !is_proposal(sv(&msg.actions[in_.action_index as usize], "type"))
        {
            return Err(Error::Invalid("that is not a proposal".into()));
        }
        let act = msg.actions[in_.action_index as usize].clone();
        if sv(&act, "status") != STATUS_PROPOSED {
            return Err(Error::Decided);
        }
        let d = Decider {
            s: self,
            sys: with_principal(&ctx.without_cancel(), PRINCIPAL.clone()),
            by: p.subject.clone(),
            decision: in_.decision.clone(),
            message_id: in_.message_id.clone(),
            index: in_.action_index,
        };
        if in_.decision == DECISION_REJECT {
            return d.decide(STATUS_PROPOSED, STATUS_REJECTED, None).await;
        }
        let typ = sv(&act, "type");
        if typ == ACTION_UI_TOOL {
            // a screen tool (ADR 0092): the server does nothing but record the decision; the web runs it through the
            // edit path of the screen and reports the outcome. Stale when the project is no longer the proposal's.
            let proj = sv(&act, "project");
            if proj != p.project {
                return Err(Error::Stale(format!(
                    "it was made for project {} and the active project is {}",
                    q(proj),
                    q(&p.project)
                )));
            }
            return d.decide(STATUS_PROPOSED, STATUS_ACCEPTED, None).await;
        }
        if typ == ACTION_ATTACH_CHANGE || typ == ACTION_DETACH_CHANGE {
            return self.confirm_relate(&ctx, &p, &act, &d).await;
        }

        let args_map = map_of(&act, "args");
        let ar = Args(args_map.clone());
        // stale: nothing is claimed, the proposal stays for the person to reject
        let mut change_id = ar.str("changeId");
        let mut project = ar.str("project");
        let nc = Args(map_of(&args_map, "newChange"));
        if change_id.is_empty() && p.project != project {
            return Err(Error::Stale(format!(
                "it was made for project {} and the active project is {}",
                q(&project),
                q(&p.project)
            )));
        }
        if !change_id.is_empty() {
            let ch = self
                .read_change(&ctx, &p, &change_id)
                .await
                .map_err(Error::Stale)?;
            if !startable(&ch.status) {
                return Err(Error::Stale(format!(
                    "change {} is {}",
                    q(&change_id),
                    ch.status.as_str()
                )));
            }
            project = ch.project_id.clone();
        }
        p.project = project.clone();
        let ctx = with_principal(&ctx, p.clone());
        let c = self
            .runnable(&ctx, &p, &project, &change_id)
            .await
            .map_err(Error::Stale)?;
        let step = ar.str("step");
        if !step.is_empty() {
            let Some(pts) = &c.points else {
                return Err(Error::Stale(format!(
                    "no step can be started on change {} any more",
                    q(&change_id)
                )));
            };
            let ok = pts.find(&step).is_some_and(|pt| {
                pt.possible() && pt.agent == ar.str("agent") && pt.agent_goal == ar.str("goal")
            });
            if !ok {
                return Err(Error::Stale(format!(
                    "step {} is no longer possible now on change {}",
                    q(&step),
                    q(&change_id)
                )));
            }
        } else if c.find(&ar.str("methodology"), &ar.str("agent")).is_none() {
            return Err(Error::Stale(format!(
                "agent {} of {} is no longer one you may run here",
                q(&ar.str("agent")),
                q(&ar.str("methodology"))
            )));
        }

        d.decide(STATUS_PROPOSED, STATUS_STARTING, None).await?;
        let mut result = Map::new();
        let (dr, conv_id) = (&d, in_.conversation_id.as_str());
        let fail = |err: String, result: Map<String, Value>| async move {
            let r = dr
                .decide(
                    STATUS_STARTING,
                    STATUS_FAILED,
                    Some(Box::new(move |a: &mut convsvc::Action| {
                        a.insert("error".into(), json!(clip(&err, MAX_ERROR_TEXT)));
                        if !result.is_empty() {
                            a.insert("result".into(), Value::Object(result));
                        }
                    })),
                )
                .await;
            if let Err(e) = &r {
                tracing::error!(conversation = %conv_id, err = %e, "assistant proposal outcome not stored");
            }
            r
        };
        if !nc.0.is_empty() {
            let Ok(m) = self
                .methodologies
                .methodology(&ctx, &ar.str("methodology"))
                .await
            else {
                return fail(
                    format!("methodology {} cannot be read", q(&ar.str("methodology"))),
                    result,
                )
                .await;
            };
            // no active project means the root project (ADR 0091)
            let mut change_project = project.clone();
            if change_project.is_empty() {
                match self.projects.root_project(&ctx).await {
                    Ok(r) => change_project = r,
                    Err(e) => return fail(e.to_string(), result).await,
                }
            }
            let data = json!({"createdBy": p.subject, "via": "assistant", "conversation": in_.conversation_id});
            let ch = match self
                .graph
                .create_change(
                    &ctx,
                    NewChange {
                        title: nc.str("title"),
                        intent: nc.str("intent"),
                        methodology: m.definition().name.clone(),
                        namespace: m.definition().namespace.clone(),
                        project_id: change_project,
                        data: data.as_object().cloned().unwrap_or_default(),
                        ..Default::default()
                    },
                )
                .await
            {
                Ok(ch) => ch,
                Err(e) => return fail(format!("the change was not created: {e}"), result).await,
            };
            change_id = ch.id.as_str().to_string();
            result.insert("changeId".into(), json!(change_id));
        }
        let Some(engine) = &self.engine else {
            return fail(
                "agents cannot be run from here: the engine is not reachable".into(),
                result,
            )
            .await;
        };
        let mut vars = Map::new();
        vars.insert(
            goap_engine::VAR_CONVERSATION.into(),
            json!(in_.conversation_id),
        );
        vars.insert("messageId".into(), json!(in_.message_id));
        let pr = match engine
            .start_process(
                &ctx,
                StartProcess {
                    methodology: ar.str("methodology"),
                    agent: ar.str("agent"),
                    goal: ar.str("goal"),
                    intent: ar.str("intent"),
                    change_id: change_id.clone(),
                    project_id: project.clone(),
                    vars,
                    ..Default::default()
                },
            )
            .await
        {
            Ok(pr) => pr,
            Err(e) => return fail(format!("the agent was not started: {e}"), result).await,
        };
        result.insert("processId".into(), json!(pr.id));
        if !pr.change_id.is_empty() {
            result.insert("changeId".into(), json!(pr.change_id));
        }
        let r = d
            .decide(
                STATUS_STARTING,
                STATUS_STARTED,
                Some(Box::new(move |a: &mut convsvc::Action| {
                    a.insert("result".into(), Value::Object(result));
                })),
            )
            .await;
        if let Err(e) = &r {
            tracing::error!(conversation = %in_.conversation_id, process = %pr.id, err = %e, "assistant proposal outcome not stored");
        }
        r
    }
}

/// describeActions is what the history says of the actions of an assistant message: the proposals with the decision
/// of the person (so the assistant can follow up), the screen tools with what became of them, the searches of the
/// changes, then the types of the other actions.
pub(crate) fn describe_actions(actions: &[convsvc::Action]) -> String {
    let mut other: Vec<String> = Vec::new();
    let mut notes: Vec<String> = Vec::new();
    for a in actions {
        let t = sv(a, "type");
        if t == ACTION_UI_TOOL {
            notes.push(describe_ui_tool(a));
            continue;
        }
        if t == ACTION_ATTACH_CHANGE || t == ACTION_DETACH_CHANGE {
            notes.push(describe_relate(a));
            continue;
        }
        if t == ACTION_SEARCH_CHANGES {
            notes.push(format!(
                "searched the open changes for {}: {} found",
                q(&Args(map_of(a, "args")).str("q")),
                go_v(map_of(a, "result").get("count"))
            ));
            continue;
        }
        if t != ACTION_START_AGENT {
            if !t.is_empty() {
                other.push(t.to_string());
            }
            continue;
        }
        let ar = Args(map_of(a, "args"));
        let mut what = format!("agent {} of {}", ar.str("agent"), ar.str("methodology"));
        let st = ar.str("step");
        if !st.is_empty() {
            what = format!("step {st} of {}", ar.str("methodology"));
        }
        let res = map_of(a, "result");
        match sv(a, "status") {
            STATUS_PROPOSED | STATUS_STARTING => notes.push(format!(
                "proposal to start {what}: the person has not decided yet, nothing was started"
            )),
            STATUS_STARTED => notes.push(format!(
                "the person accepted the proposal to start {what}: it was started (process {} on change {})",
                go_v(res.get("processId")),
                go_v(res.get("changeId"))
            )),
            STATUS_REJECTED => notes.push(format!(
                "the person rejected the proposal to start {what}: nothing was started"
            )),
            STATUS_FAILED => notes.push(format!(
                "the person accepted the proposal to start {what} but it failed: {}",
                go_v(a.get("error"))
            )),
            _ => {}
        }
    }
    let mut out = Vec::new();
    if !other.is_empty() {
        out.push(format!(
            "(actions run for the person: {})",
            other.join(", ")
        ));
    }
    for n in notes {
        out.push(format!("({n})"));
    }
    out.join("\n")
}

/// describeUITool is the history line of a screen tool action: the tool with its arguments, and the decision and
/// outcome known so far.
pub(crate) fn describe_ui_tool(a: &convsvc::Action) -> String {
    let mut what = format!("{PREFIX_UI}{}", go_v(a.get("tool")));
    let s = short_args(&map_of(a, "args"));
    if !s.is_empty() {
        what.push(' ');
        what.push_str(&s);
    }
    match sv(a, "status") {
        STATUS_REQUESTED => format!("the interface was asked to run {what}: no outcome is known"),
        STATUS_PROPOSED => {
            format!("proposal {what}: the person has not decided yet, nothing was changed")
        }
        STATUS_ACCEPTED => format!(
            "the person accepted the proposal {what}: the interface is applying it, no outcome reported yet"
        ),
        STATUS_REJECTED => {
            format!("the person rejected the proposal {what}: nothing was changed")
        }
        STATUS_DONE => format!("{what} was done in the interface"),
        STATUS_FAILED => format!("{what} failed in the interface: {}", go_v(a.get("error"))),
        _ => what,
    }
}
