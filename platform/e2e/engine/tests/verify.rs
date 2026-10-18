//! Go: verify_test.go.

use std::sync::Arc;

use goap_domain::{self as domain, LogFilter};
use goap_verify as verify;

use crate::common::*;
use crate::derogation::{run_ops, verified_ctx};
use crate::nodeops::{on_change, root_change};

/// An action that declares an independent verification cannot accept what it produced; another action may, and the
/// states are facts of the change (ADR 0075).
#[tokio::test]
async fn independent_verification() {
    let ctx = ctx();
    let s = setup_with(|g| g.review_policy = Some(Arc::new(verify::Policy))).await;
    let (e, g, base, _errs) = s.share();
    let c = root_change(&g, "alm", "t", &base).await;
    let p = on_change(&c.id);
    let vctx = verified_ctx(&ctx);
    let err = run_ops(
        &e,
        &p,
        &vctx,
        "write",
        "e1",
        r#"const r = ctx.impactNode("REQ-1", "why"); ctx.writeNode(r, { props: { title: "A" } }); ctx.impactNodeReview(r, true, "mine");"#,
    )
    .await;
    assert!(
        err.as_ref()
            .is_err_and(|e| e.to_string().contains("may not verify")),
        "the producer reviews its own effect: {err:?}"
    );
    let ch = g.change(&ctx, &c.id).await.unwrap();
    assert!(
        ch.nodes.len() == 1 && ch.nodes[0].review == domain::REVIEW_PROPOSED,
        "nothing accepted: {:?}",
        ch.nodes
    );
    let bb = g.blackboard(&ctx, &c.id).await.unwrap();
    let s = verify::subjects(&bb.change.items);
    assert!(
        s.len() == 1
            && s[0].state == verify::PRODUCED
            && s[0].independent
            && s[0].oracle == "human"
            && s[0].producer == "write",
        "produced: {s:?}"
    );
    run_ops(
        &e,
        &p,
        &ctx,
        "check",
        "e2",
        r#"ctx.impactNodeReview("REQ-1", true, "checked");"#,
    )
    .await
    .expect("another action verifies");
    let bb = g.blackboard(&ctx, &c.id).await.unwrap();
    let s = verify::subjects(&bb.change.items);
    assert!(
        s.len() == 1 && s[0].state == verify::ACCEPTED && s[0].by == "check",
        "accepted: {s:?}"
    );
    let (entries, _) = g
        .change_log(
            &ctx,
            &LogFilter {
                change: c.id.clone(),
                types: vec!["fact.verification".into()],
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(entries.len(), 3, "log: produced, verified, accepted");

    // an action with no verification is not constrained
    run_ops(
        &e,
        &p,
        &ctx,
        "plain",
        "e3",
        r#"const r = ctx.impactNodeCreate("TestCase", "TST-9", "why"); ctx.writeNode(r, { props: { title: "T" } }); ctx.impactNodeReview(r, true, "mine");"#,
    )
    .await
    .expect("no verify declared");
}
