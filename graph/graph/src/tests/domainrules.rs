//! Withdrawing impacts, the links a type requires, link attributes, retiring and
//! restoring by commit (ADR 0054, 0076, 0078, 0102).

use std::sync::Arc;

use goap_domain::*;
use goap_graphapi::{Commit, LinkWrite, NodeCheckout, NodeCreate, NodeEdit, NodeUpdate};
use serde_json::json;

use crate::tests::helpers::{accept_impact, draft_of, touch};
use crate::testsupport::*;
use crate::{ErrorKind, Graph, NewChange};

fn change_in(base: &Baseline, namespace: &str, title: &str) -> NewChange {
    NewChange {
        project_id: "PROJ-ROOT".into(),
        namespace: namespace.into(),
        title: title.into(),
        baseline_id: base.id.clone(),
        ..Default::default()
    }
}

fn checkout(node: &NodeID) -> NodeCheckout {
    NodeCheckout {
        node: node.clone(),
        rationale: "v2".into(),
        ..Default::default()
    }
}

fn create(key: &str, typ: &str, rationale: &str) -> NodeCreate {
    NodeCreate {
        key: key.into(),
        r#type: typ.into(),
        rationale: rationale.into(),
        ..Default::default()
    }
}

// A rejected impact keeps its draft, to be reworked once reopened; taking it out of the change is an explicit
// operation (ADR 0076 §5b).
#[tokio::test]
async fn withdraw_impact() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(
            g.create_change(&ctx, change_in(&f.base, "", "rework"))
                .await,
        );

        // a rejected checkout keeps its draft: edited, it is proposed again (ADR 0102)
        let cn = must(
            g.impact_node_checkout(&ctx, &c.id, checkout(&f.req.id))
                .await,
        );
        must(
            g.impact_node_review(&ctx, &c.id, &cn.id, REVIEW_REJECTED, "bob", "not like this")
                .await,
        );
        assert!(
            must(draft_of(g, &c.id, "", &cn.id).await).is_some(),
            "a rejected impact keeps its draft"
        );
        let cn = must(
            g.impact_node_update(
                &ctx,
                &c.id,
                &cn.id,
                NodeUpdate {
                    properties: props(&[("title", json!("Use PSP v2"))]),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(cn.review, REVIEW_PROPOSED, "reworked, it is proposed again");

        // removed: the draft goes, the node is back to its version, the impact leaves the change
        must(g.withdraw_impact(&ctx, &c.id, &cn.id, "", "").await);
        let latest = must(
            g.node(
                &ctx,
                &NodeRef {
                    id: f.req.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(latest.version, f.req.version, "the draft is dropped");
        let got = must(g.change(&ctx, &c.id).await);
        assert!(
            got.nodes.is_empty(),
            "the impact leaves the change: {:?}",
            got.nodes
        );

        // a creation removed before it is accepted leaves no node
        let created = must(
            g.impact_node_create(&ctx, &c.id, create("REQ-NEW", "Requirement", "new"))
                .await,
        );
        must(g.withdraw_impact(&ctx, &c.id, &created.id, "", "").await);
        assert_kind(
            g.node_by_key(&ctx, "", "REQ-NEW").await,
            ErrorKind::NotFound,
            "the created node is gone",
        );

        // an accepted impact keeps its draft until the change lands: it is withdrawn like any other
        let cn = must(
            g.impact_node_checkout(&ctx, &c.id, checkout(&f.test.id))
                .await,
        );
        touch(g, &c.id, &cn.id).await;
        must(accept_impact(g, &c.id, &cn.id, "").await);
        must(g.withdraw_impact(&ctx, &c.id, &cn.id, "", "").await);
        assert_kind(
            apply(g, &c.id, "").await,
            ErrorKind::Invalid,
            "a change left with nothing that modifies a node is not committed (ADR 0102)",
        );
    })
    .await;
}

// Several impacts are withdrawn at once, in one transaction: all of them or none; one named twice is withdrawn once.
#[tokio::test]
async fn withdraw_impacts() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, change_in(&f.base, "", "bulk")).await);
        let req = must(
            g.impact_node_checkout(&ctx, &c.id, checkout(&f.req.id))
                .await,
        );
        let test = must(
            g.impact_node_checkout(&ctx, &c.id, checkout(&f.test.id))
                .await,
        );
        let created = must(
            g.impact_node_create(&ctx, &c.id, create("REQ-NEW", "Requirement", "new"))
                .await,
        );

        assert_kind(
            g.withdraw_impacts(&ctx, &c.id, &[], "", "").await,
            ErrorKind::Invalid,
            "nothing to withdraw is refused",
        );
        assert!(
            g.withdraw_impacts(&ctx, &c.id, &[req.id.clone(), "CI-UNKNOWN".into()], "", "")
                .await
                .is_err(),
            "an unknown impact refuses the whole batch"
        );
        assert_eq!(
            must(g.change(&ctx, &c.id).await).nodes.len(),
            3,
            "a refused batch withdraws nothing"
        );

        must(
            g.withdraw_impacts(
                &ctx,
                &c.id,
                &[req.id.clone(), created.id.clone(), req.id.clone()],
                "",
                "",
            )
            .await,
        );
        let got = must(g.change(&ctx, &c.id).await);
        assert!(
            got.nodes.len() == 1 && got.nodes[0].id == test.id,
            "only the impact left out of the batch stays: {:?}",
            got.nodes
        );
        assert_kind(
            g.node_by_key(&ctx, "", "REQ-NEW").await,
            ErrorKind::NotFound,
            "the created node is gone",
        );
    })
    .await;
}

