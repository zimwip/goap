//! Go: steps_test.go.

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_domain::{self as domain, ChangeID};
use goap_engine::{
    Engine, ProcessProgress, STATUS_COMPLETED, STATUS_WAITING, STEP_ACTIVE, STEP_DONE, STEP_TODO,
    STEP_WAITING, StartRequest, StepProgress, TASK_AGENT, TASK_INPUT,
};
use goap_journal::{self as journal, History};
use serde_json::json;

use crate::common::*;

/// STAGED_YAML describes a delivery as a process: a phase with a sub-step done by an action, a step done by an agent
/// that plans, and a step that nests another process whose only step is manual (ADR 0034). The steps are declared out
/// of order: their conditions sequence them.
pub const STAGED_YAML: &str = r#"
name: staged
version: 1.0.0
namespace: alm
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.target == "note")'}
  - {name: checked, expr: 'artifacts.exists(a, a.target == "check")'}
actions:
  - {name: write_note, kind: human, description: Write the note, effects: {noted: true}}
  - {name: check, kind: human, description: Check the note, pre: {noted: true}, effects: {checked: true}}
goals:
  - {name: check_done, pre: {checked: true}}
agents:
  - {name: checker, actions: [check], goals: [check_done]}
roles:
  - {name: writer, description: Writes the notes}
  - {name: reviewer}
  - {name: signer}
methods:
  - name: peer_check
    for: verification
    when: 'artifacts.exists(a, a.target == "note")'
    priority: 10
    guidance: Check the note with a peer
    references: [{title: Peer review guide, ref: "doc:PEER"}]
    actions: [check]
    done: {checked: true}
  - {name: self_check, for: verification, actions: [check], done: {checked: true}}
processes:
  - name: sign_off
    steps:
      - {name: sign, instructions: Sign the delivery off, roles: {responsible: signer}}
  - name: delivery
    description: Deliver the change
    steps:
      - {name: approve, process: sign_off, pre: {checked: true}}
      - {name: verify, method: verification, pre: {noted: true}}
      - name: prepare
        roles: {responsible: writer, accountable: reviewer}
        steps:
          - {name: note, action: write_note}
"#;

/// staged_setup is the setup serving staged too (Go's stagedEngine, the engine not shared yet).
pub async fn staged_setup() -> Setup {
    let mut s = setup().await;
    add_yaml(&mut s, STAGED_YAML);
    s
}

/// staged_engine is the engine of staged_setup, shared.
pub async fn staged_engine() -> (Arc<Engine>, Errs) {
    let (e, _, _, errs) = staged_setup().await.share();
    (e, errs)
}

