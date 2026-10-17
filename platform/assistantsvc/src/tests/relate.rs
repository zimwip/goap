//! relate_test.go

use goap_domain::{CHANGE_ACTIVE, Change, ChangeID};

use super::*;
use crate::agents::{DECISION_ACCEPT, STATUS_PROPOSED, describe_actions};
use crate::relate::{
    ACTION_ATTACH_CHANGE, ACTION_DETACH_CHANGE, TOOL_ATTACH_CHANGE, TOOL_DETACH_CHANGE,
};
use crate::uitools::STATUS_DONE;

/// On a change, attach_change and detach_change record proposals; accepted, they attach or detach the change as the
/// person (ADR 0106).
#[tokio::test]
async fn attach_and_detach_proposals() {
    let e = new_env(&[
        &call(
            TOOL_ATTACH_CHANGE,
            r#"{"parent":"CHG-R","rationale":"it ships with R1"}"#,
        ),
        r#"{"message":"proposed"}"#,
    ])
    .await;
    e.graph.0.lock().changes.insert(
        sv("CHG-R"),
        Change {
            id: ChangeID::from("CHG-R"),
            title: "R1".into(),
            methodology: "release-based".into(),
            project_id: "PROJ-A".into(),
            status: CHANGE_ACTIVE,
            ..Default::default()
        },
    );
    e.send("put it in R1", change_ctx("CHG-D")).await.unwrap();
    let m = e.answer().await;
    assert!(
        m.actions.len() == 1
            && s(&m.actions[0], "type") == ACTION_ATTACH_CHANGE
            && s(&m.actions[0], "status") == STATUS_PROPOSED,
        "actions {:?}",
        m.actions
    );
    assert!(
        e.graph.change("CHG-D").parent_id.is_empty(),
        "a proposal changes nothing"
    );
    let got = e.confirm(&m, DECISION_ACCEPT).await.unwrap();
    assert!(
        s(&got.actions[0], "status") == STATUS_DONE
            && e.graph.change("CHG-D").parent_id.as_str() == "CHG-R",
        "accepted: {:?}",
        got.actions[0]
    );
    assert!(
        e.confirm(&m, DECISION_ACCEPT).await.is_err(),
        "decided twice"
    );
    let d = describe_actions(&got.actions);
    assert!(
        d.contains("attach change CHG-D to CHG-R: done"),
        "history: {d}"
    );

    // attached, the change is proposed to be detached
    e.model.0.lock().answers.extend([
        call(TOOL_DETACH_CHANGE, "{}"),
        r#"{"message":"proposed"}"#.to_string(),
    ]);
    e.send("take it out", change_ctx("CHG-D")).await.unwrap();
    let m = e.answer().await;
    assert!(
        m.actions.len() == 1 && s(&m.actions[0], "type") == ACTION_DETACH_CHANGE,
        "actions {:?}",
        m.actions
    );
    e.confirm(&m, DECISION_ACCEPT).await.unwrap();
    assert!(e.graph.change("CHG-D").parent_id.is_empty());
}
