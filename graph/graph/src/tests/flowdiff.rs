//! DiffFlows (ADR 0083): the impacts two flows see differ as added, removed or modified,
//! the identical ones are counted.

use std::collections::HashMap;

use goap_domain::*;
use goap_graphapi::{LinkWrite, NodeCreate, OpenOptionRequest};
use serde_json::Value;

use crate::changeview::{VIEW_ACCEPTED, VIEW_WRITTEN};
use crate::tests::draft::title;
use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{ErrorKind, NewChange};

fn by_key(d: &FlowDiff) -> HashMap<String, ImpactDiff> {
    d.impacts
        .iter()
        .map(|i| (i.key.clone(), i.clone()))
        .collect()
}

fn on(flow: &str, p: serde_json::Map<String, Value>) -> Edit {
    Edit {
        flow: flow.into(),
        properties: p,
        ..Default::default()
    }
}

fn verifies(to: &NodeRef) -> LinkWrite {
    LinkWrite {
        r#type: "verifies".into(),
        to: to.clone(),
        ..Default::default()
    }
}

#[tokio::test]
async fn diff_flows() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let req2 = must(
            import_node(
                g,
                ImportNode {
                    key: "REQ-2".into(),
                    r#type: "Requirement".into(),
                    properties: title("Refund"),
                    ..Default::default()
                },
            )
            .await,
        );
        let base = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);
        let c = must(
            g.create_change(
                &ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "PSP".into(),
                    baseline_id: base.id.clone(),
                    own_branch: true,
                    ..Default::default()
                },
            )
            .await,
        );
        let (pre, pre2) = (f.req.r#ref(), req2.r#ref());
        let modified = |p: &NodeRef, rationale: &str| ChangeImpact {
            intent: INTENT_MODIFIED,
            pre: Some(p.clone()),
            rationale: rationale.into(),
            ..Default::default()
        };

        // the main flow: REQ-1 modified and accepted, REQ-2 modified (identical on every flow), TST-9 created
        let m1 = must(
            g.propose_impact(&ctx, &c.id, vec![modified(&pre, "main")])
                .await,
        )[0]
        .clone();
        must(edit(g, &c.id, &m1.id, on(MAIN_FLOW, title("main title"))).await);
        must(accept_on(g, &c.id, MAIN_FLOW, "", &m1.id, "u", "ok").await);
        let m2 = must(
            g.propose_impact(&ctx, &c.id, vec![modified(&pre2, "r2")])
                .await,
        )[0]
        .clone();
        must(edit(g, &c.id, &m2.id, on(MAIN_FLOW, title("Refund v2"))).await);
        let m3 = must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "TST-9".into(),
                    r#type: "TestCase".into(),
                    rationale: "new".into(),
                    flow: MAIN_FLOW.into(),
                    links: vec![verifies(&pre)],
                    ..Default::default()
                },
            )
            .await,
        );

        let option = |name: &str| OpenOptionRequest {
            name: name.into(),
            by: "u".into(),
            ..Default::default()
        };
        let a = must(g.open_option(&ctx, &c.id, option("stripe")).await);
        let b = must(g.open_option(&ctx, &c.id, option("adyen")).await);

        // nothing differs yet: every impact is identical, whoever is on the left
        let d = must(g.diff_flows(&ctx, &c.id, "", &a.id, "").await);
        assert!(
            d.impacts.is_empty()
                && d.identical == 3
                && d.left == MAIN_FLOW
                && d.right == a.id
                && d.level == VIEW_WRITTEN,
            "nothing differs: {d:?}"
        );
        let d = must(g.diff_flows(&ctx, &c.id, &a.id, &a.id, "").await);
        assert!(
            d.impacts.is_empty() && d.identical == 3,
            "one flow against itself: {d:?}"
        );

        // option A: REQ-1 retitled, TST-9 linked to REQ-2 too, a creation of its own
        must(
            edit(
                g,
                &c.id,
                &m1.id,
                on(
                    &a.id,
                    props(&[("title", "A title".into()), ("note", "n".into())]),
                ),
            )
            .await,
        );
        must(
            edit(
                g,
                &c.id,
                &m3.id,
                Edit {
                    flow: a.id.clone(),
                    add_links: vec![verifies(&pre2)],
                    ..Default::default()
                },
            )
            .await,
        );
        let ma = must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "TST-A".into(),
                    r#type: "TestCase".into(),
                    rationale: "only A".into(),
                    flow: a.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        // option B: REQ-1 retitled in another way, a creation with the same key as A's
        must(edit(g, &c.id, &m1.id, on(&b.id, title("B title"))).await);
        must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "TST-A".into(),
                    r#type: "TestCase".into(),
                    rationale: "also B".into(),
                    flow: b.id.clone(),
                    properties: title("b"),
                    ..Default::default()
                },
            )
            .await,
        );

        // main against A: REQ-1 and TST-9 modified, TST-A added, REQ-2 identical
        let d = must(
            g.diff_flows(&ctx, &c.id, MAIN_FLOW, &a.id, VIEW_WRITTEN)
                .await,
        );
        let got = by_key(&d);
        let counts = d.diff_counts();
        assert!(
            d.impacts.len() == 3
                && d.identical == 1
                && counts.get(DIFF_ADDED) == Some(&1)
                && counts.get(DIFF_MODIFIED) == Some(&2),
            "main against A: {d:?}"
        );
        let req = &got["REQ-1"];
        let fc = |name: &str, op: &str, old: Value, new: Value| FieldChange {
            kind: FIELD_PROPERTY.into(),
            name: name.into(),
            op: op.into(),
            old,
            new,
            ..Default::default()
        };
        assert!(
            req.category == DIFF_MODIFIED
                && req.node == pre.id
                && req.changes.len() == 2
                && req.changes[0] == fc("note", OP_ADDED, Value::Null, "n".into())
                && req.changes[1] == fc("title", OP_CHANGED, "main title".into(), "A title".into()),
            "REQ-1 modified: {req:?}"
        );
        let tst = &got["TST-9"];
        assert!(
            tst.category == DIFF_MODIFIED
                && tst.changes.len() == 1
                && tst.changes[0].kind == FIELD_LINK
                && tst.changes[0].op == OP_ADDED
                && tst.changes[0].name == "verifies"
                && tst.changes[0].target == "REQ-2",
            "TST-9 link added: {tst:?}"
        );
        let add = &got["TST-A"];
        assert!(
            add.category == DIFF_ADDED
                && add.left.is_none()
                && add.right.as_ref().is_some_and(|r| r.impact == ma.id
                    && r.intent == INTENT_CREATED
                    && r.flow == a.id),
            "TST-A added: {add:?}"
        );

        // the other way round: removed, and the old value is the left one
        let d = must(g.diff_flows(&ctx, &c.id, &a.id, "main", "").await);
        let got = by_key(&d);
        assert!(
            got["TST-A"].category == DIFF_REMOVED
                && got["TST-A"].right.is_none()
                && got["TST-9"].changes[0].op == OP_REMOVED
                && got["REQ-1"].changes[1].old == "A title"
                && got["REQ-1"].changes[1].new == "main title",
            "A against main: {:?}",
            d.impacts
        );

        // option against option: REQ-1 differs by title, TST-A (created on both, two nodes) by its properties, TST-9
        // by its link
        let d = must(g.diff_flows(&ctx, &c.id, &a.id, &b.id, "").await);
        let got = by_key(&d);
        let req_changes = &got["REQ-1"].changes;
        assert!(
            d.impacts.len() == 3
                && d.identical == 1
                && got["TST-A"].category == DIFF_MODIFIED
                && got["TST-A"].changes[0].name == "title"
                && got["TST-9"].changes[0].op == OP_REMOVED
                && req_changes.last().map(|c| &c.new) == Some(&Value::from("B title")),
            "A against B: {:?}",
            d.impacts
        );

        // accepted level: only impacts accepted on the flow; A's REQ-1 went back to proposed with its edit
        let d = must(
            g.diff_flows(&ctx, &c.id, MAIN_FLOW, &a.id, VIEW_ACCEPTED)
                .await,
        );
        assert!(
            d.impacts.len() == 1
                && d.impacts[0].key == "REQ-1"
                && d.impacts[0].category == DIFF_REMOVED
                && d.identical == 0,
            "accepted, A has not accepted REQ-1: {d:?}"
        );
        must(accept_on(g, &c.id, &a.id, "", &m1.id, "u", "ok").await);
        let d = must(
            g.diff_flows(&ctx, &c.id, MAIN_FLOW, &a.id, VIEW_ACCEPTED)
                .await,
        );
        assert!(
            d.impacts.len() == 1
                && d.impacts[0].key == "REQ-1"
                && d.impacts[0].category == DIFF_MODIFIED
                && d.impacts[0]
                    .right
                    .as_ref()
                    .is_some_and(|r| r.review == REVIEW_ACCEPTED),
            "accepted on both: {d:?}"
        );

        // a decided option is still comparable
        must(g.reject_option(&ctx, &c.id, &b.id, "u").await);
        let d = must(g.diff_flows(&ctx, &c.id, MAIN_FLOW, &b.id, "").await);
        assert!(!d.impacts.is_empty(), "a rejected option: {d:?}");

        // refused: an unknown flow or level
        assert_kind(
            g.diff_flows(&ctx, &c.id, "", "no-such-flow", "").await,
            ErrorKind::NotFound,
            "unknown flow",
        );
        assert_kind(
            g.diff_flows(&ctx, &c.id, "", &a.id, "landed").await,
            ErrorKind::Invalid,
            "unknown level",
        );
    })
    .await;
}
