//! Go: asks_test.go.

use goap_engine::{StartRequest, TASK_APPROVAL};

use crate::common::*;
use crate::roles::roles_engine;

#[tokio::test]
async fn change_asks_address_the_role_and_tell_who_may_answer() {
    let (e, _errs) = roles_engine().await;
    let dev = as_who(&principal("dev", "", &["developer"]));
    let rm = as_who(&principal("rm", "", &["release_manager"]));

    let p = e
        .start(
            &dev,
            StartRequest {
                methodology: "roled".into(),
                goal: "ship".into(),
                intent: "ship it".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.run(&dev, &p.id).await.unwrap();
    assert!(
        !p.change_id.is_empty() && p.pending.as_ref().is_some_and(|t| t.kind == TASK_APPROVAL),
        "the release must wait for a release manager on a change: {p:?}"
    );

    let asks = e.change_asks(&dev, &p.change_id).await.unwrap();
    assert!(
        asks.len() == 1
            && asks[0].kind == TASK_APPROVAL
            && asks[0].process_id == p.id
            && asks[0].action == "release"
            && asks[0].roles == ["release_manager"]
            && asks[0].owner.is_empty(),
        "one approval addressed to the release manager: {asks:?}"
    );
    assert!(
        !asks[0].may_answer,
        "a developer may not answer the release manager's approval"
    );
    let asks = e.change_asks(&rm, &p.change_id).await.unwrap();
    assert!(
        asks.len() == 1 && asks[0].may_answer,
        "the release manager answers it: {asks:?}"
    );

    e.approve(&rm, &p.id, true, "go").await.unwrap();
    e.run(&rm, &p.id).await.unwrap();
    let asks = e.change_asks(&dev, &p.change_id).await.unwrap();
    assert!(asks.is_empty(), "nothing is asked once approved: {asks:?}");
}
