//! agents_test.go

use std::sync::atomic::Ordering;

use goap_authz::{Principal, with_principal};
use goap_connect::Code;
use goap_convsvc as convsvc;
use goap_ctx::Ctx;
use goap_domain::CHANGE_APPLIED;
use goap_llm as llm;
use serde_json::Value;

use super::*;
use crate::Error;
use crate::agents::{
    ACTION_START_AGENT, ConfirmInput, DECISION_ACCEPT, DECISION_REJECT, STATUS_FAILED,
    STATUS_PROPOSED, STATUS_REJECTED, STATUS_STARTED, TOOL_LIST_AGENTS, TOOL_START_AGENT,
};
use crate::frame::{Frame, Level};
use crate::handler::rpc_err;
use crate::prompt::system_prompt;
use crate::service::history_of;
use crate::toolspecs::SERVER_TOOLS;
use crate::turn::tools;

fn agent_names(result: &str) -> Vec<String> {
    let rs = outcomes(result);
    rs[0]["result"]["agents"]
        .as_array()
        .cloned()
        .unwrap_or_default()
        .iter()
        .map(|a| {
            format!(
                "{}/{}",
                a["methodology"].as_str().unwrap_or(""),
                a["agent"].as_str().unwrap_or("")
            )
        })
        .collect()
}