#[tokio::test]
async fn process_steps_and_nested_processes() {
    let ctx = ctx();
    let (e, _errs) = staged_engine().await;
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "staged".into(),
                goal: "delivery".into(),
                intent: "deliver the note".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(
        p.agent, "delivery",
        "the process is run by the agent of its name"
    );
    let p = e.run(&ctx, &p.id).await.unwrap();
    // the sub-step done by an action comes first
    let t = p.pending.as_ref().expect("pending");
    assert!(
        p.status == STATUS_WAITING && t.kind == TASK_INPUT && t.action == "delivery/prepare/note",
        "expected the note step, got {p:?}"
    );
    let c = t
        .context
        .as_ref()
        .expect("a task of a step carries the step");
    assert!(
        c.path == "delivery/prepare/note" && c.process == "delivery",
        "a task of a step carries the step: {c:?}"
    );
    let pr = e.progress(&ctx, &p.id).await.unwrap();
    let states = progress_states(&pr.steps);
    assert!(
        states["delivery/prepare"] == STEP_WAITING
            && states["delivery/prepare/note"] == STEP_WAITING
            && states["delivery/verify"] == STEP_TODO
            && states["delivery/approve"] == STEP_TODO
            && pr.done == 0
            && pr.total == 3,
        "progress at the start: {states:?} {}/{}",
        pr.done,
        pr.total
    );
    let v = find_step(&pr.steps, "delivery/verify");
    assert_eq!(
        v.missing,
        ["noted"],
        "the verify step misses the note: {v:?}"
    );
    // the task of a step is performed by its responsible role (inherited from the phase), held in the unit of the
    // change or above it
    let roles = c.roles.as_ref().expect("roles");
    assert!(
        roles.responsible == "writer" && roles.accountable == "reviewer",
        "the note step inherits the roles of its phase: {roles:?}"
    );
    let outsider = as_who(&principal("o", "", &["contributor", "writer@TEAM-OTHER"]));
    let err = e
        .submit(&outsider, &p.id, vec![artifact("note", json!({}))])
        .await
        .unwrap_err();
    assert!(
        err.is_forbidden(),
        "a person without the responsible role here may not perform the step: {err}"
    );
    let writer = as_who(&principal("w", "", &["writer@ORG-DEFAULT"]));
    e.submit(&writer, &p.id, vec![artifact("note", json!({}))])
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    // the step done by an agent starts it on the same change, towards its goal
    let t = p.pending.clone().expect("pending");
    assert!(
        p.status == STATUS_WAITING && t.kind == TASK_AGENT && t.action == "delivery/verify",
        "expected the agent step, got {p:?}"
    );
    let st = progress_states(&must_progress(&e, &p.id).await.steps);
    assert!(
        st["delivery/prepare/note"] == STEP_DONE && st["delivery/verify"] == STEP_ACTIVE,
        "progress with the checker at work: {st:?}"
    );
    let checker = get(&e, &t.child_process_id).await;
    assert!(
        checker
            .step
            .as_ref()
            .is_some_and(|s| s.path == "delivery/verify")
            && checker
                .pending
                .as_ref()
                .and_then(|t| t.context.as_ref())
                .is_some_and(|c| c.path == "delivery/verify"),
        "the sub-agent of a step carries the step to its tasks: {checker:?}"
    );
    // the step names a capability: the applicable method with the highest priority was chosen, and its guidance
    // and references reach the agent that acts
    let c = checker.pending.as_ref().unwrap().context.as_ref().unwrap();
    assert!(
        c.method == "peer_check" && c.guidance.contains("with a peer") && c.references.len() == 1,
        "the method's guidance reaches its agent: {c:?}"
    );
    let v = find_step(&must_progress(&e, &p.id).await.steps, "delivery/verify");
    assert!(
        v.chosen == "peer_check" && v.target == "verification",
        "the progress shows the chosen method: {v:?}"
    );
    assert!(
        checker.agent == "peer_check"
            && checker.goal == "peer_check"
            && checker.change_id == p.change_id
            && checker
                .pending
                .as_ref()
                .is_some_and(|t| t.action == "check"),
        "unexpected checker {checker:?}"
    );
    // the method assigns no role
    e.submit(&ctx, &checker.id, vec![artifact("check", json!({}))])
        .await
        .unwrap();
    schedule(&e, &checker.id).await;
    // then the nested process, whose manual step is a human task
    let p = get(&e, &p.id).await;
    let t = p.pending.clone().expect("pending");
    assert!(
        p.status == STATUS_WAITING && t.kind == TASK_AGENT && t.action == "delivery/approve",
        "expected the nested process, got {p:?}"
    );
    let signer = get(&e, &t.child_process_id).await;
    assert!(
        signer.agent == "sign_off"
            && signer.pending.as_ref().is_some_and(
                |t| t.action == "sign_off/sign" && t.instructions == "Sign the delivery off"
            ),
        "unexpected nested process {signer:?}"
    );
    e.submit(
        &as_who(&principal("s", "", &["signer"])),
        &signer.id,
        vec![],
    )
    .await
    .unwrap();
    schedule(&e, &signer.id).await;
    let p = get(&e, &p.id).await;
    assert_eq!(p.status, STATUS_COMPLETED, "delivery not completed: {p:?}");
    let pr = must_progress(&e, &p.id).await;
    assert!(
        pr.done == pr.total && progress_states(&pr.steps)["delivery/prepare"] == STEP_DONE,
        "progress at the end: {:?} {}/{}",
        progress_states(&pr.steps),
        pr.done,
        pr.total
    );
    let mut steps: Vec<String> = Vec::new();
    for s in &p.steps {
        // a step waiting for its sub-agent runs again once it has ended
        if steps.last() != Some(&s.action) {
            steps.push(s.action.clone());
        }
    }
    assert_eq!(
        steps.join(","),
        "delivery/prepare/note,delivery/verify,delivery/approve"
    );
    // every step ran to its end, its run standing (ADR 0109): the agent step, and the manual step once submitted
    let last = history_of(&e, &p.change_id).await.last();
    for path in ["delivery/prepare/note", "sign_off/sign"] {
        let s = last.get(path).cloned().unwrap_or_default();
        assert!(
            s.outcome == journal::OUTCOME_SUCCEEDED && s.current,
            "the history of {path}: {s:?}"
        );
    }
}

