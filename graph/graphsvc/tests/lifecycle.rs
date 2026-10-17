//! Port of `lifecycle_test.go`.

mod common;

use std::sync::Arc;

use common::{as_, casbin, catalog_of, code, graph, typed_graph, with_types};
use goap_access as access;
use goap_authz::{Principal, with_principal};
use goap_connect::Code;
use goap_ctx::Ctx;
use goap_domain::MAIN_BRANCH;
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graph::{Commit, NewChange, NodeEdit};
use goap_graphsvc::{Handler, ensure_user, transition_authorizer};
use goap_pbconv as pbconv;

/// A User's deactivate/reactivate transitions (ADR 0048) require the admin role, like any other permission-gated
/// transition: the floor policy (hasRole(admin) -> "*","*") grants it, nobody else gets it by default.
#[tokio::test]
async fn user_deactivate_reactivate_require_admin() {
    let ctx = &Ctx::background();
    let mut g = typed_graph();
    g.authorizer = Some(transition_authorizer(Some(casbin())));
    g.bootstrap(ctx).await.unwrap();
    Box::pin(ensure_user(ctx, &g, "carol")).await.unwrap();
    let mv = async |p: Principal, title: &str, state: &str| {
        let head = g
            .branch_head(ctx, access::NAMESPACE_ORGANISATION, MAIN_BRANCH)
            .await
            .unwrap();
        let carol = g
            .node_by_key(
                ctx,
                access::NAMESPACE_ORGANISATION,
                &access::user_key("carol"),
            )
            .await
            .unwrap();
        Box::pin(g.commit(
            &with_principal(ctx, p),
            Commit {
                project_id: "PROJ-ROOT".into(),
                namespace: access::NAMESPACE_ORGANISATION.into(),
                title: title.into(),
                baseline: head.id,
                edits: vec![NodeEdit {
                    pre: Some(carol.r#ref()),
                    state: state.into(),
                    rationale: "test".into(),
                    ..Default::default()
                }],
                ..Default::default()
            },
        ))
        .await
    };
    let mallory = Principal {
        subject: "mallory".into(),
        ..Default::default()
    };
    let admin = Principal {
        subject: "admin1".into(),
        roles: vec!["admin".into()],
        ..Default::default()
    };
    let state_of_carol = async || {
        g.node_by_key(
            ctx,
            access::NAMESPACE_ORGANISATION,
            &access::user_key("carol"),
        )
        .await
        .unwrap()
        .state
    };
    let r = mv(mallory.clone(), "deactivate carol", "deactivated").await;
    assert!(
        r.as_ref().is_err_and(goap_rpcerr::is_forbidden),
        "a non-admin must not deactivate a user: {r:?}"
    );
    mv(admin.clone(), "deactivate carol", "deactivated")
        .await
        .expect("an admin may deactivate a user");
    assert_eq!(
        state_of_carol().await,
        "deactivated",
        "carol should be deactivated"
    );
    let r = mv(mallory, "reactivate carol", "active").await;
    assert!(
        r.as_ref().is_err_and(goap_rpcerr::is_forbidden),
        "a non-admin must not reactivate a user: {r:?}"
    );
    mv(admin, "reactivate carol", "active")
        .await
        .expect("an admin may reactivate a user");
    assert_eq!(
        state_of_carol().await,
        "active",
        "carol should be active again"
    );
}

#[tokio::test]
async fn lifecycle_is_enforced_by_the_service() {
    let ctx = &Ctx::background();
    let mut g = graph();
    g.authorizer = Some(transition_authorizer(Some(casbin())));
    with_types(
        &mut g,
        catalog_of(
            r#"
name: docs
version: 1.0.0
lifecycles:
  - name: req
    initial: released
    states: [{name: draft, notLandable: true}, {name: released}]
    transitions:
      - {name: reopen, from: released, to: draft}
      - {name: release, from: draft, to: released, permission: "requirement:release"}
nodeTypes:
  - {name: Req, lifecycle: req, attributes: [title]}
  - {name: Note}
"#,
        ),
    );
    let g = Arc::new(g);
    let h = Handler {
        authz: Some(casbin()),
        ..Handler::new(g.clone())
    };
    let req = Box::pin(goap_graphtest::import(
        ctx,
        &g,
        goap_graphtest::Node {
            namespace: "docs".into(),
            key: "REQ-1".into(),
            r#type: "docs@Req".into(),
            properties: obj! {"title" => "a"},
            state: "released".into(),
            ..Default::default()
        },
    ))
    .await
    .unwrap();
    let base = g.branch_head(ctx, "docs", MAIN_BRANCH).await.unwrap();
    // reopen, edit, release: "release" needs requirement:release
    let c = g
        .create_change(
            ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                namespace: "docs".into(),
                title: "t".into(),
                baseline_id: base.id,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let cid = c.id.to_string();
    let ar = as_(
        pb::ProposeImpactRequest {
            change_id: cid.clone(),
            nodes: vec![pb::ChangeImpact {
                intent: "modified".into(),
                pre: Some(pbconv::ref_to_pb(&req.r#ref())),
                rationale: "release".into(),
                ..Default::default()
            }],
        },
        "u",
        "contributor",
    );
    let added = h
        .propose_impact(ctx, ar)
        .await
        .expect("anyone may propose the release");
    let cn_id = added.msg.nodes[0].id.clone();
    let mv = async |roles: &str, state: &str| {
        let r = as_(
            pb::ImpactNodeTransitionRequest {
                change_id: cid.clone(),
                change_impact_id: cn_id.clone(),
                state: state.into(),
                ..Default::default()
            },
            "u",
            roles,
        );
        Box::pin(h.impact_node_transition(ctx, r)).await.map(|_| ())
    };
    // a transition is authorized for whoever takes it, when it is taken (ADR 0076)
    mv("contributor", "draft")
        .await
        .expect("a contributor may reopen");
    // the transition checked the node out: a second checkout is refused, the caller updates the draft
    let co = as_(
        pb::ImpactNodeCheckoutRequest {
            change_id: cid.clone(),
            change_impact_id: cn_id.clone(),
            ..Default::default()
        },
        "u",
        "contributor",
    );
    let r = h.impact_node_checkout(ctx, co).await;
    assert!(
        r.is_err() || matches!(code(&r), Some(Code::FailedPrecondition | Code::Aborted)),
        "a node checked out by a transition is not checked out again: {r:?}"
    );
    let up = as_(
        pb::ImpactNodeUpdateRequest {
            change_id: cid.clone(),
            change_impact_id: cn_id.clone(),
            props: pbconv::to_struct(&obj! {"title" => "b"}),
            ..Default::default()
        },
        "u",
        "contributor",
    );
    h.impact_node_update(ctx, up)
        .await
        .expect("a contributor may edit");
    let rv = as_(
        pb::ImpactNodeReviewRequest {
            change_id: cid.clone(),
            change_impact_id: cn_id.clone(),
            accept: true,
            comment: "ok".into(),
            ..Default::default()
        },
        "u",
        "contributor",
    );
    Box::pin(h.impact_node_review(ctx, rv)).await.unwrap();
    let r = mv("contributor", "released").await;
    assert_eq!(
        code(&r),
        Some(Code::PermissionDenied),
        "a contributor lacks requirement:release: {r:?}"
    );
    // the release is taken on the draft: one version for the checkout, the edit and the moves, written at landing
    mv("admin", "released").await.expect("admin may release");
    let r = as_(
        pb::ApplyChangeRequest {
            change_id: cid.clone(),
            ..Default::default()
        },
        "u",
        "contributor",
    );
    Box::pin(h.apply_change(ctx, r)).await.unwrap();
    let n = g.node_by_key(ctx, "docs", "REQ-1").await;
    assert!(
        matches!(&n, Ok(n) if n.state == "released" && n.version == 2 && n.properties.get("title") == Some(&serde_json::json!("b"))),
        "REQ-1: {n:?}"
    );
}
