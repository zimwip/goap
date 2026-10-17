//! subactivity_test.go: the cascade of the activity of a sub-change, asked of the registry directly (the graph asks
//! it at CreateChange, PORTING.md).

use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::{Change, MAIN_BRANCH};
use goap_graphapi::{Commit, LinkEdit, NodeEdit};

use super::{FakeGraph, bg, memory};
use crate::activitygoals::DATA_ACTIVITY;
use crate::graphstore::{GraphStore, LINK_SUB_ACTIVITY, NAMESPACE_METHODOLOGY, StoreGraph};
use crate::service::Service;

fn scoped(r: &str) -> Change {
    let mut c = Change::default();
    if !r.is_empty() {
        c.data.insert(DATA_ACTIVITY.into(), r.into());
    }
    c
}

async fn activities(ctx: &Ctx, g: &FakeGraph) {
    let mk = |key: &str, links: Vec<LinkEdit>| NodeEdit {
        key: key.into(),
        r#type: "methodology@Process".into(),
        links,
        ..NodeEdit::default()
    };
    let head = g
        .branch_head(ctx, NAMESPACE_METHODOLOGY, MAIN_BRANCH)
        .await
        .expect("head");
    g.commit(
        ctx,
        Commit {
            project_id: "PROJ-ROOT".into(),
            namespace: NAMESPACE_METHODOLOGY.into(),
            title: "activities".into(),
            baseline: head.id,
            by: "test".into(),
            edits: vec![
                mk(
                    "ACT-PARENT",
                    vec![LinkEdit {
                        r#type: LINK_SUB_ACTIVITY.into(),
                        to_key: "ACT-CHILD".into(),
                        ..LinkEdit::default()
                    }],
                ),
                mk("ACT-CHILD", vec![]),
                mk("ACT-OTHER", vec![]),
            ],
            ..Commit::default()
        },
    )
    .await
    .expect("commit");
}

// A sub-change's activity, when it names one, must be the parent's own or a descendant of it reached by
// sub_activity links; unset is fine (no inheritance).
#[tokio::test]
async fn sub_change_activity_cascade() {
    let ctx = bg();
    let g = FakeGraph::new();
    activities(&ctx, &g).await;
    let reg = Service::new(Arc::new(GraphStore::new(g.clone())));
    let parent = scoped("ACT-PARENT");
    reg.sub_change_validator(&ctx, &parent, &scoped("ACT-CHILD"))
        .await
        .expect("activity within the parent's");
    let err = reg
        .sub_change_validator(&ctx, &parent, &scoped("ACT-OTHER"))
        .await
        .err()
        .expect("activity outside the parent's");
    assert!(err.is_graph_invalid(), "{err}");
    reg.sub_change_validator(&ctx, &parent, &scoped(""))
        .await
        .expect("activity not inherited");
    reg.sub_change_validator(&ctx, &parent, &scoped("ACT-PARENT"))
        .await
        .expect("the parent's own");
    // a registry with no graph behind its store validates nothing
    Service::new(memory())
        .sub_change_validator(&ctx, &parent, &scoped("ACT-OTHER"))
        .await
        .expect("no graph");
}