#[tokio::test]
async fn list_agents_filters_by_project_and_roles() {
    let e = new_env(&[&call(TOOL_LIST_AGENTS, "{}"), r#"{"message":"ok"}"#]).await;
    e.send("what can I run?", in_project("PROJ-A"))
        .await
        .unwrap();
    let res = e.tool_result(1);
    // u1 is a developer: not the release manager's agent, and nothing of a methodology of another project
    assert_eq!(
        agent_names(&res),
        ["delivery/builder", "delivery/helper"],
        "{res}"
    );
    assert!(
        res.contains(r#""requiredRoles":["developer"]"#)
            && res.contains("Builds the software")
            && res.contains("build it"),
        "entry {res}"
    );
    // another project: the methodologies of that one
    let mut e = new_env(&[&call(TOOL_LIST_AGENTS, "{}"), r#"{"message":"ok"}"#]).await;
    let mut u = e.user.clone();
    u.project = "PROJ-B".into();
    e.set_user(u);
    e.send("and here?", in_project("PROJ-B")).await.unwrap();
    assert_eq!(agent_names(&e.tool_result(1)), ["other/stranger"]);
    // a release manager sees the shipper too
    let e = new_env(&[&call(TOOL_LIST_AGENTS, "{}"), r#"{"message":"ok"}"#]).await;
    e.engine
        .state
        .lock()
        .held
        .insert(sv("u1"), vec![sv("release_manager")]);
    e.send("what can I run?", in_project("PROJ-A"))
        .await
        .unwrap();
    assert_eq!(
        agent_names(&e.tool_result(1)),
        ["delivery/shipper", "delivery/helper"]
    );
}

#[tokio::test]
async fn list_agents_of_the_change_in_context() {
    let e = new_env(&[&call(TOOL_LIST_AGENTS, "{}"), r#"{"message":"ok"}"#]).await;
    e.engine.state.lock().active = vec![
        ProcessInfo {
            id: "PRC-9".into(),
            agent: "builder".into(),
            change_id: "CHG-D".into(),
            status: "running".into(),
            ..Default::default()
        },
        ProcessInfo {
            id: "PRC-8".into(),
            agent: "helper".into(),
            change_id: "CHG-OTHER".into(),
            status: "running".into(),
            ..Default::default()
        },
    ];
    e.send("what now?", change_ctx("CHG-D")).await.unwrap();
    let res = e.tool_result(1);
    assert_eq!(agent_names(&res), ["delivery/builder", "delivery/helper"]);
    let r = outcomes(&res)[0]["result"].clone();
    let ch = &r["change"];
    assert!(
        ch["id"] == "CHG-D"
            && ch["methodology"] == "delivery"
            && ch["status"] == "active"
            && ch["state"] == "design"
            && ch["runningProcesses"].as_array().map(Vec::len) == Some(1)
            && ch["runningProcesses"][0]["id"] == "PRC-9",
        "change {ch}"
    );
    // only the agent with a process of its own on this change is flagged
    let ag = &r["agents"];
    assert!(
        ag[0]["running"] == true && ag[1]["running"] == false && ag[0]["needsChange"] == true,
        "agents {ag}"
    );
    // a change that cannot take a process any more lists nothing, and says why
    let e = new_env(&[&call(TOOL_LIST_AGENTS, "{}"), r#"{"message":"ok"}"#]).await;
    e.send("what now?", change_ctx("CHG-DONE")).await.unwrap();
    let res = e.tool_result(1);
    assert!(
        agent_names(&res).is_empty() && res.contains("applied"),
        "{res}"
    );
    // a change of someone else is not visible
    let e = new_env(&[&call(TOOL_LIST_AGENTS, "{}"), r#"{"message":"ok"}"#]).await;
    e.send("what now?", change_ctx("CHG-HERS")).await.unwrap();
    let res = e.tool_result(1);
    assert!(res.contains("not visible"), "{res}");
}

pub(crate) fn step_args(id: &str) -> String {
    format!(r#"{{"step":"{id}","rationale":"it is possible"}}"#)
}

pub(crate) fn propose_args(extra: &str) -> String {
    format!(
        r#"{{"methodology":"delivery","agent":"builder","goal":"built","rationale":"you asked for a build"{extra}}}"#
    )
}

pub(crate) const NEW_CHANGE_ARG: &str =
    r#","newChange":{"title":"Build v2","intent":"a build of v2"}"#;

#[tokio::test]
async fn start_agent_only_records_a_proposal() {
    let e = new_env(&[
        &call(
            TOOL_START_AGENT,
            &propose_args(r#","newChange":{"title":"Build v2","intent":"a build of v2"}"#),
        ),
        r#"{"message":"I propose to start the builder."}"#,
    ])
    .await;
    e.searched().await;
    e.send("build v2", in_project("PROJ-A")).await.unwrap();
    assert!(
        e.engine.starts().is_empty() && e.graph.created().is_empty(),
        "something ran"
    );
    let res = e.tool_result(1);
    assert!(
        res.contains("proposed") && res.contains("nothing was started"),
        "{res}"
    );
    let a = e.answer().await;
    assert!(
        a.status == convsvc::STATUS_DONE && a.actions.len() == 1,
        "{a:?}"
    );
    let act = &a.actions[0];
    let args = obj(act, "args");
    assert!(
        s(act, "type") == ACTION_START_AGENT
            && s(act, "status") == STATUS_PROPOSED
            && s(act, "rationale") == "you asked for a build"
            && s(act, "label").contains("builder")
            && s(&args, "methodology") == "delivery"
            && s(&args, "agent") == "builder"
            && s(&args, "goal") == "built"
            && s(&args, "project") == "PROJ-A"
            && s(&obj(&args, "newChange"), "title") == "Build v2"
            && args.get("changeId").is_none(),
        "action {act:?}"
    );
    for m in &e.model.0.lock().metas {
        assert!(
            m.source == llm::SOURCE_ASSISTANT && m.conversation_id == e.conv.id,
            "meta {m:?}"
        );
    }
}

#[tokio::test]
async fn start_agent_refusals() {
    let cases: Vec<(&str, String, Context, &str)> = vec![
        ("a role the caller lacks", r#"{"methodology":"delivery","agent":"shipper","goal":"ship","newChange":{"title":"t","intent":"i"}}"#.into(), in_project("PROJ-A"), "not one you may run here"),
        ("another project", r#"{"methodology":"other","agent":"stranger","newChange":{"title":"t","intent":"i"}}"#.into(), in_project("PROJ-A"), "not one you may run here"),
        ("an unknown goal", r#"{"methodology":"delivery","agent":"builder","goal":"nope","newChange":{"title":"t","intent":"i"}}"#.into(), in_project("PROJ-A"), "no goal"),
        ("no change and no new", propose_args(""), in_project("PROJ-A"), "newChange"),
        ("a change out of context", propose_args(r#","changeId":"CHG-OTHER""#), change_ctx("CHG-D"), "looking at"),
        ("a new change in a change", propose_args(r#","newChange":{"title":"t","intent":"i"}"#), change_ctx("CHG-D"), "no newChange"),
        ("a closed change", step_args("delivery/build"), change_ctx("CHG-DONE"), "no step can be started"),
        ("an agent on a change", propose_args(""), change_ctx("CHG-D"), "possibleSteps"),
        ("a step that waits", step_args("delivery/design"), change_ctx("CHG-D"), "not possible now"),
        ("an unknown step", step_args("delivery/nope"), change_ctx("CHG-D"), "delivery/build"),
        ("a step already running", step_args("delivery/running"), change_ctx("CHG-D"), "already being carried out"),
        ("a step the caller may not", step_args("delivery/restricted"), change_ctx("CHG-D"), "may not start"),
        ("a goal to choose", r#"{"methodology":"delivery","agent":"helper","goal":"","newChange":{"title":"t","intent":"i"}}"#.into(), in_project("PROJ-A"), ""),
    ];
    for (name, args, c, want) in cases {
        let e = new_env(&[&call(TOOL_START_AGENT, &args), r#"{"message":"no"}"#]).await;
        e.searched().await;
        e.send("run it", c).await.unwrap();
        let res = e.tool_result(1);
        if want.is_empty() {
            // helper has one goal: it is chosen, the proposal stands
            assert_eq!(e.answer().await.actions.len(), 1, "{name}: {res}");
            continue;
        }
        assert!(
            res.contains(r#""error""#) && res.contains(want),
            "{name}: {res}"
        );
        let a = e.answer().await;
        assert!(
            a.actions.is_empty() && e.engine.starts().is_empty(),
            "{name}: {a:?}"
        );
    }
}

#[tokio::test]
async fn start_agent_on_the_change_in_context() {
    let e = new_env(&[
        &call(TOOL_START_AGENT, &step_args("delivery/build")),
        r#"{"message":"proposed"}"#,
    ])
    .await;
    e.send("build", change_ctx("CHG-D")).await.unwrap();
    // a round that only proposes settles: one model round after the proposal, no more
    assert_eq!(e.got().len(), 2, "model rounds");
    let a = e.answer().await;
    let act = &a.actions[0];
    let args = obj(act, "args");
    assert!(
        s(&args, "changeId") == "CHG-D"
            && args.get("newChange").is_none()
            && s(&args, "step") == "delivery/build"
            && s(&args, "agent") == "builder"
            && s(&args, "goal") == "delivery/build"
            && s(&args, "methodology") == "delivery"
            && s(act, "label").contains("step build"),
        "{args:?}"
    );
}

/// With a change in context list_agents gives the steps possible now in the same call, flags the agents that would
/// carry one out and only counts the steps that wait (ADR 0097).
#[tokio::test]
async fn list_agents_gives_the_possible_steps() {
    let e = new_env(&[&call(TOOL_LIST_AGENTS, "{}"), r#"{"message":"ok"}"#]).await;
    e.send("what can be done?", change_ctx("CHG-D"))
        .await
        .unwrap();
    assert_eq!(
        e.engine.point_calls.load(Ordering::SeqCst),
        1,
        "the steps were asked"
    );
    let res = e.tool_result(1);
    let r = outcomes(&res)[0]["result"].clone();
    let steps = r["possibleSteps"].as_array().cloned().unwrap_or_default();
    assert!(
        r["goal"] == "ship"
            && steps.len() == 3
            && steps[0]["id"] == "delivery/build"
            && steps[0]["mayRun"] == true
            && steps[0]["why"][0] == "noted"
            && r["waitingSteps"]["count"].as_f64() == Some(2.0),
        "{r}"
    );
    // builder carries the step out, helper none
    assert!(
        r["agents"][0]["ready"] == true && r["agents"][1].get("ready").is_none(),
        "agents {}",
        r["agents"]
    );
    assert!(
        !res.contains("build_action"),
        "actions are never shown: {res}"
    );
    // none possible: the result says what is awaited
    let e = new_env(&[&call(TOOL_LIST_AGENTS, "{}"), r#"{"message":"ok"}"#]).await;
    e.engine.state.lock().points.insert(
        sv("CHG-D"),
        Points {
            goal: "ship".into(),
            reason: "no step is possible yet: the steps towards the goal wait for conditions"
                .into(),
            blocked_count: 1,
            blocked: vec!["delivery/design: noted".into()],
            ..Default::default()
        },
    );
    e.send("what can be done?", change_ctx("CHG-D"))
        .await
        .unwrap();
    let res = e.tool_result(1);
    assert!(
        res.contains(r#""possibleSteps":[]"#)
            && res.contains("propose nothing")
            && res.contains("delivery/design: noted"),
        "{res}"
    );
}

#[tokio::test]
async fn one_proposal_per_answer() {
    let a = propose_args(r#","newChange":{"title":"t","intent":"i"}"#);
    let two = format!(
        r#"{{"message":"","tool_calls":[{{"name":"start_agent","arguments":{a}}},{{"name":"start_agent","arguments":{a}}}]}}"#
    );
    let e = new_env(&[&two, r#"{"message":"proposed"}"#]).await;
    e.searched().await;
    e.send("build", in_project("PROJ-A")).await.unwrap();
    let n = e.answer().await.actions.len();
    assert!(
        n == 1 && e.tool_result(1).contains("one proposal per answer"),
        "{n} actions, {}",
        e.tool_result(1)
    );
}

impl Env {
    /// Runs a turn that ends with a proposal and returns its message.
    pub(crate) async fn propose(&mut self, extra: &str, c: Context) -> convsvc::Message {
        self.set_answers(&[
            &call(TOOL_START_AGENT, &propose_args(extra)),
            r#"{"message":"I propose it."}"#,
        ]);
        if extra.contains("newChange") && !self.did_search {
            self.searched().await;
            self.did_search = true;
        }
        self.send("build", c).await.unwrap();
        let m = self.answer().await;
        assert_eq!(m.actions.len(), 1, "no proposal: {m:?}");
        m
    }

    /// Runs a turn that ends with a proposal of a step.
    pub(crate) async fn propose_step(&self, id: &str, c: Context) -> convsvc::Message {
        self.set_answers(&[
            &call(TOOL_START_AGENT, &step_args(id)),
            r#"{"message":"I propose it."}"#,
        ]);
        self.send("build", c).await.unwrap();
        let m = self.answer().await;
        assert_eq!(m.actions.len(), 1, "no proposal: {m:?}");
        m
    }

    pub(crate) async fn confirm(
        &self,
        m: &convsvc::Message,
        decision: &str,
    ) -> Result<convsvc::Message, Error> {
        self.confirm_in(m, decision, "PROJ-A").await
    }

    pub(crate) async fn confirm_in(
        &self,
        m: &convsvc::Message,
        decision: &str,
        project: &str,
    ) -> Result<convsvc::Message, Error> {
        self.svc
            .confirm(
                &self.ctx,
                ConfirmInput {
                    conversation_id: self.conv.id.clone(),
                    message_id: m.id.clone(),
                    decision: decision.into(),
                    project: project.into(),
                    ..Default::default()
                },
            )
            .await
    }
}

#[tokio::test]
async fn confirm_accept_creates_the_change_and_starts_as_the_caller() {
    let mut e = new_env(&[]).await;
    let m = e.propose(NEW_CHANGE_ARG, in_project("PROJ-A")).await;
    let got = e.confirm(&m, DECISION_ACCEPT).await.unwrap();
    let created = e.graph.created();
    assert!(
        created.len() == 1
            && created[0].title == "Build v2"
            && created[0].methodology == "delivery"
            && created[0].namespace == "alm"
            && created[0].project_id == "PROJ-A"
            && e.graph.0.lock().who[0].subject == "u1",
        "change {created:?}"
    );
    let starts = e.engine.starts();
    assert_eq!(starts.len(), 1, "starts {starts:?}");
    let (st, who) = (&starts[0], e.engine.state.lock().start_who[0].clone());
    assert!(
        st.methodology == "delivery"
            && st.agent == "builder"
            && st.goal == "built"
            && st.change_id == "CHG-NEW"
            && st.project_id == "PROJ-A"
            && st.vars["conversationId"] == e.conv.id.as_str()
            && st.vars["messageId"] == m.id.as_str()
            && who.subject == "u1"
            && who.project == "PROJ-A",
        "start {st:?} as {who:?}"
    );
    let act = &got.actions[0];
    let res = obj(act, "result");
    assert!(
        s(act, "status") == STATUS_STARTED
            && s(&res, "processId") == "PRC-1"
            && s(&res, "changeId") == "CHG-NEW"
            && s(&obj(act, "decided"), "by") == "u1",
        "action {act:?}"
    );
    assert!(
        got.text == m.text && got.status == convsvc::STATUS_DONE,
        "the message changed: {got:?}"
    );
    // stored
    assert_eq!(s(&e.answer().await.actions[0], "status"), STATUS_STARTED);
    // a second decision, either way, is a conflict and starts nothing more
    for d in [DECISION_ACCEPT, DECISION_REJECT] {
        let err = e.confirm(&m, d).await.unwrap_err();
        assert!(err.is_decided(), "{d} again: {err}");
    }
    assert!(
        e.engine.starts().len() == 1 && e.graph.created().len() == 1,
        "started twice"
    );
}

#[tokio::test]
async fn confirm_accept_on_an_existing_change() {
    let e = new_env(&[]).await;
    let m = e.propose_step("delivery/build", change_ctx("CHG-D")).await;
    e.confirm(&m, DECISION_ACCEPT).await.unwrap();
    let starts = e.engine.starts();
    assert!(
        e.graph.created().is_empty()
            && starts.len() == 1
            && starts[0].change_id == "CHG-D"
            && starts[0].project_id == "PROJ-A"
            && starts[0].agent == "builder"
            && starts[0].goal == "delivery/build",
        "{starts:?}"
    );
}

#[tokio::test]
async fn confirm_reject_starts_nothing() {
    let mut e = new_env(&[]).await;
    let m = e.propose(NEW_CHANGE_ARG, in_project("PROJ-A")).await;
    let got = e.confirm(&m, DECISION_REJECT).await.unwrap();
    assert!(
        s(&got.actions[0], "status") == STATUS_REJECTED
            && e.engine.starts().is_empty()
            && e.graph.created().is_empty(),
        "{:?}",
        got.actions
    );
    assert!(
        e.confirm(&m, DECISION_ACCEPT)
            .await
            .unwrap_err()
            .is_decided()
    );
}

#[tokio::test]
async fn confirm_only_by_the_owner_on_a_proposal() {
    let mut e = new_env(&[]).await;
    let m = e.propose(NEW_CHANGE_ARG, in_project("PROJ-A")).await;
    let other = Principal {
        subject: "u2".into(),
        project: "PROJ-A".into(),
        ..Default::default()
    };
    let input = |message_id: &str, index: i64, decision: &str| ConfirmInput {
        conversation_id: e.conv.id.clone(),
        message_id: message_id.into(),
        action_index: index,
        decision: decision.into(),
        ..Default::default()
    };
    let err = e
        .svc
        .confirm(
            &with_principal(&Ctx::background(), other),
            input(&m.id, 0, DECISION_ACCEPT),
        )
        .await
        .unwrap_err();
    assert!(err.is_not_found(), "{err}");
    let sys = with_principal(&Ctx::background(), PRINCIPAL.clone());
    let err = e
        .svc
        .confirm(&sys, input(&m.id, 0, DECISION_ACCEPT))
        .await
        .unwrap_err();
    assert!(err.is_forbidden(), "{err}");
    // not a proposal, not a decision
    for in_ in [
        input(&m.id, 3, DECISION_ACCEPT),
        input(&m.id, 0, "maybe"),
        input("MSG-NOPE", 0, DECISION_ACCEPT),
    ] {
        assert!(
            e.svc.confirm(&e.ctx, in_.clone()).await.is_err(),
            "accepted {in_:?}"
        );
    }
    assert!(
        e.engine.starts().is_empty() && e.graph.created().is_empty(),
        "something ran"
    );
}

#[tokio::test]
async fn confirm_stale_proposal_stays_proposed() {
    let mut e = new_env(&[]).await;
    let m = e.propose(NEW_CHANGE_ARG, in_project("PROJ-A")).await;
    // the active project is no longer the proposal's
    let err = e
        .confirm_in(&m, DECISION_ACCEPT, "PROJ-B")
        .await
        .unwrap_err();
    assert!(
        err.is_stale() && err.to_string().contains("PROJ-B"),
        "{err}"
    );
    // the role was lost since
    e.engine.state.lock().held.insert(sv("u1"), Vec::new());
    assert!(e.confirm(&m, DECISION_ACCEPT).await.unwrap_err().is_stale());
    let got = e.answer().await;
    assert!(
        s(&got.actions[0], "status") == STATUS_PROPOSED
            && e.engine.starts().is_empty()
            && e.graph.created().is_empty(),
        "status {:?}",
        got.actions[0]
    );
    // a change closed since
    let e = new_env(&[]).await;
    let m = e.propose_step("delivery/build", change_ctx("CHG-D")).await;
    // the step is no longer possible when the person decides
    e.engine.state.lock().points.insert(
        sv("CHG-D"),
        Points {
            reason: "the goal is reached".into(),
            ..Default::default()
        },
    );
    let err = e.confirm(&m, DECISION_ACCEPT).await.unwrap_err();
    assert!(
        err.is_stale() && err.to_string().contains("no longer possible"),
        "{err}"
    );
    e.graph.0.lock().changes.get_mut("CHG-D").unwrap().status = CHANGE_APPLIED;
    let err = e.confirm(&m, DECISION_ACCEPT).await.unwrap_err();
    assert!(
        err.is_stale() && err.to_string().contains("applied"),
        "{err}"
    );
    // a stale proposal can still be rejected
    let got = e.confirm(&m, DECISION_REJECT).await.unwrap();
    assert_eq!(s(&got.actions[0], "status"), STATUS_REJECTED);
}

#[tokio::test]
async fn confirm_start_failure_is_recorded() {
    let mut e = new_env(&[]).await;
    let m = e.propose(NEW_CHANGE_ARG, in_project("PROJ-A")).await;
    e.engine.state.lock().start_err = Some("engine is down".into());
    let got = e.confirm(&m, DECISION_ACCEPT).await.unwrap();
    let act = &got.actions[0];
    assert!(
        s(act, "status") == STATUS_FAILED && s(act, "error").contains("engine is down"),
        "{act:?}"
    );
    // the change created before the failure is reported
    assert_eq!(s(&obj(act, "result"), "changeId"), "CHG-NEW", "{act:?}");
    assert!(
        e.confirm(&m, DECISION_ACCEPT)
            .await
            .unwrap_err()
            .is_decided()
    );
    // the change creation failing
    let mut e = new_env(&[]).await;
    let m = e.propose(NEW_CHANGE_ARG, in_project("PROJ-A")).await;
    e.graph.0.lock().err = Some("graph is down".into());
    let got = e.confirm(&m, DECISION_ACCEPT).await.unwrap();
    assert!(
        s(&got.actions[0], "status") == STATUS_FAILED && e.engine.starts().is_empty(),
        "{:?}",
        got.actions
    );
}

#[tokio::test]
async fn the_decision_is_in_the_next_history() {
    for (decision, want) in [
        (
            DECISION_ACCEPT,
            "accepted the proposal to start agent builder of delivery: it was started (process PRC-1 on change CHG-NEW)",
        ),
        (
            DECISION_REJECT,
            "rejected the proposal to start agent builder of delivery",
        ),
    ] {
        let mut e = new_env(&[]).await;
        let m = e.propose(NEW_CHANGE_ARG, in_project("PROJ-A")).await;
        // before the decision: nothing was started
        let (h, _) = history_of(&e.messages().await);
        assert!(
            h.last()
                .unwrap()
                .content
                .contains("has not decided yet, nothing was started"),
            "{h:?}"
        );
        e.confirm(&m, decision).await.unwrap();
        e.set_answers(&[r#"{"message":"ok"}"#]);
        e.send("and then?", in_project("PROJ-A")).await.unwrap();
        let all: Vec<String> = e.got()[0]
            .messages
            .iter()
            .map(|m| m.content.clone())
            .collect();
        let all = all.join("\n");
        assert!(all.contains(want), "{decision}: {all}");
    }
}

#[tokio::test]
async fn agent_tools_need_the_engine() {
    let mut e = new_env(&[&call(TOOL_LIST_AGENTS, "{}"), r#"{"message":"no"}"#]).await;
    e.svc_mut().engine = None;
    e.send("what can I run?", in_project("PROJ-A"))
        .await
        .unwrap();
    let res = e.tool_result(1);
    assert!(res.contains("engine is not reachable"), "{res}");
}

#[test]
fn tools_are_declared() {
    assert_eq!(tools().len(), 10);
    // each is declared to the model with what it does and the schema of its arguments (ADR 0098), and nothing else
    assert_eq!(SERVER_TOOLS.len(), tools().len());
    let f = Frame {
        level: Level::Project,
        project: "PROJ-A".into(),
        ..Default::default()
    };
    let prompt = system_prompt(
        &Context::default(),
        "PROJ-A",
        &[],
        "",
        &f,
        &f.tools(true),
        true,
    );
    for name in tools() {
        let spec = SERVER_TOOLS.iter().find(|s| s.name == name);
        assert!(
            spec.is_some_and(|s| !s.description.is_empty()
                && s.input_schema.get("type") == Some(&Value::from("object"))),
            "{name} is not declared"
        );
        assert!(prompt.contains(name), "the prompt does not name {name}");
    }
    // every tool of a level is one of them
    for l in [Level::Project, Level::Change] {
        let f = Frame {
            level: l,
            ..Default::default()
        };
        for n in f.tools(true) {
            assert!(tools().contains(&n.as_str()), "{l}: {n}");
        }
    }
}

#[test]
fn rpc_error_codes() {
    assert_eq!(rpc_err(&Error::Decided).code(), Code::Aborted);
    assert_eq!(
        rpc_err(&Error::Stale(String::new())).code(),
        Code::FailedPrecondition
    );
}
