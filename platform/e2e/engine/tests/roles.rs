//! Go: roles_test.go.

use std::sync::Arc;

use goap_engine::{
    ActionResult, Engine, ItemInput, PERMISSION_RUN_ACTION, STATUS_COMPLETED, STATUS_WAITING,
    StartRequest, TASK_APPROVAL, builtin,
};
use goap_methodology as methodology;
use serde_json::Value;

use crate::common::*;

/// roles_engine serves roled (engine/engine/testdata/roles.yaml): a release only a release_manager runs, by an agent
/// any developer or release_manager starts, and an audit agent only an auditor starts (ADR 0043).
pub async fn roles_engine() -> (Arc<Engine>, Errs) {
    let mut s = setup().await;
    add_yaml(&mut s, &read_roles());
    // test.emit writes an artifact of the type its params name
    s.builtin(
        "test.emit",
        builtin(|_ctx, ac| async move {
            let typ = ac
                .action
                .params
                .get("target")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_string();
            Ok(ActionResult {
                items: vec![ItemInput {
                    r#type: "artifact".into(),
                    target: typ,
                    ..Default::default()
                }],
                ..Default::default()
            })
        }),
    );
    let (e, _, _, errs) = s.share();
    (e, errs)
}

#[test]
fn roles_undeclared_by_the_methodology_are_refused() {
    let m = methodology::parse(
        br#"
name: bad
version: 1.0.0
roles: [{name: developer}]
conditions: [{name: done, expr: 'artifacts.exists(a, a.target == "x")'}]
actions: [{name: a, kind: human, roles: [tester], effects: {done: true}}]
goals: [{name: g, pre: {done: true}}]
agents: [{name: x, roles: [auditor], actions: [a]}]
"#,
    )
    .unwrap();
    let err = m.compile().unwrap_err();
    let methodology::Error::Invalid { issues, .. } = err else {
        panic!("expected issues, got {err}");
    };
    let paths: Vec<&str> = issues.0.iter().map(|i| i.path.as_str()).collect();
    assert_eq!(
        paths,
        ["actions[0].roles[0]", "agents[0].roles[0]"],
        "undeclared roles must be reported: {issues:?}"
    );
}

#[tokio::test]
async fn agents_and_actions_run_by_their_roles() {
    let (e, _errs) = roles_engine().await;
    let dev = as_who(&principal("dev", "", &["developer"]));
    let rm = as_who(&principal("rm", "", &["release_manager"]));
    let req = |goal: &str, intent: &str| StartRequest {
        methodology: "roled".into(),
        goal: goal.into(),
        intent: intent.into(),
        project_id: TEST_PROJECT.into(),
        ..Default::default()
    };

    // an agent its initiator holds no role of may not be started
    let err = e.start(&dev, req("audited", "audit")).await.unwrap_err();
    assert!(
        err.is_forbidden(),
        "a developer started the auditor's agent: {err}"
    );

    // a developer starts the shipper: building is open to it, releasing is the release manager's
    let p = e.start(&dev, req("ship", "ship it")).await.unwrap();
    let p = e.run(&dev, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_WAITING
            && p.pending.as_ref().is_some_and(|t| t.kind == TASK_APPROVAL
                && t.action == "release"
                && t.permission == PERMISSION_RUN_ACTION
                && t.roles == ["release_manager"]),
        "the release must wait for a release manager: {p:?}"
    );
    let err = e.approve(&dev, &p.id, true, "me").await.unwrap_err();
    assert!(
        err.is_forbidden(),
        "a developer approved the release: {err}"
    );
    e.approve(&rm, &p.id, true, "go").await.unwrap();
    let p = e.run(&rm, &p.id).await.unwrap();
    assert_eq!(
        p.status, STATUS_COMPLETED,
        "released by the release manager: {p:?}"
    );

    // a release manager runs it all at once
    let p = e.start(&rm, req("ship", "ship it")).await.unwrap();
    let p = e.run(&rm, &p.id).await.unwrap();
    assert_eq!(
        p.status, STATUS_COMPLETED,
        "a release manager ships alone: {p:?}"
    );
}
