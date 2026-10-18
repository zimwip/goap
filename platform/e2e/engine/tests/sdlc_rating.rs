//! Go: sdlc_rating_test.go.

use std::sync::{Arc, OnceLock};
use std::time::Duration;

use goap_assess as assess;
use goap_ctx::Ctx;
use goap_domain::{self as domain, ChangeEvent};
use goap_engine::{
    Broker, Engine, EventingGraph, LLMExecutor, OnEvent, STATUS_CANCELLED, STATUS_COMPLETED,
    StartRequest, TriggerManager, trigger_event_of,
};
use goap_graph::{ChangePatch, NewChange};
use goap_llm as llm;
use goap_methodology as methodology;
use parking_lot::Mutex;
use serde_json::Value;

use crate::common::*;
use crate::sdlc::{SdlcSetup, dev_ctx, sdlc_setup};

/// wired is the engine of sdlc_setup wired like goap-dev: the graph's events and the process events reach the
/// triggers. It returns the shared engine and graph, the head of alm and the cancel of the watch.
async fn wired(
    s: SdlcSetup,
    client: Arc<dyn llm::Client>,
) -> (
    Arc<Engine>,
    Arc<goap_graph::Graph>,
    domain::BaselineID,
    goap_ctx::CancelHandle,
) {
    let SdlcSetup { mut e, g, base, .. } = s;
    let broker = Arc::new(Broker::new());
    e.events = Some(broker.clone());
    let tm: Arc<OnceLock<Arc<TriggerManager>>> = Arc::default();
    let on_event: OnEvent = {
        let tm = tm.clone();
        Arc::new(move |ctx: &Ctx, ev: ChangeEvent| {
            if let Some(tm) = tm.get().cloned() {
                let ctx = ctx.clone();
                tokio::spawn(async move { tm.handle(&ctx, trigger_event_of(ev)).await });
            }
        })
    };
    e.graph = Arc::new(EventingGraph::new(g.clone(), Some(on_event)));
    e.executors.insert(
        methodology::KIND_LLM.into(),
        Arc::new(LLMExecutor {
            client: Some(client),
        }),
    );
    let e = Arc::new(e);
    let m = TriggerManager::new(e.clone());
    let _ = tm.set(m.clone());
    let (wctx, cancel) = ctx().with_cancel();
    tokio::spawn(async move { m.watch_processes(&wctx, &broker).await });
    tokio::time::sleep(Duration::from_millis(20)).await;
    (e, g, base, cancel)
}

fn installments(base: &domain::BaselineID) -> NewChange {
    NewChange {
        title: "Installments".into(),
        intent: "Allow payment in 3 installments".into(),
        methodology: "sdlc".into(),
        namespace: "alm".into(),
        baseline_id: base.clone(),
        project_id: TEST_PROJECT.into(),
        ..Default::default()
    }
}

async fn settle(e: &Engine, rounds: usize) {
    for _ in 0..rounds {
        tokio::time::sleep(Duration::from_millis(150)).await;
        Box::pin(e.drain()).await;
    }
}

/// The description of a new sdlc change is rated once, by nobody's request but its creation: no run starts again on
/// its own. A rating that misses its effect (here, about the wrong subject) is not tried again by itself (ADR 0112
/// §6): the rating falls to a person.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn sdlc_description_is_rated_once() {
    for key in ["description", "intent"] {
        rated_once(key).await;
    }
}

