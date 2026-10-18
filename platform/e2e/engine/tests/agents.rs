//! Go: agents_test.go.

use std::sync::Arc;

use goap_authz::Principal;
use goap_ctx::Ctx;
use goap_domain as domain;
use goap_engine::{
    Engine, InprocSandboxes, ItemInput, STATUS_COMPLETED, STATUS_WAITING, ScriptExecutor,
    StartRequest, TASK_AGENT,
};
use goap_llm as llm;
use goap_methodology as methodology;
use serde_json::{Value, json};

use crate::common::*;

/// agents_setup extends the impact-analysis setup with test-design, scripts run in-process and a fake model gateway.
pub async fn agents_setup() -> (Arc<Engine>, domain::BaselineID, Errs) {
    let (e, _, base, errs) = agents_base().await.share();
    (e, base, errs)
}

/// agents_base is the setup of agents_setup, not shared yet.
pub async fn agents_base() -> Setup {
    let mut s = setup().await;
    s.add(load_methodology("test-design.yaml"));
    s.e.executors.insert(
        methodology::KIND_SCRIPT.into(),
        Arc::new(ScriptExecutor {
            sandboxes: Some(Arc::new(InprocSandboxes)),
        }),
    );
    s.e.llm = Some(llm::ClientFunc::shared(
        |_ctx: Ctx, r: llm::Request| async move {
            Ok(llm::Response {
                text: "Test campaign.".into(),
                provider: "test".into(),
                model: r.model.clone(),
                usage: llm::Usage {
                    input_tokens: 42,
                    output_tokens: 7,
                    ..Default::default()
                },
                ..Default::default()
            })
        },
    ));
    s
}

fn start(base: &domain::BaselineID, agent: &str, intent: &str) -> StartRequest {
    StartRequest {
        methodology: "test-design".into(),
        agent: agent.into(),
        baseline_id: base.clone(),
        project_id: TEST_PROJECT.into(),
        intent: intent.into(),
        ..Default::default()
    }
}

fn artifact_input(target: &str) -> Vec<ItemInput> {
    vec![ItemInput {
        r#type: "artifact".into(),
        target: target.into(),
        ..Default::default()
    }]
}

#[tokio::test]
async fn identify_agent_across_methodologies() {
    let ctx = ctx();
    let (e, base, _errs) = agents_setup().await;
    // the intent text deliberately echoes the coordinator's example phrase in methodologies/examples/test-design.yaml.
    let p = e
        .start(
            &ctx,
            StartRequest {
                baseline_id: base,
                project_id: TEST_PROJECT.into(),
                intent: "prepare and validate the test campaign".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        p.methodology == "test-design"
            && p.agent == "coordinator"
            && p.goal == "delivered"
            && p.planner == "hybrid",
        "wrong identification: {}/{}/{} ({}) {:?}",
        p.methodology,
        p.agent,
        p.goal,
        p.status,
        p.candidates
    );
    assert!(
        p.candidates[0].agent == "coordinator" && p.candidates[0].methodology == "test-design",
        "candidates not qualified: {:?}",
        p.candidates[0]
    );
}

#[tokio::test]
async fn script_agent_with_llm_usage() {
    let ctx = ctx();
    let (e, base, _errs) = agents_setup().await;
    let p = e
        .start(&ctx, start(&base, "test-designer", "design the test cases"))
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_COMPLETED,
        "status {} {} steps={:?}",
        p.status,
        p.error,
        p.steps
    );
    assert_eq!(actions_of(&p), "collect_scope,design_tests,summarize");
    let sum = &p.steps[2];
    assert!(
        sum.llm_calls.len() == 1
            && sum.usage.input_tokens == 42
            && sum.llm_calls[0].model == "fast"
            && sum.sandbox == "inproc",
        "llm usage not recorded: {sum:?}"
    );
    assert!(
        p.usage.input_tokens == 42 && p.usage.output_tokens == 7 && p.usage.llm_calls == 1,
        "process usage {:?}",
        p.usage
    );
    // "requirement" is logged by the collect_scope script in methodologies/examples/test-design.yaml.
    let logs = &p.steps[0].logs;
    assert!(
        logs.len() == 1 && logs[0].message.contains("requirement"),
        "script logs not recorded: {logs:?}"
    );
}

