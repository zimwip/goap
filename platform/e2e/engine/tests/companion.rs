//! Go: companion_test.go.

use std::sync::Arc;
use std::time::{Duration, Instant};

use goap_domain::{self as domain, ChangeID, ChangeItem};
use goap_engine::{
    Broker, Engine, Process, STATUS_COMPLETED, STATUS_WAITING, STEP_WAITING, StartRequest,
    TASK_CONDITION, TriggerEvent, TriggerManager,
};
use goap_graph::{NewChange, NodeCheckout, NodeUpdate};
use goap_methodology as methodology;
use serde_json::{Value, json};

use crate::common::*;
use crate::steps::{find_step, must_progress, staged_setup};

/// RISKY_YAML is a transverse methodology (ADR 0036 §3): it applies to staged, so its process runs alongside the
/// changes of staged, on the same change.
pub const RISKY_YAML: &str = r#"
name: risky
version: 1.0.0
appliesTo: [staged]
imports: [risks]
on:
  - {event: process.attached}
  - {event: step.completed}
  - {event: change.item_added, filter: 'event.items.exists(i, i.type == "risk" || i.type == "action")'}
conditions:
  - {name: risks_identified, expr: 'artifacts.exists(a, a.target == "risk_review")'}
actions:
  - {name: identify, kind: human, description: Identify the risks, effects: {risks_identified: true}}
  - {name: mitigate, kind: human, description: Define the mitigation actions, pre: {risks_identified: true}, effects: {risks_under_control: true}}
processes:
  - name: risk_watch
    steps:
      - {name: identify, action: identify}
      - {name: mitigate, action: mitigate}
"#;

/// companion_of gives the companion runs of a change.
pub async fn companion_of(e: &Engine, change: &ChangeID) -> Vec<Process> {
    let all = e.store.list(&ctx()).await.unwrap();
    all.into_iter()
        .filter(|p| p.change_id == *change && p.is_companion())
        .collect()
}

/// accompany hands an event to the engine and waits for the work it started.
pub async fn accompany(e: &Arc<Engine>, ev: TriggerEvent) {
    e.accompany(&ctx(), &ev).await;
    e.drain().await;
}

pub fn item(typ: &str, data: Value) -> goap_engine::ItemInput {
    goap_engine::ItemInput {
        r#type: typ.into(),
        data: obj(data),
        ..Default::default()
    }
}

