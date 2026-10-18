//! Go: assessmentstates_test.go.

use goap_assess as assess;
use goap_condition::{self as condition, AssessmentState};
use goap_domain::ChangeItem;
use serde_json::json;

use crate::common::*;
use crate::reaction::{DESCRIBED_YAML, note_change};
use crate::steps::staged_setup;

/// The states of the assessments follow what the methodology declares, bound to its conditions (ADR 0116).
#[tokio::test]
async fn assessment_states_follow_the_methodology() {
    let ctx = ctx();
    let mut s = staged_setup().await;
    add_yaml(
        &mut s,
        &format!(
            "{DESCRIBED_YAML}{}",
            r#"
assessments:
  - subject: description
    states:
      - {conditions: {description_accepted: true}, tone: ok, label: "good to go"}
      - {when: "assessment.score <= 1", tone: danger, label: "to be rewritten", open: true}
"#
        ),
    );
    let (e, g, _, _errs) = s.share();
    let ch = note_change(&g, "write a note", "described").await;
    let rate = |score: i64| {
        let (e, g, id) = (e.clone(), g.clone(), ch.id.clone());
        async move {
            let ctx = crate::common::ctx();
            let bb = g.blackboard(&ctx, &id).await.unwrap();
            let text = assess::text(&bb, assess::SUBJECT_DESCRIPTION).unwrap_or_default();
            g.add_items(
                &ctx,
                &id,
                vec![ChangeItem {
                    r#type: assess::TYPE_ASSESSMENT,
                    data: obj(json!({"key": "description", "score": score, "rated": text})),
                    ..Default::default()
                }],
            )
            .await
            .unwrap();
            let states = e.assessment_states(&ctx, &id).await.unwrap();
            assert_eq!(states.len(), 1, "states {states:?}");
            states.into_iter().next().unwrap()
        }
    };
    let s: AssessmentState = rate(4).await;
    assert!(
        s.tone == condition::TONE_OK && s.label == "good to go",
        "accepted {s:?}"
    );
    let s = rate(1).await;
    assert!(s.tone == condition::TONE_DANGER && s.open, "poor {s:?}");
    let s = rate(2).await;
    assert!(
        s.tone == condition::TONE_WARN && s.label == "to be improved",
        "the defaults settle the rest {s:?}"
    );
    let _ = ctx;
}
