//! Go: reaction_test.go.

use std::collections::BTreeMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

use goap_assess as assess;
use goap_ctx::Ctx;
use goap_domain::{self as domain, ChangeItem, HeaderValue};
use goap_engine::{
    Engine, ItemInput, Process, STATUS_COMPLETED, STATUS_WAITING, StartRequest, TASK_CONDITION,
    TriggerEvent, TriggerManager, declared_events,
};
use goap_graph::{ChangePatch, Graph, NewChange};
use goap_llm as llm;
use goap_methodology as methodology;
use serde_json::{Value, json};

use crate::common::*;
use crate::companion::{accompany, companion_of};
use crate::steps::staged_setup;

/// DESCRIBED_YAML reviews the description of its changes with a reactive step (ADR 0110): the step runs on the
/// change when it is created, when its description is edited and when a person comments on the rating.
pub const DESCRIBED_YAML: &str = r#"
name: described
version: 1.0.0
namespace: alm
imports: [assessments]
actions:
  - {name: rate, kind: human, description: Rate the description, effects: {description_rated: true}}
  - {name: write, kind: human, description: Write the note, pre: {description_accepted: true}, effects: {noted: true}}
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.target == "note")'}
processes:
  - name: delivery
    steps:
      - {name: note, action: write}
  - name: review
    steps:
      - name: rate
        action: rate
        on:
          - {event: change.created}
          - {event: change.edited, filter: '"intent" in event.fields || "title" in event.fields'}
          - {event: change.item_added, filter: 'event.items.exists(i, i.type == "comment" && i.data.about == "description")'}
"#;

pub async fn note_change(g: &Graph, intent: &str, methodology: &str) -> domain::Change {
    g.create_change(
        &ctx(),
        NewChange {
            title: "Note".into(),
            intent: intent.into(),
            methodology: methodology.into(),
            namespace: "alm".into(),
            project_id: TEST_PROJECT.into(),
            ..Default::default()
        },
    )
    .await
    .unwrap()
}

async fn one_companion(e: &Engine, ch: &domain::ChangeID) -> Process {
    let cs = companion_of(e, ch).await;
    assert_eq!(cs.len(), 1, "one run of the reaction per change");
    cs.into_iter().next().unwrap()
}