async fn rated_once(key: &'static str) {
    let ctx = dev_ctx();
    let s = Box::pin(sdlc_setup()).await;
    let prompts: Arc<Mutex<Vec<String>>> = Arc::default();
    let client = {
        let prompts = prompts.clone();
        llm::ClientFunc::shared(move |_ctx: Ctx, r: llm::Request| {
            let prompts = prompts.clone();
            async move {
                prompts.lock().push(
                    r.messages
                        .last()
                        .map(|m| m.content.clone())
                        .unwrap_or_default(),
                );
                Ok(llm::Response {
                    text: format!(
                        r#"{{"items":[{{"type":"assessment","data":{{"key":"{key}","score":2,"verdict":"unclear","findings":[{{"text":"no scope","severity":"major"}}],"suggestion":"Allow payment in 3 installments with no fees, for card payments of the web shop","reply":"say the scope"}}}}]}}"#
                    ),
                    ..Default::default()
                })
            }
        })
    };
    let (e, _g, base, cancel) = wired(s, client).await;
    let c = e
        .graph
        .create_change(&ctx, installments(&base))
        .await
        .unwrap();
    settle(&e, 3).await;
    // the person starts the delivery on the change: nothing in it rates the description again
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "sdlc".into(),
                goal: "software_delivery".into(),
                change_id: c.id.clone(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    Box::pin(e.run(&ctx, &p.id)).await.unwrap();
    settle(&e, 3).await;
    let prompts = prompts.lock().clone();
    let rated = prompts
        .iter()
        .filter(|p| p.contains("You review the description of a change"))
        .count();
    let all = e.store.list(&ctx).await.unwrap();
    if rated != 1 {
        let mut log = String::new();
        for p in all.iter().filter(|p| p.change_id == c.id) {
            log += &format!(
                "process {} {}/{} goal {} status {} runs {}\n",
                p.id, p.methodology, p.agent, p.goal, p.status, p.runs
            );
            for s in &p.steps {
                log += &format!("  step {} {}\n", s.action, s.error);
            }
        }
        panic!(
            "{key}: the description is rated {rated} times ({} model calls)\n{log}",
            prompts.len()
        );
    }
    if key != "description" {
        for p in all
            .iter()
            .filter(|p| p.change_id == c.id && p.agent == "description_rating")
        {
            assert!(
                p.pending
                    .as_ref()
                    .is_some_and(|t| t.action == "rate_description_by_hand"),
                "the rating falls to a person: {:?}",
                p.pending
            );
        }
    }
    cancel.cancel();
}

/// The description edited again while its rating is in flight (ADR 0110, supersede): the model call is interrupted,
/// the rating of the text before is dropped, and the run rates the description as it is now.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn sdlc_edit_during_rating_restarts_it() {
    let ctx = dev_ctx();
    let s = Box::pin(sdlc_setup()).await;
    let (started_tx, mut started) = tokio::sync::mpsc::channel::<()>(1);
    let prompts: Arc<Mutex<Vec<String>>> = Arc::default();
    let interrupted: Arc<Mutex<usize>> = Arc::default();
    let client = {
        let (prompts, interrupted) = (prompts.clone(), interrupted.clone());
        llm::ClientFunc::shared(move |ctx: Ctx, r: llm::Request| {
            let (prompts, interrupted, started_tx) =
                (prompts.clone(), interrupted.clone(), started_tx.clone());
            async move {
                let first = {
                    let mut p = prompts.lock();
                    p.push(
                        r.messages
                            .last()
                            .map(|m| m.content.clone())
                            .unwrap_or_default(),
                    );
                    p.len() == 1
                };
                if first {
                    // the first rating waits for its answer until it is interrupted
                    let _ = started_tx.try_send(());
                    let err = tokio::select! {
                        err = ctx.done() => err,
                        _ = tokio::time::sleep(Duration::from_secs(5)) => {
                            return Err(llm::Error::Other("never interrupted".into()));
                        }
                    };
                    *interrupted.lock() += 1;
                    return Err(llm::Error::Ctx(err));
                }
                Ok(llm::Response {
                    text: r#"{"items":[{"type":"assessment","data":{"key":"description","score":4,"verdict":"clear","reply":"fine"}}]}"#.into(),
                    ..Default::default()
                })
            }
        })
    };
    let (e, g, base, cancel) = wired(s, client).await;
    let c = e
        .graph
        .create_change(&ctx, installments(&base))
        .await
        .unwrap();
    tokio::time::timeout(Duration::from_secs(5), started.recv())
        .await
        .expect("the rating did not start");
    let text = "Allow payment in 3 installments with no fees, for card payments of the web shop";
    e.graph
        .update_change(
            &ctx,
            &c.id,
            ChangePatch {
                intent: Some(text.into()),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    settle(&e, 4).await;
    let prompts = prompts.lock().clone();
    let interrupted = *interrupted.lock();
    assert!(
        interrupted == 1 && prompts.len() == 2,
        "interrupted {interrupted}, {} model calls, want 1 and 2",
        prompts.len()
    );
    assert!(
        prompts[1].contains("with no fees"),
        "the new run rates the text before: {}",
        prompts[1]
    );
    let all = e.store.list(&ctx).await.unwrap();
    for p in all
        .iter()
        .filter(|p| p.change_id == c.id && p.agent == "description_rating")
    {
        assert!(
            p.status == STATUS_COMPLETED || p.status == STATUS_CANCELLED,
            "rating process {} status {} pending {:?}",
            p.id,
            p.status,
            p.pending
        );
    }
    let bb = g.blackboard(&ctx, &c.id).await.unwrap();
    let mut cur = 0;
    for it in &bb.change.items {
        if it.r#type == "assessment"
            && it.data.get("key").and_then(Value::as_str) == Some("description")
        {
            cur += 1;
            let want = assess::fingerprint_of(&bb, "description");
            assert_eq!(
                it.data.get("rated").and_then(Value::as_str),
                Some(want.as_str()),
                "the rating is of another text"
            );
        }
    }
    assert_eq!(cur, 1, "{cur} ratings, want 1");
    cancel.cancel();
}