#[tokio::test]
async fn transverse_process_runs_alongside_the_change() {
    let ctx = ctx();
    let mut s = staged_setup().await;
    let m = methodology::parse(RISKY_YAML.as_bytes()).unwrap();
    let issues = m.validate_stored();
    assert!(
        issues.0.is_empty(),
        "a transverse methodology needs no namespace: {issues:?}"
    );
    s.add(Arc::new(m.compile().unwrap()));
    let (e, g, _, _errs) = s.share();

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
    // the change of staged is created: the risk process joins it
    accompany(
        &e,
        TriggerEvent {
            r#type: "process.attached".into(),
            process: Some(p.clone()),
            ..Default::default()
        },
    )
    .await;
    let cs = companion_of(&e, &p.change_id).await;
    assert!(
        cs.len() == 1
            && cs[0].methodology == "risky"
            && cs[0].agent == "risk_watch"
            && cs[0]
                .pending
                .as_ref()
                .is_some_and(|t| t.action == "risk_watch/identify"),
        "companion {cs:?}"
    );
    let risk = cs[0].clone();
    // the risks are identified: a high one needs an action
    e.submit(
        &ctx,
        &risk.id,
        vec![
            artifact("risk_review", json!({})),
            item(
                "risk",
                json!({"key": "RSK-1", "title": "the note is wrong", "probability": 4.0, "impact": 4.0}),
            ),
        ],
    )
    .await
    .unwrap();
    let risk = e.run(&ctx, &risk.id).await.unwrap();
    assert!(
        risk.pending
            .as_ref()
            .is_some_and(|t| t.action == "risk_watch/mitigate"),
        "mitigation expected: {risk:?}"
    );
    e.submit(
        &ctx,
        &risk.id,
        vec![item(
            "action",
            json!({"key": "ACT-1", "title": "review the note", "for": "RSK-1"}),
        )],
    )
    .await
    .unwrap();
    let risk = e.run(&ctx, &risk.id).await.unwrap();
    assert_eq!(
        risk.status, STATUS_COMPLETED,
        "risks under control: {risk:?}"
    );
    let steps = risk.steps.len();
    // the change moves: the companion runs again in place, and has nothing to do
    accompany(
        &e,
        TriggerEvent {
            r#type: "process.completed".into(),
            process: Some(p.clone()),
            ..Default::default()
        },
    )
    .await;
    let risk = get(&e, &risk.id).await;
    assert!(
        risk.status == STATUS_COMPLETED && risk.steps.len() == steps,
        "nothing to do: {} {} steps",
        risk.status,
        risk.steps.len()
    );
    // someone raises a new high risk: the companion wakes up and asks for its mitigation
    let items = g
        .add_items(
            &ctx,
            &p.change_id,
            vec![ChangeItem {
                r#type: goap_risk::TYPE_RISK,
                status: domain::ITEM_PROPOSED,
                produced_by: "alice".into(),
                data: obj(
                    json!({"key": "RSK-2", "title": "the reviewer is away", "probability": 3.0, "impact": 5.0}),
                ),
                ..Default::default()
            }],
        )
        .await
        .unwrap();
    let bb = g.blackboard(&ctx, &p.change_id).await.unwrap();
    accompany(
        &e,
        TriggerEvent {
            r#type: "change.item_added".into(),
            change: Some(bb.change.clone()),
            items,
            ..Default::default()
        },
    )
    .await;
    let risk = get(&e, &risk.id).await;
    assert!(
        risk.status == STATUS_WAITING
            && risk
                .pending
                .as_ref()
                .is_some_and(|t| t.action == "risk_watch/mitigate"),
        "the new risk needs a mitigation: {risk:?}"
    );
    // its own productions do not wake it, and there is only ever one companion per change and process
    accompany(
        &e,
        TriggerEvent {
            r#type: "process.attached".into(),
            process: Some(p.clone()),
            ..Default::default()
        },
    )
    .await;
    let cs = companion_of(&e, &p.change_id).await;
    assert_eq!(cs.len(), 1, "one companion per change");
}

/// choreography of two methodologies through the events of the change they share (ADR 0036 §3): the risk watch
/// reviews the risks at each step of the flow (the events that arrive while it works wait in its inbox), and the
/// flow's approval waits for the risks to be under control — it is tried again when the watch has mitigated them.
pub const CHOREO_YAML: &str = r#"
name: choreo
version: 1.0.0
namespace: alm
imports: [risks]
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.target == "note")'}
actions:
  - {name: write_note, kind: human, effects: {noted: true}}
processes:
  - name: flow
    steps:
      - {name: note, action: write_note}
      - {name: approve, instructions: Approve the delivery, pre: {noted: true, risks_under_control: true}}
"#;

const WATCH_YAML: &str = r#"
name: watch
version: 1.0.0
appliesTo: [choreo]
imports: [risks]
on:
  - {event: process.attached}
  - {event: step.completed, filter: 'event.step.process == "flow"'}
conditions:
  # reviewed for the event that woke the watch: a review written after it
  - {name: reviewed, expr: 'artifacts.exists(a, a.target == "risk_review" && has(vars.event) && double(a.at) >= double(vars.event.at))'}
actions:
  - {name: review, kind: human, effects: {reviewed: true}}
  - {name: mitigate, kind: human, pre: {reviewed: true}, effects: {risks_under_control: true}}