/// history_of is the history of a change (ADR 0109), as the engine folds it.
pub async fn history_of(e: &Arc<Engine>, id: &ChangeID) -> History {
    let bb = Engine::internals_of(e)
        .blackboard(&ctx(), id, "")
        .await
        .unwrap();
    domain::facet::<History>(&bb, domain::FACET_HISTORY)
}

pub async fn must_progress(e: &Engine, id: &str) -> ProcessProgress {
    e.progress(&ctx(), id).await.unwrap()
}

pub fn progress_states(steps: &[StepProgress]) -> BTreeMap<String, String> {
    let mut out = BTreeMap::new();
    for s in steps {
        out.insert(s.path.clone(), s.state.clone());
        out.extend(progress_states(&s.steps));
    }
    out
}

pub fn find_step(steps: &[StepProgress], path: &str) -> StepProgress {
    for s in steps {
        if s.path == path {
            return s.clone();
        }
        let f = find_step(&s.steps, path);
        if !f.path.is_empty() {
            return f;
        }
    }
    StepProgress::default()
}

/// A capability provided by a method that composes its own steps (architecture plan "Activity concept"): the method
/// is run as a sub-agent of its own name, exactly like a nested process, and completing its own steps completes the
/// outer step that named the capability.
const METHOD_STEPS_YAML: &str = r#"
name: inspected
version: 1.0.0
namespace: alm
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.target == "note")'}
  - {name: checked, expr: 'artifacts.exists(a, a.target == "check")'}
actions:
  - {name: write_note, kind: human, effects: {noted: true}}
  - {name: check, kind: human, pre: {noted: true}, effects: {checked: true}}
methods:
  - name: thorough_check
    for: verification
    steps:
      - {name: note, action: write_note}
      - {name: verify, action: check}
processes:
  - name: delivery
    steps:
      - {name: verify, method: verification}
"#;

#[tokio::test]
async fn method_composed_of_steps_runs_as_a_sub_agent() {
    let ctx = ctx();
    let mut s = setup().await;
    add_yaml(&mut s, METHOD_STEPS_YAML);
    let (e, _, _, _errs) = s.share();
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "inspected".into(),
                goal: "delivery".into(),
                intent: "inspect it".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    let t = p.pending.clone().expect("pending");
    assert!(
        p.status == STATUS_WAITING && t.kind == TASK_AGENT && t.action == "delivery/verify",
        "expected the method-capability step, got {p:?}"
    );
    let sub = get(&e, &t.child_process_id).await;
    // the method composing its own steps runs as a sub-agent of its own name, towards a goal of its own name
    assert!(
        sub.agent == "thorough_check" && sub.goal == "thorough_check",
        "sub-agent of a steps-composed method: {sub:?}"
    );
    assert!(
        sub.pending
            .as_ref()
            .is_some_and(|t| t.action == "thorough_check/note"),
        "the method's own first step: {:?}",
        sub.pending
    );
    e.submit(&ctx, &sub.id, vec![artifact("note", json!({}))])
        .await
        .unwrap();
    schedule(&e, &sub.id).await;
    let sub = get(&e, &sub.id).await;
    assert!(
        sub.pending
            .as_ref()
            .is_some_and(|t| t.action == "thorough_check/verify"),
        "the method's own second step: {sub:?}"
    );
    e.submit(&ctx, &sub.id, vec![artifact("check", json!({}))])
        .await
        .unwrap();
    schedule(&e, &sub.id).await;
    let sub = get(&e, &sub.id).await;
    assert_eq!(sub.status, STATUS_COMPLETED, "method sub-agent: {sub:?}");
    let p = get(&e, &p.id).await;
    assert_eq!(p.status, STATUS_COMPLETED, "outer process: {p:?}");
}