// The links a type requires (the parent of a unit, ADR 0054) are checked when the draft is accepted and when the version
// lands, whatever operations built it; a draft may be on its way there.
#[tokio::test]
async fn required_links_at_accept() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let g = r.graph();
        must(g.bootstrap(&ctx).await);
        let root = must(g.node_by_key(&ctx, "organisation", &root_org(&g)).await);
        let head = must(g.branch_head(&ctx, "organisation", MAIN_BRANCH).await);
        let c = must(
            g.create_change(&ctx, change_in(&head, "organisation", "unit"))
                .await,
        );
        let cn = must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    properties: props(&[("name", json!("X"))]),
                    ..create("ORG-X", NODE_TYPE_ORG_UNIT, "new unit")
                },
            )
            .await,
        );
        assert_kind(
            g.impact_node_review(&ctx, &c.id, &cn.id, REVIEW_ACCEPTED, "bob", "ok")
                .await,
            ErrorKind::Invalid,
            "a unit without a parent is not accepted",
        );
        assert!(
            must(draft_of(&g, &c.id, "", &cn.id).await).is_some(),
            "a refused accept leaves the draft"
        );
        must(
            g.impact_link_create(
                &ctx,
                &c.id,
                &cn.id,
                LinkWrite {
                    r#type: LINK_PART_OF.into(),
                    to: root.r#ref(),
                    ..Default::default()
                },
                "",
                "",
            )
            .await,
        );
        must(accept_impact(&g, &c.id, &cn.id, "").await);
        must(apply(&g, &c.id, "").await);
    })
    .await;
}

// The attributes of a link type are checked like the ones of a node type: on every link edit.
#[tokio::test]
async fn link_attributes() {
    let ctx = ctx();
    let d = goap_def::parse_domain(
        br#"
name: docs
version: 1.0.0
enums:
  - {name: strength, values: [{value: weak}, {value: strong}]}
nodeTypes:
  - {name: Req, attributes: [title]}
  - {name: Test, attributes: [title]}
linkTypes:
  - {name: verifies, from: Test, to: Req, attributes: [{name: strength, type: enum, enum: strength}]}
"#,
    )
    .expect("domain");
    let cat = goap_typecat::new(&[d]).expect("catalogue");
    let mut g = memory_repo().graph();
    use_types(&mut g, Arc::new(cat));
    let head = must(g.branch_head(&ctx, "docs", MAIN_BRANCH).await);
    let c = must(g.create_change(&ctx, change_in(&head, "docs", "t")).await);
    let req = must(
        g.impact_node_create(&ctx, &c.id, create("R1", "docs@Req", "r"))
            .await,
    );
    let tst = must(
        g.impact_node_create(&ctx, &c.id, create("T1", "docs@Test", "t"))
            .await,
    );
    let link = |strength: &str| LinkWrite {
        r#type: "docs@verifies".into(),
        to: req.post.clone().expect("a draft"),
        properties: props(&[("strength", json!(strength))]),
    };
    assert_kind(
        g.impact_link_create(&ctx, &c.id, &tst.id, link("huge"), "", "")
            .await,
        ErrorKind::Invalid,
        "a link property outside its enum",
    );
    let l = must(
        g.impact_link_create(&ctx, &c.id, &tst.id, link("weak"), "", "")
            .await,
    );
    assert_kind(
        g.impact_link_update(
            &ctx,
            &c.id,
            &l.id,
            props(&[("strength", json!("huge"))]),
            "",
            "",
        )
        .await,
        ErrorKind::Invalid,
        "a link property updated outside its enum",
    );
    must(
        g.impact_link_update(
            &ctx,
            &c.id,
            &l.id,
            props(&[("strength", json!("strong"))]),
            "",
            "",
        )
        .await,
    );
}

// A node no parent holds is retired by its lifecycle, never deleted (ADR 0076 §4c); restoring it is a state set in the same
// commit as its edits (no move first, ADR 0078).
#[tokio::test]
async fn retire_and_restore_by_commit() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut g = r.graph();
        use_types(&mut g, goap_typecat::builtin());
        let commit = async |g: &Graph, edits: Vec<NodeEdit>| {
            let head = must(g.branch_head(&ctx, "organisation", MAIN_BRANCH).await);
            must(
                g.commit(
                    &ctx,
                    Commit {
                        project_id: "PROJ-ROOT".into(),
                        namespace: "organisation".into(),
                        title: "policy".into(),
                        baseline: head.id,
                        by: "t".into(),
                        edits,
                        ..Default::default()
                    },
                )
                .await,
            );
        };
        must(g.bootstrap(&ctx).await);
        commit(
            &g,
            vec![NodeEdit {
                key: "POL:x".into(),
                r#type: "organisation@Policy".into(),
                props: props(&[
                    ("rule", json!("true")),
                    ("resource", json!("x")),
                    ("action", json!("read")),
                ]),
                ..Default::default()
            }],
        )
        .await;
        let pol = must(g.node_by_key(&ctx, "organisation", "POL:x").await);
        assert_eq!(pol.state, "active", "created in force: {pol:?}");
        commit(
            &g,
            vec![NodeEdit {
                pre: Some(pol.r#ref()),
                state: "retired".into(),
                ..Default::default()
            }],
        )
        .await;
        let pol = must(g.node_by_key(&ctx, "organisation", "POL:x").await);
        assert_eq!(pol.state, "retired", "retired: {pol:?}");
        commit(
            &g,
            vec![NodeEdit {
                pre: Some(pol.r#ref()),
                state: "active".into(),
                props: props(&[("action", json!("write"))]),
                ..Default::default()
            }],
        )
        .await;
        let pol = must(g.node_by_key(&ctx, "organisation", "POL:x").await);
        assert!(
            pol.state == "active" && pol.properties.get("action") == Some(&json!("write")),
            "restored and edited: {pol:?}"
        );
    })
    .await;
}