#[tokio::test]
async fn sub_agents_with_suspension() {
    let ctx = ctx();
    let (e, base, _errs) = agents_setup().await;
    let mut req = start(
        &base,
        "coordinator",
        "prepare and validate the test campaign",
    );
    req.vars = obj(json!({"review": "human"}));
    let p = e.start(&ctx, req).await.unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    // the reviewer (utility planner) prefers the human review: the coordinator is suspended
    let pending = p.pending.clone().unwrap_or_default();
    assert!(
        p.status == STATUS_WAITING
            && pending.kind == TASK_AGENT
            && !pending.child_process_id.is_empty(),
        "expected suspension on a sub-agent, got {} {pending:?} {}",
        p.status,
        p.error
    );
    let reviewer = get(&e, &pending.child_process_id).await;
    assert!(
        reviewer.agent == "reviewer"
            && reviewer.parent_id == p.id
            && reviewer.status == STATUS_WAITING
            && reviewer
                .pending
                .as_ref()
                .is_some_and(|t| t.action == "human_review"),
        "unexpected reviewer {reviewer:?}"
    );
    assert_eq!(
        p.steps[0].children.len(),
        2,
        "children not recorded on the step: {:?}",
        p.steps[0]
    );
    // the human accepts every test case of the shared change
    let bb = e.graph.blackboard_in(&ctx, &p.change_id, "").await.unwrap();
    let decisions: Vec<ItemInput> = bb
        .change
        .nodes
        .iter()
        .filter(|n| n.intent == domain::INTENT_CREATED)
        .map(|n| ItemInput {
            r#type: "changeImpact".into(),
            change_impact: Some(goap_dsl::NodeOp {
                op: "review".into(),
                node: n.key.clone(),
                accept: true,
                comment: "reviewed by a human".into(),
                ..Default::default()
            }),
            ..Default::default()
        })
        .collect();
    assert!(
        !decisions.is_empty(),
        "no test case to review: {:?}",
        bb.change.nodes
    );
    // human_review is a test_reviewer's (ADR 0043): another role on the project may not perform it
    let who = |s: &str, role: &str| {
        as_who(&Principal {
            subject: s.into(),
            roles: vec![role.into()],
            ..Default::default()
        })
    };
    let err = e
        .submit(
            &who("dan", "test_designer"),
            &reviewer.id,
            decisions.clone(),
        )
        .await
        .expect_err("a test_designer performed the review");
    assert!(
        err.is_forbidden(),
        "a test_designer performed the review: {err}"
    );
    e.submit(&who("rita", "test_reviewer"), &reviewer.id, decisions)
        .await
        .unwrap();
    schedule(&e, &reviewer.id).await;
    let p = get(&e, &p.id).await;
    assert!(
        p.status == STATUS_COMPLETED,
        "coordinator not resumed: {} {:?} {}",
        p.status,
        p.pending,
        p.error
    );
    assert!(
        p.children.is_empty(),
        "children map must be cleared after the action: {:?}",
        p.children
    );
    let designer = get(&e, &p.steps[p.steps.len() - 1].children[0]).await;
    assert!(
        designer.agent == "test-designer" && designer.usage.llm_calls == 1,
        "designer child {designer:?}"
    );
}

