//! Go: persistent_test.go.

use std::collections::BTreeMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicI64, Ordering};

use goap_assess as assess;
use goap_ctx::Ctx;
use goap_domain::{ChangeID, HeaderValue};
use goap_engine::{Engine, LLMExecutor, Process, STATUS_COMPLETED, StartRequest, TriggerEvent};
use goap_graph::{ChangePatch, Graph};
use goap_llm as llm;
use goap_methodology::{self as methodology, Persistence};
use parking_lot::Mutex;
use serde_json::{Map, Value, json};

use crate::common::*;
use crate::companion::accompany;
use crate::reaction::{RATED_YAML, note_change};
use crate::steps::staged_setup;

/// persistent_rated_yaml is RATED_YAML with a persistent rater (ADR 0112): one instance per change applies the method.
fn persistent_rated_yaml() -> String {
    RATED_YAML.replacen(
        "roles: {responsible: analyst}}",
        "roles: {responsible: analyst}, persistent: {scope: change, memory: 2}}",
        1,
    )
}

fn llm_exec(c: Arc<dyn llm::Client>) -> Arc<LLMExecutor> {
    Arc::new(LLMExecutor { client: Some(c) })
}

/// edit sets the intent of the change and hands the edit to the engine.
async fn edit(e: &Arc<Engine>, g: &Graph, id: &ChangeID, text: &str) {
    let ctx = ctx();
    let from = g.blackboard(&ctx, id).await.unwrap().change.intent;
    g.update_change(
        &ctx,
        id,
        &ChangePatch {
            intent: Some(text.into()),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let bb = g.blackboard(&ctx, id).await.unwrap();
    accompany(
        e,
        TriggerEvent {
            r#type: "change.edited".into(),
            change: Some(bb.change.clone()),
            fields: BTreeMap::from([(
                "intent".to_string(),
                HeaderValue {
                    from: Value::from(from),
                    to: Value::from(text),
                },
            )]),
            ..Default::default()
        },
    )
    .await;
}

async fn created(e: &Arc<Engine>, g: &Graph, id: &ChangeID) {
    let bb = g.blackboard(&ctx(), id).await.unwrap();
    accompany(
        e,
        TriggerEvent {
            r#type: "change.created".into(),
            change: Some(bb.change.clone()),
            ..Default::default()
        },
    )
    .await;
}

#[tokio::test]
async fn persistent_rater_keeps_its_instance_and_conversation() {
    let ctx = ctx();
    let mut s = staged_setup().await;
    let c = add_yaml(&mut s, &persistent_rated_yaml());
    let ag = c.agent("rating");
    assert!(
        ag.is_some_and(|a| a.persistent.is_some() && Persistence::turns(a.persistent.as_ref()) == 2),
        "the agent of the method is persistent: {:?}",
        ag.map(|a| &a.persistent)
    );
    let ch = note_change(&s.g, "write a note", "rated").await;
    let reqs: Arc<Mutex<Vec<llm::Request>>> = Arc::default();
    {
        let (reqs, g, id) = (reqs.clone(), s.g.clone(), ch.id.clone());
        s.e.executors.insert(
            methodology::KIND_LLM.into(),
            llm_exec(llm::ClientFunc::shared(move |_ctx: Ctx, r: llm::Request| {
                let (reqs, g, id) = (reqs.clone(), g.clone(), id.clone());
                async move {
                    let bb = g.blackboard(&crate::common::ctx(), &id).await.unwrap();
                    let fp = assess::fingerprint_of(&bb, assess::SUBJECT_DESCRIPTION);
                    let n = {
                        let mut reqs = reqs.lock();
                        reqs.push(r);
                        reqs.len()
                    };
                    let text = json!({"items": [{"type": "assessment", "data": {"key": "description", "score": 3,
                        "verdict": "clear", "rated": fp, "reply": format!("answer {n}")}}]});
                    Ok(llm::Response {
                        text: text.to_string(),
                        ..Default::default()
                    })
                }
            })),
        );
    }
    let (e, g, _, _errs) = s.share();
    created(&e, &g, &ch.id).await;
    edit(&e, &g, &ch.id, "write the release note of 1.2").await;
    edit(
        &e,
        &g,
        &ch.id,
        "write the release note of 1.2, for the users",
    )
    .await;

    let rs: Vec<Process> = e
        .store
        .list(&ctx)
        .await
        .unwrap()
        .into_iter()
        .filter(|p| p.change_id == ch.id && p.agent == "rating")
        .collect();
    assert_eq!(rs.len(), 1, "one instance of the rater for the change");
    let p = &rs[0];
    assert!(
        p.instance == format!("rated/rating@{}", ch.id)
            && p.runs == 2
            && p.status == STATUS_COMPLETED
            && !p.closed,
        "instance {:?} runs {} status {} closed {}",
        p.instance,
        p.runs,
        p.status,
        p.closed
    );
    let reqs = reqs.lock().clone();
    assert_eq!(reqs.len(), 3, "model calls");
    // the first call starts the conversation; the next ones get the earlier exchanges back, the last two only
    assert!(
        reqs[0].messages.len() == 1 && !reqs[0].system.contains("persistent agent"),
        "first call: {} messages",
        reqs[0].messages.len()
    );
    assert!(
        reqs[1].messages.len() == 3
            && reqs[1].messages[1].content.contains("answer 1")
            && reqs[1].system.contains("persistent agent"),
        "second call: {:?}",
        reqs[1].messages
    );
    assert!(
        reqs[2].messages.len() == 5
            && reqs[2].messages[3].content.contains("answer 2")
            && reqs[2].messages[2].content.contains("earlier execution 1"),
        "third call: {:?}",
        reqs[2].messages
    );
    assert!(
        p.memory.len() == 2 && p.memory[1].run == 2 && p.memory[1].event == "change.edited",
        "memory {:?}",
        p.memory
    );
}

/// NOTE_YAML has a persistent agent on its change, closed once a final note is written.
const NOTE_YAML: &str = r#"
name: notes
version: 1.0.0
namespace: alm
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.target == "note" && a.data.n == vars.event.n)'}
  - {name: final, expr: 'artifacts.exists(a, a.target == "note" && a.data.final == true)'}
actions:
  - {name: note, kind: llm, model: default, prompt: Write the note, effects: {noted: true}}
goals:
  - {name: noted, pre: {noted: true}}
agents:
  - {name: writer, planner: goap, goals: [noted], persistent: {until: {final: true}}}
"#;

#[tokio::test]
async fn persistent_instance_closes_until() {
    let ctx = ctx();
    let mut s = staged_setup().await;
    add_yaml(&mut s, NOTE_YAML);
    let ch =
        s.g.create_change(
            &ctx,
            goap_graph::NewChange {
                title: "Notes".into(),
                intent: "notes".into(),
                methodology: "notes".into(),
                namespace: "alm".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    // Go swapped the client per run; here one client answers for the run number it is given
    let current = Arc::new(AtomicI64::new(0));
    {
        let current = current.clone();
        s.e.executors.insert(
            methodology::KIND_LLM.into(),
            llm_exec(llm::ClientFunc::shared(move |_ctx: Ctx, _r: llm::Request| {
                let n = current.load(Ordering::SeqCst);
                async move {
                    let text = json!({"items": [{"type": "artifact", "target": "note", "data": {"n": n, "final": n == 2}}]});
                    Ok(llm::Response {
                        text: text.to_string(),
                        ..Default::default()
                    })
                }
            })),
        );
    }
    let (e, _, _, _errs) = s.share();
    let run = |n: i64| {
        let (e, id, current) = (e.clone(), ch.id.clone(), current.clone());
        async move {
            let ctx = crate::common::ctx();
            current.store(n, Ordering::SeqCst);
            let p = e
                .start(
                    &ctx,
                    StartRequest {
                        methodology: "notes".into(),
                        agent: "writer".into(),
                        goal: "noted".into(),
                        change_id: id,
                        vars: obj(json!({"event": {"type": "manual", "n": n}})),
                        ..Default::default()
                    },
                )
                .await
                .unwrap();
            let p = e.run(&ctx, &p.id).await.unwrap();
            assert_eq!(p.status, STATUS_COMPLETED, "run {n}: {}", p.error);
            p
        }
    };
    let first = run(1).await;
    let second = run(2).await;
    let third = run(3).await;
    assert!(
        first.id == second.id && second.runs == 1 && second.closed,
        "the second execution goes to the instance, and its final note closes it: {} {} runs {} closed {}",
        first.id,
        second.id,
        second.runs,
        second.closed
    );
    assert!(
        third.id != second.id && third.instance == second.instance,
        "after closing, a new instance: {} {}",
        third.id,
        third.instance
    );
}

#[test]
fn persistence_scope() {
    let p = Persistence {
        scope: "event.process.methodology".into(),
        ..Default::default()
    };
    let ev: Map<String, Value> = obj(json!({"process": {"methodology": "sdlc"}}));
    assert_eq!(Persistence::resolve(Some(&p), "C1", &ev), "sdlc", "scope");
    assert_eq!(
        Persistence::resolve(Some(&p), "C1", &Map::new()),
        "",
        "no event, no scope"
    );
    assert_eq!(
        Persistence::resolve(Some(&Persistence::default()), "C1", &Map::new()),
        "C1",
        "default scope is the change"
    );
    let src = NOTE_YAML.replacen(
        "until: {final: true}",
        "scope: process, until: {nope: true}, memory: 50",
        1,
    );
    let m = methodology::parse(src.as_bytes()).unwrap();
    let (_, issues) = m.compile_lenient();
    let paths: Vec<_> = issues.iter().map(|is| is.path.clone()).collect();
    let got = paths.join(" ");
    for want in [
        "agents[0].persistent.scope",
        "agents[0].persistent.until.nope",
        "agents[0].persistent.memory",
    ] {
        assert!(got.contains(want), "issues {paths:?}, want {want}");
    }
}

/// A persistent rater given its earlier exchanges back copies the fingerprint of its first rating in the next ones:
/// the engine stamps what was rated with the text the action read, so every new rating counts.
#[tokio::test]
async fn persistent_rater_rates_the_current_text() {
    let ctx = ctx();
    let mut s = staged_setup().await;
    add_yaml(&mut s, &persistent_rated_yaml());
    let ch = note_change(&s.g, "write a note", "rated").await;
    let state: Arc<Mutex<(String, i64)>> = Arc::default();
    {
        let (state, g, id) = (state.clone(), s.g.clone(), ch.id.clone());
        s.e.executors.insert(
            methodology::KIND_LLM.into(),
            llm_exec(llm::ClientFunc::shared(move |_ctx: Ctx, _r: llm::Request| {
                let (state, g, id) = (state.clone(), g.clone(), id.clone());
                async move {
                    let empty = state.lock().0.is_empty();
                    if empty {
                        let bb = g.blackboard(&crate::common::ctx(), &id).await.unwrap();
                        state.lock().0 = assess::fingerprint_of(&bb, assess::SUBJECT_DESCRIPTION);
                    }
                    let (stale, n) = {
                        let mut st = state.lock();
                        st.1 += 1;
                        (st.0.clone(), st.1)
                    };
                    let text = json!({"items": [{"type": "assessment", "data": {"key": "description",
                        "score": n + 1, "verdict": "clear", "rated": stale}}]});
                    Ok(llm::Response {
                        text: text.to_string(),
                        ..Default::default()
                    })
                }
            })),
        );
    }
    let (e, g, _, _errs) = s.share();
    created(&e, &g, &ch.id).await;
    // Go kept the intent read before the first edit as the from of both edits; the from only names the field here
    for text in [
        "write the release note of 1.2",
        "write the release note of 1.2, for the users",
    ] {
        edit(&e, &g, &ch.id, text).await;
    }
    let bb = g.blackboard(&ctx, &ch.id).await.unwrap();
    let a = assess::of(&bb, assess::SUBJECT_DESCRIPTION);
    let n = state.lock().1;
    assert!(
        a.as_ref()
            .is_some_and(|a| a.current && a.score == 4 && a.versions == 3)
            && n == 3,
        "the third rating is about the current description: {a:?} ({n} calls)"
    );
    for p in e.store.list(&ctx).await.unwrap() {
        if p.change_id == ch.id && p.agent == "rating" {
            assert!(
                p.status == STATUS_COMPLETED
                    && !p.disabled.get("rate").copied().unwrap_or_default(),
                "the rater completed with its model: {} disabled {:?}",
                p.status,
                p.disabled
            );
        }
    }
}