#[tokio::test]
async fn reactive_step_reviews_the_description() {
    let ctx = ctx();
    let mut s = staged_setup().await;
    let c = compile_yaml(DESCRIBED_YAML);
    let rs = c.reactions();
    assert!(
        rs.len() == 1
            && rs[0].activity == "review/rate"
            && rs[0].agent == "review"
            && rs[0].goal == "review/rate"
            && rs[0].owns("review/rate"),
        "reactions {rs:?}"
    );
    s.add(c);
    let (e, g, _, _errs) = s.share();

    let ch = note_change(&g, "write a note", "described").await;
    let event =
        |typ: &'static str, items: Vec<ChangeItem>, fields: BTreeMap<String, HeaderValue>| {
            let (e, g, id) = (e.clone(), g.clone(), ch.id.clone());
            async move {
                let bb = g.blackboard(&crate::common::ctx(), &id).await.unwrap();
                accompany(
                    &e,
                    TriggerEvent {
                        r#type: typ.into(),
                        change: Some(bb.change.clone()),
                        items,
                        fields,
                        ..Default::default()
                    },
                )
                .await;
            }
        };
    let rate = |score: i64, reply: &'static str| {
        let (e, g, id) = (e.clone(), g.clone(), ch.id.clone());
        async move {
            let ctx = crate::common::ctx();
            let bb = g.blackboard(&ctx, &id).await.unwrap();
            let text = assess::text(&bb, assess::SUBJECT_DESCRIPTION).unwrap_or_default();
            let p = one_companion(&e, &id).await;
            e.submit(
                &ctx,
                &p.id,
                vec![ItemInput {
                    r#type: assess::TYPE_ASSESSMENT.to_string(),
                    data: obj(
                        json!({"key": "description", "score": score, "verdict": "unclear",
                        "rated": text, "reply": reply}),
                    ),
                    ..Default::default()
                }],
            )
            .await
            .unwrap();
            let p = e.run(&ctx, &p.id).await.unwrap();
            assert_eq!(p.status, STATUS_COMPLETED, "rated: {p:?}");
        }
    };
    let waiting = |why: &'static str| {
        let (e, id) = (e.clone(), ch.id.clone());
        async move {
            let p = one_companion(&e, &id).await;
            assert!(
                p.status == STATUS_WAITING
                    && p.pending
                        .as_ref()
                        .is_some_and(|t| t.action == "review/rate"),
                "{why}: the rater runs again, got {p:?}"
            );
        }
    };

    // the change is created: the step rates its description
    event("change.created", vec![], BTreeMap::new()).await;
    let p = one_companion(&e, &ch.id).await;
    assert!(
        p.trigger == "companion:described/review/rate" && p.goal == "review/rate",
        "reaction run {p:?}"
    );
    waiting("created").await;
    rate(2, "say what the note is about").await;
    let run = one_companion(&e, &ch.id).await.id;

    // the person answers in the thread: the same run is woken and rates again
    let items = g
        .add_items(
            &ctx,
            &ch.id,
            vec![ChangeItem {
                r#type: assess::TYPE_COMMENT,
                status: domain::ITEM_PROPOSED,
                produced_by: "alice".into(),
                data: obj(json!({"about": "description", "text": "it is the release note"})),
                ..Default::default()
            }],
        )
        .await
        .unwrap();
    event("change.item_added", items, BTreeMap::new()).await;
    waiting("commented").await;
    rate(2, "then say so in the description").await;
    assert_eq!(
        one_companion(&e, &ch.id).await.id,
        run,
        "the reaction keeps its run"
    );

    // an edit of the goal is not one of its events; an edit of the description is
    let hv = |from: &str, to: &str| HeaderValue {
        from: Value::from(from),
        to: Value::from(to),
    };
    event(
        "change.edited",
        vec![],
        BTreeMap::from([("goal".to_string(), hv("a", "b"))]),
    )
    .await;
    let p = one_companion(&e, &ch.id).await;
    assert_eq!(p.status, STATUS_COMPLETED, "filtered out");
    let intent = "write the release note of 1.2";
    g.update_change(
        &ctx,
        &ch.id,
        &ChangePatch {
            intent: Some(intent.into()),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    event(
        "change.edited",
        vec![],
        BTreeMap::from([("intent".to_string(), hv("write a note", intent))]),
    )
    .await;
    waiting("edited").await;
    rate(4, "clear").await;
    let bb = g.blackboard(&ctx, &ch.id).await.unwrap();
    let a = assess::of(&bb, "description");
    assert!(
        a.as_ref()
            .is_some_and(|a| a.current && a.answered && a.score == 4 && a.versions == 3),
        "assessment {a:?}"
    );
}

#[test]
fn reactive_actions_and_their_checks() {
    let c = compile_yaml(
        r#"
name: nudging
version: 1.0.0
namespace: alm
conditions:
  - {name: nudged, expr: 'artifacts.exists(a, a.target == "nudge")'}
actions:
  - {name: nudge, kind: human, effects: {nudged: true}, on: [{event: change.edited}]}
processes:
  - name: main
    steps:
      - {name: one, action: nudge}
"#,
    );
    let rs = c.reactions();
    assert!(
        rs.len() == 1 && rs[0].kind == methodology::REACT_ACTION && rs[0].agent == "on.nudge",
        "reactions {rs:?}"
    );
    let g = c.goal("on.nudge");
    assert!(
        g.as_ref()
            .is_some_and(|g| g.pre.get("nudged") == Some(&true)),
        "goal of a reactive action: {g:?}"
    );
    let ag = c.agent("on.nudge");
    assert!(
        ag.is_some_and(|ag| c.agent_actions(ag).len() == 1),
        "agent of a reactive action: {ag:?}"
    );
    for (name, src) in [
        (
            "no effects",
            r#"
name: bad
version: 1.0.0
namespace: alm
actions:
  - {name: nudge, kind: human, on: [{event: change.edited}]}
goals: [{name: g, pre: {x: true}}]
conditions: [{name: x, expr: "true"}]
"#,
        ),
        (
            "unknown event",
            r#"
name: bad
version: 1.0.0
namespace: alm
conditions: [{name: x, expr: "true"}]
actions: [{name: a, kind: human, effects: {x: true}}]
processes: [{name: p, on: [{event: change.nothing}], steps: [{name: s, action: a}]}]
"#,
        ),
        (
            "transverse",
            r#"
name: bad
version: 1.0.0
appliesTo: [staged]
conditions: [{name: x, expr: "true"}]
actions: [{name: a, kind: human, effects: {x: true}}]
processes: [{name: p, steps: [{name: s, action: a, on: [{event: change.edited}]}]}]
"#,
        ),
    ] {
        refused(name, src);
    }
}

/// refused checks that a methodology is refused at validation.
pub fn refused(name: &str, src: &str) {
    let m = methodology::parse(src.as_bytes()).unwrap();
    assert!(m.validate().has_errors(), "{name}: refused expected");
}

/// EVENTFUL_YAML declares an event of its own (ADR 0110): the action writing the spec raises it, a reactive action
/// follows it.
const EVENTFUL_YAML: &str = r#"
name: eventful
version: 1.0.0
namespace: alm
events:
  - {name: spec.ready, description: The spec is written}
conditions:
  - {name: written, expr: 'artifacts.exists(a, a.target == "spec")'}
  - {name: notified, expr: 'artifacts.exists(a, a.target == "notice")'}
actions:
  - {name: write, kind: human, effects: {written: true}, emits: [spec.ready]}
  - {name: notify, kind: human, effects: {notified: true}, on: [{event: spec.ready}]}
processes:
  - name: delivery
    steps:
      - {name: spec, action: write}
"#;

#[tokio::test]
async fn declared_events_are_emitted_and_followed() {
    let ctx = ctx();
    let mut s = staged_setup().await;
    add_yaml(&mut s, EVENTFUL_YAML);
    let (e, g, _, _errs) = s.share();
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "eventful".into(),
                goal: "delivery".into(),
                intent: "write the spec".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert_eq!(p.status, STATUS_WAITING, "the spec step waits: {p:?}");
    e.submit(
        &ctx,
        &p.id,
        vec![artifact("spec", json!({"text": "the spec"}))],
    )
    .await
    .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert_eq!(p.status, STATUS_COMPLETED, "delivered: {p:?}");
    let bb = g.blackboard(&ctx, &p.change_id).await.unwrap();
    let signals: Vec<ChangeItem> = bb
        .change
        .items
        .iter()
        .filter(|it| it.r#type == domain::TYPE_SIGNAL)
        .cloned()
        .collect();
    assert!(
        signals.len() == 1
            && signals[0].target == "spec.ready"
            && signals[0].data.get("step") == Some(&Value::from("delivery/spec")),
        "the action raises its event: {signals:?}"
    );

    // the item added raises the declared event, which wakes the reactive action
    let ev = TriggerEvent {
        r#type: "change.item_added".into(),
        change: Some(bb.change.clone()),
        items: signals,
        ..Default::default()
    };
    let own = declared_events(&ev);
    assert!(
        own.len() == 1 && own[0].r#type == "spec.ready" && own[0].items.len() == 1,
        "declared events {own:?}"
    );
    TriggerManager::new(e.clone()).handle(&ctx, ev).await;
    e.drain().await;
    let cs = companion_of(&e, &p.change_id).await;
    assert!(
        cs.len() == 1
            && cs[0].trigger == "companion:eventful/notify"
            && cs[0].status == STATUS_WAITING,
        "the reaction to the declared event: {cs:?}"
    );

    // a signal of a standard name raises nothing more, an undeclared event is refused at compile
    let own = declared_events(&TriggerEvent {
        r#type: "change.item_added".into(),
        items: vec![ChangeItem {
            r#type: domain::TYPE_SIGNAL,
            target: "change.applied".into(),
            ..Default::default()
        }],
        ..Default::default()
    });
    assert!(
        own.is_empty(),
        "a standard name is not a declared event: {own:?}"
    );
    for (name, src) in [
        (
            "undeclared",
            "name: bad\nversion: 1.0.0\nnamespace: alm\nconditions: [{name: x, expr: 'true'}]\nactions: [{name: a, kind: human, effects: {x: true}, on: [{event: spec.ready}]}]\n",
        ),
        (
            "reserved",
            "name: bad\nversion: 1.0.0\nnamespace: alm\nevents: [{name: change.ready}]\nconditions: [{name: x, expr: 'true'}]\nactions: [{name: a, kind: human, effects: {x: true}}]\ngoals: [{name: g, pre: {x: true}}]\n",
        ),
        (
            "emits",
            "name: bad\nversion: 1.0.0\nnamespace: alm\nconditions: [{name: x, expr: 'true'}]\nactions: [{name: a, kind: human, effects: {x: true}, emits: [nope]}]\ngoals: [{name: g, pre: {x: true}}]\n",
        ),
    ] {
        refused(name, src);
    }
}

/// RATED_YAML rates the description the way sdlc does (ADR 0110): a goap method whose llm action comes first and
/// whose human action is the fallback, every activity carried out by an agent.
pub const RATED_YAML: &str = r#"
name: rated
version: 1.0.0
namespace: alm
imports: [assessments]
roles: [{name: analyst}]
actions:
  - {name: rate, kind: llm, model: default, prompt: Rate the description, effects: {description_rated: true}, cost: 1}
  - {name: rate_by_hand, kind: human, effects: {description_rated: true}, cost: 6}
methods:
  - {name: rating, for: rating, planner: goap, actions: [rate, rate_by_hand], done: {description_rated: true}, roles: {responsible: analyst}}
processes:
  - name: review
    steps:
      - {name: rate, method: rating, on: [{event: change.created}, {event: change.edited}]}
"#;

/// fingerprinting answers with answer, its FP replaced by the fingerprint of the description of the change.
pub fn fingerprinting(
    g: &Arc<Graph>,
    id: &domain::ChangeID,
    answer: &'static str,
    calls: Option<Arc<AtomicUsize>>,
) -> Arc<dyn llm::Client> {
    let (g, id) = (g.clone(), id.clone());
    llm::ClientFunc::shared(move |_ctx: Ctx, _r: llm::Request| {
        let (g, id, calls) = (g.clone(), id.clone(), calls.clone());
        async move {
            if let Some(c) = calls {
                c.fetch_add(1, Ordering::SeqCst);
            }
            let bb = g.blackboard(&crate::common::ctx(), &id).await.unwrap();
            let fp = assess::fingerprint_of(&bb, assess::SUBJECT_DESCRIPTION);
            Ok(llm::Response {
                text: answer.replacen("FP", &fp, 1),
                ..Default::default()
            })
        }
    })
}

#[tokio::test]
async fn description_rating_falls_back_to_a_person() {
    for (name, answer) in [
        (
            "model",
            r#"{"items":[{"type":"assessment","data":{"key":"description","score":4,"verdict":"clear","rated":"FP"}}]}"#,
        ),
        // the model rates nothing: its action fails, is disabled, the person rates
        ("person", r#"{"items":[]}"#),
    ] {
        let ctx = ctx();
        let mut s = staged_setup().await;
        let c = compile_yaml(RATED_YAML);
        let rs = c.reactions();
        assert!(
            rs.len() == 1 && rs[0].owns("rate") && rs[0].owns("rate_by_hand"),
            "the reaction owns the actions of the method it runs: {rs:?}"
        );
        s.add(c);
        let ch = note_change(&s.g, "write the release note of 1.2", "rated").await;
        s.llm(fingerprinting(&s.g, &ch.id, answer, None));
        let (e, g, _, _errs) = s.share();
        let bb = g.blackboard(&ctx, &ch.id).await.unwrap();
        accompany(
            &e,
            TriggerEvent {
                r#type: "change.created".into(),
                change: Some(bb.change.clone()),
                ..Default::default()
            },
        )
        .await;
        let mut waiting: Option<Process> = None;
        for p in e.store.list(&ctx).await.unwrap() {
            if p.change_id == ch.id && p.status == STATUS_WAITING && !p.parent_id.is_empty() {
                waiting = Some(p); // the agent of the method, a sub-agent of the reaction's run
            }
        }
        let bb = g.blackboard(&ctx, &ch.id).await.unwrap();
        let a = assess::of(&bb, assess::SUBJECT_DESCRIPTION);
        match name {
            "model" => assert!(
                a.as_ref().is_some_and(|a| a.current && a.by == "rate") && waiting.is_none(),
                "{name}: the model rates first: {a:?}, waiting {waiting:?}"
            ),
            _ => assert!(
                a.is_none()
                    && waiting.as_ref().is_some_and(|w| w
                        .pending
                        .as_ref()
                        .is_some_and(|t| t.action == "rate_by_hand")
                        && w.disabled.get("rate") == Some(&true)),
                "{name}: the person rates when the model cannot: {waiting:?}"
            ),
        }
    }
}

/// FRAMED_YAML reviews the description within its framing (ADR 0130): the review step is declared, automatic, and
/// rates the description by itself; the run of the process waits for its rating, then a person confirms the framing.
const FRAMED_YAML: &str = r#"
name: framed
version: 1.0.0
namespace: alm
imports: [assessments]
actions:
  - {name: rate, kind: llm, model: default, prompt: Rate the description, effects: {description_rated: true}, cost: 1}
  - {name: write, kind: human, description: Write the note, effects: {noted: true}}
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.target == "note")'}
methods:
  - {name: rating, for: rating, planner: goap, actions: [rate], done: {description_rated: true}}
processes:
  - name: delivery
    steps:
      - name: framing
        steps:
          - {name: review, method: rating, review: {subject: description, automatic: true}}
          - {name: confirmation, description: A person confirms the framing, pre: {description_rated: true}}
      - {name: note, action: write, pre: {"step:delivery/framing/confirmation": true, description_accepted: true}}
"#;

#[tokio::test]
async fn automatic_review_of_the_framing() {
    let ctx = ctx();
    let mut s = staged_setup().await;
    let c = compile_yaml(FRAMED_YAML);
    let rs = c.reactions();
    assert!(
        rs.len() == 1
            && rs[0].activity == "delivery/framing/review"
            && rs[0].agent == "delivery"
            && rs[0].on.len() == 3
            && rs[0].on[1].supersede
            && rs[0].owns("rate"),
        "the automatic review reacts to the events of its subject: {rs:?}"
    );
    let step = c.automatic_review("delivery/framing/review");
    assert_eq!(
        step,
        Some("delivery/framing/review"),
        "the action of the review step"
    );
    let view = c.view();
    let v = view.process("delivery");
    assert!(
        v.is_some_and(|v| v.steps[0].steps[0]
            .review
            .as_ref()
            .is_some_and(|r| r.automatic)),
        "the view shows the review: {v:?}"
    );
    s.add(c);
    let ch = note_change(&s.g, "write the release note of 1.2", "framed").await;
    let calls = Arc::new(AtomicUsize::new(0));
    s.llm(fingerprinting(
        &s.g,
        &ch.id,
        r#"{"items":[{"type":"assessment","data":{"key":"description","score":4,"verdict":"clear","rated":"FP"}}]}"#,
        Some(calls.clone()),
    ));
    let (e, g, _, _errs) = s.share();
    let calls_now = || calls.load(Ordering::SeqCst);

    // the delivery run does not rate the description itself: it waits for the review
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "framed".into(),
                agent: "delivery".into(),
                goal: "delivery".into(),
                change_id: ch.id.clone(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_WAITING
            && p.pending.as_ref().is_some_and(|t| t.kind == TASK_CONDITION
                && t.conditions.iter().any(|c| c == "description_rated"))
            && calls_now() == 0,
        "the run waits for the review: {p:?}, {} calls",
        calls_now()
    );

    // the change created starts the review by itself, without anyone launching it
    let bb = g.blackboard(&ctx, &ch.id).await.unwrap();
    accompany(
        &e,
        TriggerEvent {
            r#type: "change.created".into(),
            change: Some(bb.change.clone()),
            ..Default::default()
        },
    )
    .await;
    let cs = companion_of(&e, &ch.id).await;
    assert!(
        cs.len() == 1
            && cs[0].trigger == "companion:framed/delivery/framing/review"
            && cs[0].status == STATUS_COMPLETED
            && calls_now() == 1,
        "the review ran by itself: {cs:?}, {} calls",
        calls_now()
    );
    let bb = g.blackboard(&ctx, &ch.id).await.unwrap();
    let a = assess::of(&bb, assess::SUBJECT_DESCRIPTION);
    assert!(
        a.as_ref().is_some_and(|a| a.current && a.score == 4),
        "assessment {a:?}"
    );

    // the review completed, the delivery run goes on to the person's confirmation of the framing: the rating
    // advises, a person decides
    accompany(
        &e,
        TriggerEvent {
            r#type: "process.completed".into(),
            process: Some(cs[0].clone()),
            ..Default::default()
        },
    )
    .await;
    let p = get(&e, &p.id).await;
    assert!(
        p.status == STATUS_WAITING
            && p.pending
                .as_ref()
                .is_some_and(|t| t.action == "delivery/framing/confirmation")
            && calls_now() == 1,
        "a person confirms the framing: {p:?}, {} calls",
        calls_now()
    );

    for (name, src) in [
        (
            "subject",
            "name: bad\nversion: 1.0.0\nnamespace: alm\nconditions: [{name: x, expr: 'true'}]\nactions: [{name: a, kind: human, effects: {x: true}}]\nprocesses: [{name: p, steps: [{name: s, action: a, review: {subject: everything}}]}]\n",
        ),
        (
            "manual",
            "name: bad\nversion: 1.0.0\nnamespace: alm\nprocesses: [{name: p, steps: [{name: s, review: {subject: description, automatic: true}}]}]\n",
        ),
        (
            "no events",
            "name: bad\nversion: 1.0.0\nnamespace: alm\nconditions: [{name: x, expr: 'true'}]\nactions: [{name: a, kind: human, effects: {x: true}}]\nprocesses: [{name: p, steps: [{name: s, action: a, done: {x: true}, review: {subject: 'impact:REQ-1', automatic: true}}]}]\n",
        ),
    ] {
        refused(name, src);
    }
}