#[tokio::test]
async fn sub_agent_wakes_parent_on_signal() {
    let ctx = ctx();
    let (e, base, _errs) = agents_setup().await;
    let p = e
        .start(
            &ctx,
            start(&base, "watcher", "watch a pinger and wake on its signal"),
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    let pending = p.pending.clone().unwrap_or_default();
    assert!(
        p.status == STATUS_WAITING
            && p.pending.is_some()
            && pending.kind == TASK_AGENT
            && !pending.child_process_id.is_empty(),
        "expected the watcher suspended on the pinger, got {} {:?} {}",
        p.status,
        p.pending,
        p.error
    );
    assert_eq!(
        pending.wake_on,
        ["progress"],
        "wakeOn not recorded on the pending task"
    );
    let child = pending.child_process_id.clone();
    let pinger = get(&e, &child).await;
    assert!(
        pinger.agent == "pinger" && pinger.parent_id == p.id && pinger.status == STATUS_WAITING,
        "unexpected pinger {pinger:?}"
    );
    let steps_before_signal = p.steps.len();

    // let the pinger send its progress signal, without letting it terminate
    e.submit(&ctx, &child, artifact_input("go")).await.unwrap();
    schedule(&e, &child).await;
    let pinger = get(&e, &child).await;
    assert!(
        !pinger.status.terminal(),
        "pinger must still be running (not terminated) when it signals, got {}",
        pinger.status
    );
    assert!(
        pinger.status == STATUS_WAITING,
        "pinger expected waiting again after signaling: {} {}",
        pinger.status,
        pinger.error
    );

    // the watcher must have been woken and replayed wait_for_ping BEFORE the pinger terminated: a new step, and a
    // fresh suspension on the same child.
    let p = get(&e, &p.id).await;
    assert!(
        p.steps.len() == steps_before_signal + 1
            && p.steps[p.steps.len() - 1].action == "wait_for_ping",
        "watcher was not woken early by the signal (steps before={steps_before_signal} after={}): {:?}",
        p.steps.len(),
        p.steps
    );
    assert!(
        p.status == STATUS_WAITING
            && p.pending
                .as_ref()
                .is_some_and(|t| t.child_process_id == child),
        "watcher must have re-suspended on the still-running pinger: {} {:?}",
        p.status,
        p.pending
    );

    // now let the pinger actually finish, and the watcher complete
    e.submit(&ctx, &child, artifact_input("wrapped"))
        .await
        .unwrap();
    schedule(&e, &child).await;
    let p = get(&e, &p.id).await;
    assert!(
        p.status == STATUS_COMPLETED,
        "watcher not completed: {} {:?} {}",
        p.status,
        p.pending,
        p.error
    );
}

/// A sub-agent's vars are cloned at spawn, so a mutation on either side (SetVar) is not visible to the other.
#[tokio::test]
async fn child_vars_are_isolated() {
    let ctx = ctx();
    let (e, base, _errs) = agents_setup().await;
    let mut req = start(&base, "watcher", "watch a pinger and wake on its signal");
    req.vars = obj(json!({"shared": "parent"}));
    let p = e.start(&ctx, req).await.unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_WAITING && p.pending.is_some(),
        "expected the watcher suspended on the pinger, got {} {:?} {}",
        p.status,
        p.pending,
        p.error
    );
    let child = p.pending.as_ref().unwrap().child_process_id.clone();
    let pinger = get(&e, &child).await;
    assert_eq!(
        pinger.vars.get("shared"),
        Some(&Value::from("parent")),
        "child did not inherit the parent's vars at spawn"
    );

    // the pinger's own ping/wait_ready scripts set vars ("pinged"); the parent must not see them, and the parent's
    // own var must stay untouched by the child.
    e.submit(&ctx, &child, artifact_input("go")).await.unwrap();
    schedule(&e, &child).await;
    let pinger = get(&e, &child).await;
    assert_eq!(
        pinger.vars.get("pinged"),
        Some(&Value::Bool(true)),
        "child's own SetVar was not recorded"
    );
    assert_eq!(
        pinger.vars.get("shared"),
        Some(&Value::from("parent")),
        "child lost the inherited var"
    );
    let p = get(&e, &p.id).await;
    assert!(
        !p.vars.contains_key("pinged"),
        "child's SetVar leaked into the parent's vars: {:?}",
        p.vars
    );
    assert_eq!(
        p.vars.get("shared"),
        Some(&Value::from("parent")),
        "parent's own var was affected by the child"
    );
}

#[tokio::test]
async fn utility_planner_auto_review() {
    let ctx = ctx();
    let (e, base, _errs) = agents_setup().await;
    let d = e
        .start(&ctx, start(&base, "test-designer", "design"))
        .await
        .unwrap();
    let d = e.run(&ctx, &d.id).await.unwrap();
    let r = e
        .start(
            &ctx,
            StartRequest {
                methodology: "test-design".into(),
                agent: "reviewer".into(),
                change_id: d.change_id.clone(),
                intent: "review".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let r = e.run(&ctx, &r.id).await.unwrap();
    assert!(
        r.status == STATUS_COMPLETED && r.steps[0].action == "auto_review",
        "utility planner should pick auto_review: {} {:?}",
        r.status,
        r.steps
    );
}
