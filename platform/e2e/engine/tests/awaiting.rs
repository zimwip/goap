//! Go: awaiting_test.go.

use goap_domain::ChangeItem;
use goap_engine::{
    STATUS_FAILED, STATUS_RUNNING, STATUS_STUCK, STATUS_WAITING, StartRequest, TASK_APPROVAL,
    TASK_UNBLOCK, UNBLOCK_ABANDON, UNBLOCK_RETRY, UnblockRequest,
};
use goap_journal as journal;
use serde_json::json;

use crate::common::*;
use crate::companion::{CHOREO_YAML, item};
use crate::engine::deliver_until_reviewed;
use crate::steps::staged_setup;

fn decision(d: &str, reason: &str) -> UnblockRequest {
    UnblockRequest {
        decision: d.into(),
        reason: reason.into(),
    }
}

#[tokio::test]
async fn a_person_can_always_unblock_a_stuck_run() {
    let (e, g, p, _errs) = deliver_until_reviewed(contributor()).await;
    let ctx = as_who(&approver());
    e.approve(&ctx, &p.id, false, "not now").await.unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_STUCK
            && p.pending.as_ref().is_some_and(|t| t.kind == TASK_UNBLOCK)
            && !p.status.terminal(),
        "stuck with a task to unblock it: {p:?}"
    );
    // someone who does not answer for the run may not
    let other = as_who(&principal("dave", "globex", &[]));
    let err = e
        .unblock(&other, &p.id, decision(UNBLOCK_RETRY, ""))
        .await
        .unwrap_err();
    assert!(err.is_forbidden(), "forbidden: {err}");
    // its initiator retries what it gave up on: the application asks for its approval again
    let owner = as_who(&contributor());
    let p = e
        .unblock(&owner, &p.id, decision(UNBLOCK_RETRY, ""))
        .await
        .unwrap();
    assert!(
        p.status == STATUS_RUNNING && p.disabled.is_empty(),
        "retry: {} {:?}",
        p.status,
        p.disabled
    );
    let p = e.run(&owner, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_WAITING && p.pending.as_ref().unwrap().kind == TASK_APPROVAL,
        "approval asked again: {p:?}"
    );
    // rejected again: the approver of the organisation abandons it, with its reason
    e.approve(&ctx, &p.id, false, "still not").await.unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    let err = e
        .unblock(&ctx, &p.id, decision(UNBLOCK_ABANDON, ""))
        .await
        .unwrap_err();
    assert!(err.is_invalid_state(), "a reason is required: {err}");
    let p = e
        .unblock(
            &ctx,
            &p.id,
            decision(UNBLOCK_ABANDON, "postponed to the next release"),
        )
        .await
        .unwrap();
    assert_eq!(p.status, STATUS_FAILED, "abandon");
    let recs = journal::read(
        &ctx,
        &*g,
        &journal::Filter {
            process_ids: vec![p.id.clone()],
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let n = recs
        .iter()
        .filter(|r| r.kind == journal::KIND_UNBLOCK)
        .count();
    assert_eq!(n, 2, "each decision is journaled");
}

#[tokio::test]
async fn a_person_establishes_what_a_run_waits_for() {
    let ctx = as_who(&contributor());
    let mut s = staged_setup().await;
    add_yaml(&mut s, CHOREO_YAML);
    let (e, g, _, _errs) = s.share();
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
    let p = e.run(&ctx, &p.id).await.unwrap();
    e.submit(
        &ctx,
        &p.id,
        vec![
            artifact("note", json!({})),
            item(
                "risk",
                json!({"key": "RSK-1", "title": "wrong note", "probability": 4.0, "impact": 4.0}),
            ),
        ],
    )
    .await
    .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(p.waits_for_conditions(), "waits for the risks: {p:?}");
    // nobody declares the risks under control (ADR 0109 §6): a person mitigates the risk on the change, and the run,
    // asked to retry, sees it
    g.add_items(
        &ctx,
        &p.change_id,
        vec![ChangeItem {
            r#type: goap_risk::TYPE_ACTION,
            data: obj(
                json!({"key": "ACT-1", "title": "review the note", "status": "open", "for": "RSK-1"}),
            ),
            ..Default::default()
        }],
    )
    .await
    .unwrap();
    let p = e
        .unblock(&ctx, &p.id, decision(UNBLOCK_RETRY, ""))
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.pending
            .as_ref()
            .is_some_and(|t| t.action == "flow/approve"),
        "resumes to its approval: {p:?}"
    );
}