processes:
  - name: risk_watch
    steps:
      - {name: review, action: review}
      - {name: mitigate, action: mitigate}
"#;

/// eventually waits up to 3 seconds for ok.
pub async fn eventually<F, Fut>(what: &str, mut ok: F)
where
    F: FnMut() -> Fut,
    Fut: std::future::Future<Output = bool>,
{
    let deadline = Instant::now() + Duration::from_secs(3);
    while Instant::now() < deadline {
        if ok().await {
            return;
        }
        tokio::time::sleep(Duration::from_millis(15)).await;
    }
    panic!("never: {what}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn choreography_through_the_events_of_the_change() {
    let (ctx, cancel) = ctx().with_cancel();
    let mut s = staged_setup().await;
    add_yaml(&mut s, CHOREO_YAML);
    add_yaml(&mut s, WATCH_YAML);
    let broker = Arc::new(Broker::new());
    s.e.events = Some(broker.clone());
    let (e, _, _, _errs) = s.share();
    let tm = TriggerManager::new(e.clone());
    {
        let (tm, ctx, broker) = (tm.clone(), ctx.clone(), broker.clone());
        tokio::spawn(async move { tm.watch_processes(&ctx, &broker).await });
    }
    tokio::time::sleep(Duration::from_millis(20)).await; // subscription ready

    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "choreo".into(),
                goal: "flow".into(),
                intent: "deliver".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    Engine::internals_of(&e).schedule(&p.id);
    let found: Arc<parking_lot::Mutex<Option<Process>>> = Arc::default();
    eventually("the watch joins the change and asks for a review", || {
        let e = e.clone();
        let change = p.change_id.clone();
        let w = found.clone();
        async move {
            let cs = companion_of(&e, &change).await;
            if cs.len() == 1
                && cs[0]
                    .pending
                    .as_ref()
                    .is_some_and(|t| t.action == "risk_watch/review")
            {
                *w.lock() = Some(cs[0].clone());
                return true;
            }
            false
        }
    })
    .await;
    let watch = found.lock().clone().unwrap();
    // the review raises a high risk: its mitigation is asked
    e.submit(
        &ctx,
        &watch.id,
        vec![
            artifact("risk_review", json!({})),
            item(
                "risk",
                json!({"key": "RSK-1", "title": "wrong note", "probability": 4.0, "impact": 4.0}),
            ),
        ],
    )
    .await
    .unwrap();
    Engine::internals_of(&e).schedule(&watch.id);
    let pending_is = |id: String, action: &'static str| {
        let e = e.clone();
        move || {
            let (e, id) = (e.clone(), id.clone());
            async move {
                get(&e, &id)
                    .await
                    .pending
                    .is_some_and(|t| t.action == action)
            }
        }
    };
    eventually(
        "mitigation asked",
        pending_is(watch.id.clone(), "risk_watch/mitigate"),
    )
    .await;
    eventually(
        "the flow asks for the note",
        pending_is(p.id.clone(), "flow/note"),
    )
    .await;

    // the flow completes its note step: the event waits in the watch's inbox; the approval waits for the risks
    e.submit(&ctx, &p.id, vec![artifact("note", json!({}))])
        .await
        .unwrap();
    Engine::internals_of(&e).schedule(&p.id);
    eventually(
        "the flow waits for the risks to be under control and the watch has the step in its inbox",
        || {
            let (e, pid, wid) = (e.clone(), p.id.clone(), watch.id.clone());
            async move {
                get(&e, &pid).await.waits_for_conditions() && get(&e, &wid).await.inbox.len() == 1
            }
        },
    )
    .await;
    let q = get(&e, &p.id).await;
    assert_eq!(
        q.pending.as_ref().unwrap().conditions,
        ["risks_under_control"]
    );
    let st = find_step(&must_progress(&e, &p.id).await.steps, "flow/approve");
    assert!(
        st.state == STEP_WAITING
            && st.waiting == TASK_CONDITION
            && st.missing.iter().any(|m| m == "risks_under_control"),
        "approval: {st:?}"
    );

    // the watch mitigates: the flow is tried again and reaches its approval; the watch reviews the risks of the step
    e.submit(
        &ctx,
        &watch.id,
        vec![item(
            "action",
            json!({"key": "ACT-1", "title": "check the note", "for": "RSK-1"}),
        )],
    )
    .await
    .unwrap();
    Engine::internals_of(&e).schedule(&watch.id);
    eventually(
        "the flow resumes to its approval",
        pending_is(p.id.clone(), "flow/approve"),
    )
    .await;
    eventually(
        "the watch reviews the risks of the step that completed",
        || {
            let (e, wid) = (e.clone(), watch.id.clone());
            async move {
                let w = get(&e, &wid).await;
                let path = w
                    .vars
                    .get("event")
                    .and_then(|ev| ev.get("step"))
                    .and_then(|s| s.get("path"))
                    .cloned();
                w.pending
                    .as_ref()
                    .is_some_and(|t| t.action == "risk_watch/review")
                    && path == Some(Value::from("flow/note"))
                    && w.inbox.is_empty()
            }
        },
    )
    .await;
    let cs = companion_of(&e, &p.change_id).await;
    assert_eq!(cs.len(), 1, "one watch per change");
    cancel.cancel();
}

