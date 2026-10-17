//! Go: changestate_test.go.

use std::collections::{BTreeMap, HashMap};

use goap_assess as assess;
use goap_domain::HeaderValue;
use goap_engine::{ItemInput, ProcessState, STEP_DONE, STEP_TODO, TriggerEvent};
use goap_graph::ChangePatch;
use serde_json::{Value, json};

use crate::common::*;
use crate::companion::{accompany, companion_of};
use crate::reaction::{DESCRIBED_YAML, note_change};
use crate::steps::staged_setup;

/// The current state of a change shows each process once, whatever the number of runs, and what a blocked step needs.
#[tokio::test]
async fn change_state_shows_one_state_per_process() {
    let ctx = ctx();
    let mut s = staged_setup().await;
    let c = add_yaml(&mut s, DESCRIBED_YAML);
    let (e, g, _, _errs) = s.share();
    let ch = note_change(&g, "write a note", "described").await;
    let rate = |score: i64, typ: &'static str, fields: BTreeMap<String, HeaderValue>| {
        let (e, g, id) = (e.clone(), g.clone(), ch.id.clone());
        async move {
            let ctx = crate::common::ctx();
            let bb = g.blackboard(&ctx, &id).await.unwrap();
            accompany(
                &e,
                TriggerEvent {
                    r#type: typ.into(),
                    change: Some(bb.change.clone()),
                    fields,
                    ..Default::default()
                },
            )
            .await;
            let text = assess::text(&bb, assess::SUBJECT_DESCRIPTION).unwrap_or_default();
            let p = companion_of(&e, &id).await.remove(0);
            e.submit(
                &ctx,
                &p.id,
                vec![ItemInput {
                    r#type: assess::TYPE_ASSESSMENT.to_string(),
                    data: obj(json!({"key": "description", "score": score, "verdict": "unclear", "rated": text})),
                    ..Default::default()
                }],
            )
            .await
            .unwrap();
            e.run(&ctx, &p.id).await.unwrap();
        }
    };
    // what the conditions mean and which actions produce them is the compiled view's; the state binds it (ADR 0126)
    let view = c.view();
    // before any rating, the accepted description needs a rating first, which the rating action produces (ADR 0112 §5)
    let st = e.change_state(&ctx, &ch.id).await.unwrap();
    for p in st.processes.iter().filter(|p| p.process == "delivery") {
        let n = &p.steps[0].needs;
        assert!(
            n.len() == 1
                && n[0].requires == ["description_rated"]
                && view
                    .condition("description_rated")
                    .produced_by
                    .iter()
                    .any(|a| a == "rate")
                && view.condition(&n[0].condition).produced_by.is_empty(),
            "an accepted description requires a rated one: {n:?}"
        );
        let f = &n[0].facts;
        assert!(
            f.len() == 1
                && f[0].parts.len() == 4
                && f[0].parts[0].missing
                && f[0].parts[0].produced_by.iter().any(|a| a == "rate"),
            "the facts say a rating of the description is missing, which rate establishes: {f:?}"
        );
    }
    rate(2, "change.created", BTreeMap::new()).await;
    let intent = "write the release note";
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
    rate(
        2,
        "change.edited",
        BTreeMap::from([(
            "intent".to_string(),
            HeaderValue {
                from: Value::from("write a note"),
                to: Value::from(intent),
            },
        )]),
    )
    .await;
    let st = e.change_state(&ctx, &ch.id).await.unwrap();
    assert_eq!(
        st.processes.len(),
        2,
        "one state per process: {:?}",
        st.processes
    );
    let by_name: HashMap<_, ProcessState> = st
        .processes
        .into_iter()
        .map(|p| (p.process.clone(), p))
        .collect();
    let (review, delivery) = (&by_name["review"], &by_name["delivery"]);
    assert!(
        view.process("review").is_some_and(|rv| rv.reactive)
            && review.runs == 2
            && review.state == STEP_DONE
            && review.steps.len() == 1
            && review.steps[0].state == STEP_DONE,
        "review: reactive, run twice, done once: {review:?}"
    );
    let note = &delivery.steps[0];
    assert!(
        delivery.runs == 0
            && note.state == STEP_TODO
            && note.needs.len() == 1
            && note.needs[0].condition == "description_accepted"
            && note.needs[0].want
            && !view
                .condition(&note.needs[0].condition)
                .description
                .is_empty()
            && note.needs[0].requires.is_empty(),
        "delivery waits for an accepted description, and says so: {note:?}"
    );
    let f = &note.needs[0].facts[0].parts;
    assert!(
        f[0].holds
            && f[1].holds
            && f[2].holds
            && f[3].missing
            && f[3].text == "a.score >= 3"
            && f[3].produced_by.is_empty(),
        "rated and current, the score is what is missing, and no action guarantees it: {f:?}"
    );
}
