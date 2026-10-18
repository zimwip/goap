//! Go: triggers_test.go.

use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{Duration, Instant};

use goap_ctx::Ctx;
use goap_engine::{
    Broker, Internals, LLMExecutor, Process, STATUS_CANCELLED, STATUS_COMPLETED, STATUS_RUNNING,
    StartRequest, TriggerEvent, TriggerManager,
};
use goap_llm as llm;
use goap_methodology as methodology;
use serde_json::Value;

use crate::agents::{agents_base, agents_setup};
use crate::common::*;
use crate::reaction::note_change;
use crate::steps::staged_setup;

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn event_trigger_starts_reviewer_on_same_change() {
    let (ctx, _cancel) = ctx().with_cancel();
    let mut s = agents_base().await;
    let broker = Arc::new(Broker::new());
    s.e.events = Some(broker.clone());
    let (e, _, base, _errs) = s.share();
    let tm = TriggerManager::new(e.clone());
    tm.start(&ctx).await;
    {
        let (tm, ctx, broker) = (tm.clone(), ctx.clone(), broker.clone());
        tokio::spawn(async move { tm.watch_processes(&ctx, &broker).await });
    }
    tokio::time::sleep(Duration::from_millis(20)).await; // subscription ready

    let states = tm.states();
    assert_eq!(states.len(), 2, "expected 2 triggers, got {states:?}");
    let d = e
        .start(
            &ctx,
            StartRequest {
                methodology: "test-design".into(),
                agent: "test-designer".into(),
                baseline_id: base,
                intent: "concevoir".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let d = e.run(&ctx, &d.id).await.unwrap();
    assert_eq!(d.status, STATUS_COMPLETED, "designer");
    let mut reviewer: Option<Process> = None;
    let deadline = Instant::now() + Duration::from_secs(3);
    while Instant::now() < deadline && reviewer.is_none() {
        e.drain().await;
        let ps = e.store.list(&ctx).await.unwrap();
        reviewer = ps
            .into_iter()
            .find(|p| p.agent == "reviewer" && p.status == STATUS_COMPLETED);
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    let reviewer =
        reviewer.unwrap_or_else(|| panic!("trigger did not start the reviewer: {:?}", tm.states()));
    assert!(
        reviewer.change_id == d.change_id
            && reviewer.trigger == "test-design/reviewer/after_design"
            && reviewer.initiator.subject == "system:trigger:test-design/reviewer/after_design",
        "unexpected triggered process {reviewer:?}"
    );
    // the run whose event fired the trigger is its cause
    assert_eq!(reviewer.cause, d.id, "cause: the designer run");
    // the reviewer completion does not match the filter: no loop
    e.drain().await;
    tokio::time::sleep(Duration::from_millis(50)).await;
    let ps = e.store.list(&ctx).await.unwrap();
    let n = ps.iter().filter(|p| !p.trigger.is_empty()).count();
    assert_eq!(n, 1, "expected exactly one triggered process");
}

#[tokio::test]
async fn manual_fire_of_schedule_trigger() {
    let ctx = ctx();
    let (e, _, _errs) = agents_setup().await;
    let tm = TriggerManager::new(e.clone());
    tm.reload(&ctx).await.unwrap();
    let p = tm
        .fire(&ctx, "test-design", "test-designer", "nightly")
        .await
        .unwrap();
    assert!(
        p.status == STATUS_RUNNING && p.goal == "designed" && !p.trigger.is_empty(),
        "unexpected process {p:?}"
    );
    assert!(
        p.cause.is_empty(),
        "a manual fire has no cause, got {:?}",
        p.cause
    );
    e.drain().await;
    let p = get(&e, &p.id).await;
    assert_eq!(p.status, STATUS_COMPLETED, "scheduled run {}", p.error);
    let bb = e.graph.blackboard_in(&ctx, &p.change_id, "").await.unwrap();
    assert_eq!(
        bb.change.data.get("trigger"),
        Some(&Value::from("test-design/test-designer/nightly")),
        "change not marked with its trigger: {:?}",
        bb.change.data
    );
    let s = tm.states();
    assert!(
        s[1].fires == 1 && s[1].last_process_id == p.id,
        "state {s:?}"
    );
    assert!(
        tm.fire(&ctx, "test-design", "test-designer", "nope")
            .await
            .is_err(),
        "unknown trigger must fail"
    );
}

#[tokio::test]
async fn leader_gates_schedule_firing() {
    let ctx = ctx();
    let (e, _, _errs) = agents_setup().await;
    let tm = TriggerManager::with(e.clone(), Duration::ZERO, Some(Arc::new(|| false)));
    tm.reload(&ctx).await.unwrap();
    // reload / states must stay available on every replica regardless of leadership.
    let states = tm.states();
    assert_eq!(states.len(), 2, "expected 2 triggers, got {states:?}");
    // a non-leader replica's cron callback must not actually fire the trigger.
    Internals::cron_fire(&tm, "test-design/test-designer/nightly").await;
    for s in tm.states() {
        assert!(
            !(s.name == "nightly" && s.fires != 0),
            "non-leader replica fired a schedule trigger: {s:?}"
        );
    }
    let ps = e.store.list(&ctx).await.unwrap();
    assert!(
        ps.is_empty(),
        "non-leader replica started a process: {ps:?}"
    );
    // manual fire is unaffected by leader and must still work.
    tm.fire(&ctx, "test-design", "test-designer", "nightly")
        .await
        .unwrap();
}

/// NOTE_TRIGGER_YAML runs a writer on the change of the event each time the change is edited; a newer edit cancels
/// the run still at work (supersede, ADR 0110).
pub const NOTE_TRIGGER_YAML: &str = r#"
name: noted
version: 1.0.0
namespace: alm
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.target == "note")'}
actions:
  - {name: note, kind: llm, model: default, prompt: Write the note, effects: {noted: true}}
goals:
  - {name: noted, pre: {noted: true}}
agents:
  - name: writer
    planner: goap
    goals: [noted]
    triggers:
      - {name: on_edit, type: event, event: change.edited, target: event_change, goal: noted, enabled: true, supersede: true}
"#;

pub const NOTE_ANSWER: &str =
    r#"{"items":[{"type":"artifact","target":"note","data":{"text":"done"}}]}"#;

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn superseding_trigger_cancels_the_run_at_work() {
    let (ctx, _cancel) = ctx().with_cancel();
    let mut s = staged_setup().await;
    add_yaml(&mut s, NOTE_TRIGGER_YAML);
    let (tx, mut started) = tokio::sync::mpsc::channel::<()>(1);
    let calls = Arc::new(AtomicUsize::new(0));
    let c2 = calls.clone();
    s.e.executors.insert(
        methodology::KIND_LLM.into(),
        Arc::new(LLMExecutor {
            client: Some(llm::ClientFunc::shared(move |ctx: Ctx, _r: llm::Request| {
                let (calls, tx) = (c2.clone(), tx.clone());
                async move {
                    let first = calls.fetch_add(1, Ordering::SeqCst) == 0;
                    if first {
                        let _ = tx.send(()).await;
                        tokio::select! {
                            err = ctx.done() => return Err(llm::Error::Ctx(err)),
                            _ = tokio::time::sleep(Duration::from_secs(5)) => {
                                return Err(llm::Error::Typed { error_type: "test".into(), message: "never interrupted".into() });
                            }
                        }
                    }
                    Ok(llm::Response {
                        text: NOTE_ANSWER.into(),
                        ..Default::default()
                    })
                }
            })),
        }),
    );
    let (e, g, _, _errs) = s.share();
    let tm = TriggerManager::new(e.clone());
    tm.reload(&ctx).await.unwrap();
    let ch = note_change(&g, "write a note", "noted").await;
    let edited = || {
        let (g, tm, id, ctx) = (g.clone(), tm.clone(), ch.id.clone(), ctx.clone());
        async move {
            let bb = g.blackboard(&ctx, &id).await.unwrap();
            tm.handle(
                &ctx,
                TriggerEvent {
                    r#type: "change.edited".into(),
                    change: Some(bb.change.clone()),
                    ..Default::default()
                },
            )
            .await;
        }
    };
    edited().await;
    tokio::time::timeout(Duration::from_secs(5), started.recv())
        .await
        .expect("the first run did not start");
    edited().await;
    e.drain().await;
    let (mut cancelled, mut completed) = (0, 0);
    for p in e.store.list(&ctx).await.unwrap() {
        if p.change_id != ch.id || p.trigger.is_empty() {
            continue;
        }
        if p.status == STATUS_CANCELLED {
            cancelled += 1;
        } else if p.status == STATUS_COMPLETED {
            completed += 1;
        } else {
            panic!("run {} is {}", p.id, p.status);
        }
    }
    let n = calls.load(Ordering::SeqCst);
    assert!(
        cancelled == 1 && completed == 1 && n == 2,
        "cancelled {cancelled}, completed {completed}, {n} model calls: the first run is cancelled, the second one writes"
    );
}
