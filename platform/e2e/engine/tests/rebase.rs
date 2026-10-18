//! Go: rebase_test.go.

use std::sync::Arc;

use goap_domain::{self as domain};
use goap_engine::{
    STATUS_COMPLETED, STATUS_WAITING, StartRequest, StaticMethodologies, TASK_REBASE,
};
use goap_graph::{NewChange, NodeCheckout, NodeUpdate};
use serde_json::{Value, json};

use crate::common::*;

const APPLY_ONLY: &str = r#"
name: apply-only
version: 1.0.0
namespace: alm
goal: done
conditions:
  - name: applied
    description: The change has been applied
    expr: change.status == "applied"
actions:
  - name: apply_change
    kind: builtin
    builtin: graph.apply
    description: Apply the change
    effects: {applied: true}
    cost: 1
agents:
  - name: applier
    description: Applies the change
    planner: goap
    actions: [apply_change]
    goals: [done]
goals:
  - name: done
    description: The change is applied
    pre: {applied: true}
    value: 1
"#;

/// Two changes forked from the same baseline edit the same property: the run applying the second waits on the
/// conflicts of the rebase (ADR 0101), a person settles them, and the run goes on and lands the change.
#[tokio::test]
async fn run_waits_on_the_conflicts_of_the_rebase() {
    let ctx = ctx();
    let mut s = setup().await;
    let cm = compile_yaml(APPLY_ONLY);
    s.e.methodologies = Arc::new(StaticMethodologies::new().with(cm));
    let (e, g, base, _errs) = s.share();
    let req = g.node_by_key(&ctx, "alm", "REQ-1").await.unwrap();
    let edited = |title: &'static str, value: &'static str| {
        let (g, base, req) = (g.clone(), base.clone(), req.id.clone());
        async move {
            let ctx = crate::common::ctx();
            let c = g
                .create_change(
                    &ctx,
                    NewChange {
                        project_id: TEST_PROJECT.into(),
                        namespace: "alm".into(),
                        title: title.into(),
                        baseline_id: base,
                        own_branch: true,
                        ..Default::default()
                    },
                )
                .await
                .unwrap();
            let cn = g
                .impact_node_checkout(
                    &ctx,
                    &c.id,
                    NodeCheckout {
                        node: req,
                        rationale: title.into(),
                        ..Default::default()
                    },
                )
                .await
                .unwrap();
            g.impact_node_update(
                &ctx,
                &c.id,
                &cn.id,
                NodeUpdate {
                    properties: obj(json!({"title": value})),
                    ..Default::default()
                },
            )
            .await
            .unwrap();
            g.impact_node_review(&ctx, &c.id, &cn.id, domain::REVIEW_ACCEPTED, "u", "ok")
                .await
                .unwrap();
            c
        }
    };
    let first = edited("first", "One").await;
    let second = edited("second", "Two").await;
    g.apply(&ctx, &first.id, "").await.unwrap();

    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "apply-only".into(),
                change_id: second.id.clone(),
                project_id: TEST_PROJECT.into(),
                agent: "applier".into(),
                goal: "done".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    let rebase = p
        .pending
        .as_ref()
        .filter(|t| t.kind == TASK_REBASE)
        .and_then(|t| t.rebase.clone());
    assert!(
        p.status == STATUS_WAITING && rebase.is_some(),
        "the run waits on the rebase: {} {} {:?}",
        p.status,
        p.error,
        p.pending
    );
    let rebase = rebase.unwrap();
    let im = &rebase.impacts;
    assert!(
        im.len() == 1 && im[0].key == "REQ-1" && im[0].conflicts == ["props.title"],
        "the task lists the conflict: {rebase:?}"
    );
    assert!(
        e.resolve_rebase(&ctx, &p.id, "").await.is_err(),
        "nothing is settled yet"
    );

    let st = g.rebase_state(&ctx, &second.id).await.unwrap();
    assert_eq!(st.conflicts.len(), 1, "state: {st:?}");
    let impact = st.conflicts[0].impact.clone();
    g.impact_node_resolve(&ctx, &second.id, &impact, "")
        .await
        .unwrap();
    g.impact_node_review(
        &ctx,
        &second.id,
        &impact,
        domain::REVIEW_ACCEPTED,
        "u",
        "ok",
    )
    .await
    .unwrap();
    let p = e.resolve_rebase(&ctx, &p.id, "settled").await.unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert_eq!(
        p.status, STATUS_COMPLETED,
        "the run completes: {} {:?}",
        p.error, p.pending
    );
    let c = g.change(&ctx, &second.id).await.unwrap();
    assert_eq!(
        c.status,
        domain::CHANGE_APPLIED,
        "the change is applied: {c:?}"
    );
    let n = g.node_by_key(&ctx, "alm", "REQ-1").await.unwrap();
    assert_eq!(
        n.properties.get("title"),
        Some(&Value::from("Two")),
        "the change keeps its value"
    );
}