/// a step waiting for the changes its change depends on (ADR 0108): no action produces `dependencies_met`, so the
/// run waits; the dependency merging wakes it (Engine.relay to the dependents).
const DEPENDENT_YAML: &str = r#"
name: dependent
version: 1.0.0
namespace: alm
imports: [dependencies]
processes:
  - name: flow
    steps:
      - {name: deliver, instructions: Deliver once the changes it depends on have landed, pre: {dependencies_met: true}}
"#;

#[tokio::test]
async fn run_waits_for_the_changes_it_depends_on() {
    let ctx = ctx();
    let mut s = staged_setup().await;
    add_yaml(&mut s, DEPENDENT_YAML);
    let (e, g, _, _errs) = s.share();
    let head = g
        .branch_head(&ctx, "alm", domain::MAIN_BRANCH)
        .await
        .unwrap();
    let dep = g
        .create_change(
            &ctx,
            NewChange {
                title: "first".into(),
                methodology: "dependent".into(),
                namespace: "alm".into(),
                project_id: TEST_PROJECT.into(),
                baseline_id: head.id.clone(),
                own_branch: true,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "dependent".into(),
                goal: "flow".into(),
                intent: "deliver after the first".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    g.set_change_dependencies(&ctx, &p.change_id, std::slice::from_ref(&dep.id))
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.waits_for_conditions() && p.pending.as_ref().unwrap().conditions == ["dependencies_met"],
        "the run waits for its dependency: {p:?}"
    );
    // the first change lands: it modifies a component
    let cn = g
        .impact_node_checkout(
            &ctx,
            &dep.id,
            NodeCheckout {
                key: "CMP-1".into(),
                rationale: "rename".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    g.impact_node_update(
        &ctx,
        &dep.id,
        &cn.id,
        NodeUpdate {
            properties: obj(json!({"title": "renamed"})),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    g.impact_node_review(&ctx, &dep.id, &cn.id, domain::REVIEW_ACCEPTED, "u", "ok")
        .await
        .unwrap();
    Box::pin(g.apply(&ctx, &dep.id, "")).await.unwrap();
    let applied = g.change(&ctx, &dep.id).await.unwrap();
    accompany(
        &e,
        TriggerEvent {
            r#type: "change.applied".into(),
            change: Some(applied),
            ..Default::default()
        },
    )
    .await;
    let p = get(&e, &p.id).await;
    assert!(
        p.pending
            .as_ref()
            .is_some_and(|t| t.action == "flow/deliver"),
        "the dependency landed: the run resumes: {p:?}"
    );
}
