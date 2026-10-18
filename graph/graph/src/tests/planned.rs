//! A creation is planned before it is written (ADR 0121).

use goap_domain::*;
use goap_graphapi::{LinkWrite, NodeCheckout, NodeCreate};
use serde_json::Value;

use crate::tests::draft::title;
use crate::tests::helpers::*;
use crate::tests::resolve::{impacts_on, plain};
use crate::testsupport::*;
use crate::{ErrorKind, Result};

// A creation is planned before it is written (an impact analysis on a green field names what the change will
// create): ProposeImpact with intent created gives an impact with no draft; creating or checking out its key gives it
// its draft, on the same impact; the change does not merge while one is still planned.
#[tokio::test]
async fn planned_creation() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = plain(&f, "green field").await;
        let plan = async |key: &str, typ: &str| -> Result<Vec<ChangeImpact>> {
            g.propose_impact(
                &ctx,
                &c.id,
                vec![ChangeImpact {
                    intent: INTENT_CREATED,
                    key: key.into(),
                    r#type: typ.into(),
                    rationale: format!("the change needs {key}"),
                    ..Default::default()
                }],
            )
            .await
        };
        let req = must(plan("REQ-NEW", "Requirement").await)[0].clone();
        assert!(
            req.intent == INTENT_CREATED
                && req.post.is_none()
                && req.planned()
                && req.review == REVIEW_PROPOSED,
            "a planned creation has no draft: {req:?}"
        );
        assert_err(
            plan("REQ-1", "Requirement").await,
            ErrorKind::Conflict,
            &["baseline"],
            "a key the baseline holds is not planned",
        );
        assert_err(
            plan("REQ-NEW", "Requirement").await,
            ErrorKind::Conflict,
            &["already holds"],
            "a key planned twice",
        );
        assert!(
            g.impact_node_review(&ctx, &c.id, &req.id, REVIEW_ACCEPTED, "bob", "fine")
                .await
                .is_err(),
            "a planned creation modifies nothing yet: it is not accepted"
        );

        // the creation of its key fulfils it: same impact, a draft with the properties
        let made = must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "REQ-NEW".into(),
                    r#type: "Requirement".into(),
                    rationale: "written".into(),
                    properties: title("Pay by card"),
                    ..Default::default()
                },
            )
            .await,
        );
        let made_post = made.post.clone().expect("post");
        assert!(
            made.id == req.id && made_post.is_draft() && made.rationale == req.rationale,
            "the creation fulfils the planned impact: {made:?}"
        );
        let view = must(g.change_node_view(&ctx, &c.id, "", &made_post).await);
        assert!(
            view.node.properties.get("title") == Some(&Value::from("Pay by card"))
                && view.node.key == "REQ-NEW",
            "the draft: {:?}",
            view.node
        );
        assert_kind(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "REQ-NEW".into(),
                    r#type: "Requirement".into(),
                    rationale: "again".into(),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Conflict,
            "a fulfilled creation is not created again",
        );

        // the checkout of a planned key gives it a bare draft, edited next
        let tst = must(plan("TST-NEW", "TestCase").await)[0].clone();
        let out = must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    key: "TST-NEW".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            out.id == tst.id && out.post.as_ref().is_some_and(|p| p.is_draft()),
            "the checkout fulfils the planned impact: {out:?}"
        );
        must(
            g.impact_link_create(
                &ctx,
                &c.id,
                &tst.id,
                LinkWrite {
                    r#type: "verifies".into(),
                    to: NodeRef {
                        id: made_post.id.clone(),
                        version: 0,
                    },
                    ..Default::default()
                },
                "",
                "",
            )
            .await,
        );
        let ops: Vec<ImpactOp> = must(g.change_events(&ctx, &c.id).await)
            .into_iter()
            .filter(|e| e.impact == req.id || e.impact == tst.id)
            .map(|e| e.op)
            .collect();
        assert_eq!(
            ops,
            vec![
                IMPACT_PROPOSED,
                IMPACT_CHECKED_OUT,
                IMPACT_PROPOSED,
                IMPACT_CHECKED_OUT,
                IMPACT_UPDATED
            ],
            "a planned creation is proposed, then checked out by its creation"
        );

        // one still planned holds the merge
        must(plan("REQ-LATER", "Requirement").await);
        must(accept(g, &c.id, &req.id, "bob", "fine").await);
        must(accept(g, &c.id, &tst.id, "bob", "fine").await);
        let err = apply(g, &c.id, "").await.expect_err("held");
        assert!(
            err.to_string().contains("REQ-LATER is planned"),
            "a planned creation holds the merge: {err}"
        );
        let later = impacts_on(g, &c.id, "REQ-LATER").await;
        must(g.withdraw_impact(&ctx, &c.id, &later[0].id, "", "").await);
        must(apply(g, &c.id, "").await);
        for key in ["REQ-NEW", "TST-NEW"] {
            let n = must(g.node_by_key(&ctx, DEFAULT_NAMESPACE, key).await);
            assert_eq!(n.version, 1, "{key} lands as a new node: {n:?}");
        }
        check_impact_logs(r.repo.clone()).await;
    })
    .await;
}
