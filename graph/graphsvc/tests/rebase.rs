mod common;

use std::sync::Arc;

use common::{client, graph, serve};
use goap_connect::Interceptors;
use goap_ctx::Ctx;
use goap_domain::{MAIN_BRANCH, REVIEW_ACCEPTED, conflict_prop};
use goap_graph::{Commit, NewChange, NodeCheckout, NodeEdit, NodeUpdate};
use goap_graphapi::Port;
use goap_graphsvc::Handler;

/// A sub-change behind its parent through the service (ADR 0103): the parent committed the node the sub-change
/// committed on its own branch; the state tells what is behind, the rebase names the conflict, the resolution keeps
/// the sub-change's value, and the sub-change then applies into its parent.
#[tokio::test]
async fn rebase_through_the_service() {
    let ctx = &Ctx::background();
    let g = Arc::new(graph());
    let cl = client(&serve(Handler::new(g.clone()), Interceptors::none()).await);

    let base = g.branch_head(ctx, "", MAIN_BRANCH).await.unwrap();
    Box::pin(g.commit(
        ctx,
        Commit {
            project_id: "PROJ-ROOT".into(),
            title: "seed".into(),
            baseline: base.id,
            by: "test".into(),
            edits: vec![NodeEdit {
                key: "N-1".into(),
                r#type: "Design".into(),
                props: obj! {"title" => "t"},
                ..Default::default()
            }],
            ..Default::default()
        },
    ))
    .await
    .unwrap();
    let head = g.branch_head(ctx, "", MAIN_BRANCH).await.unwrap();
    let parent = g
        .create_change(
            ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "parent".into(),
                baseline_id: head.id,
                own_branch: true,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let checkout = |rationale: &str| NodeCheckout {
        key: "N-1".into(),
        rationale: rationale.into(),
        ..Default::default()
    };
    let title = |t: &str| NodeUpdate {
        properties: obj! {"title" => t},
        ..Default::default()
    };
    let pcn = g
        .impact_node_checkout(ctx, &parent.id, checkout("p"))
        .await
        .unwrap();
    let sub = Box::pin(g.create_change(
        ctx,
        NewChange {
            title: "sub".into(),
            parent_id: parent.id.clone(),
            ..Default::default()
        },
    ))
    .await
    .unwrap();
    let scn = g
        .impact_node_checkout(ctx, &sub.id, checkout("s"))
        .await
        .unwrap();
    g.impact_node_update(ctx, &sub.id, &scn.id, title("sub"))
        .await
        .unwrap();
    Box::pin(g.impact_node_review(ctx, &sub.id, &scn.id, REVIEW_ACCEPTED, "r", "ok"))
        .await
        .unwrap();
    Box::pin(g.commit_change(ctx, &sub.id, "")).await.unwrap();
    g.impact_node_update(ctx, &parent.id, &pcn.id, title("parent"))
        .await
        .unwrap();
    Box::pin(g.impact_node_review(ctx, &parent.id, &pcn.id, REVIEW_ACCEPTED, "r", "ok"))
        .await
        .unwrap();
    Box::pin(g.commit_change(ctx, &parent.id, ""))
        .await
        .unwrap();

    let st = cl.rebase_state(ctx, &sub.id).await;
    assert!(
        matches!(&st, Ok(st) if st.parent == parent.id && st.behind.len() == 1 && st.behind[0] == scn.id && st.conflicts.is_empty()),
        "state = {st:?}"
    );
    let rb = cl.rebase_change(ctx, &sub.id).await;
    assert!(
        matches!(&rb, Ok(rb) if rb.impacts.len() == 1 && rb.impacts[0].conflicts.len() == 1 && rb.impacts[0].conflicts[0] == conflict_prop("title")),
        "rebase = {rb:?}"
    );
    let st = cl.rebase_state(ctx, &sub.id).await.unwrap();
    assert!(
        st.behind.is_empty() && st.conflicts.len() == 1,
        "after the rebase: {st:?}"
    );
    cl.impact_node_resolve(ctx, &sub.id, &scn.id, "")
        .await
        .unwrap();
    let err = cl.impact_node_resolve(ctx, &sub.id, &scn.id, "").await;
    assert!(
        matches!(&err, Err(e) if e.is_conflict()),
        "nothing left to settle: {err:?}"
    );
    Box::pin(g.impact_node_review(ctx, &sub.id, &scn.id, REVIEW_ACCEPTED, "r", "ours"))
        .await
        .unwrap();
    cl.apply(ctx, &sub.id, "").await.unwrap();
    let v = g
        .change_node_view_by_key(ctx, &parent.id, "", "", "N-1")
        .await;
    assert!(
        matches!(&v, Ok(v) if v.properties.get("title") == Some(&serde_json::json!("sub"))),
        "parent = {v:?}"
    );
}
