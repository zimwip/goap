//! The admin-floor validator through a real graph (ADR 0048).

mod common;

use std::sync::Arc;

use common::*;
use goap_access as access;
use goap_domain as domain;
use goap_graph::{Commit, Graph, LinkEdit, NodeEdit};
use serde_json::{Map, Value};

/// floorGraph returns a graph judged by the domains of the repository, with the admin-floor validator wired the way
/// cmd/goap-dev and cmd/graph wire it (ADR 0048).
fn floor_graph() -> Graph {
    let mut g = typed_graph();
    g.validators = vec![Arc::new(access::AdminFloorValidator)];
    g
}

async fn deactivate_alice(g: &Graph) -> goap_graph::Result<()> {
    let alice = g
        .node_by_key(
            &ctx(),
            access::NAMESPACE_ORGANISATION,
            &access::user_key("alice"),
        )
        .await
        .unwrap();
    let head = g
        .branch_head(&ctx(), access::NAMESPACE_ORGANISATION, domain::MAIN_BRANCH)
        .await
        .unwrap();
    Box::pin(g.commit(
        &ctx(),
        Commit {
            project_id: "PROJ-ROOT".into(),
            namespace: access::NAMESPACE_ORGANISATION.into(),
            title: "deactivate alice".into(),
            baseline: head.id,
            edits: vec![NodeEdit {
                pre: Some(alice.r#ref()),
                state: "deactivated".into(),
                rationale: "test".into(),
                ..Default::default()
            }],
            ..Default::default()
        },
    ))
    .await
    .map(|_| ())
}

// TestAdminFloorBootstrapNotRejected is implicit in every call to EnsureUser below.
#[tokio::test]
async fn admin_floor_rejects_removing_last_admin() {
    let g = floor_graph();
    g.bootstrap(&ctx()).await.unwrap();
    Box::pin(goap_graphsvc::ensure_user(&ctx(), &g, "alice"))
        .await
        .unwrap();
    let alice = g
        .node_by_key(
            &ctx(),
            access::NAMESPACE_ORGANISATION,
            &access::user_key("alice"),
        )
        .await
        .unwrap();
    assert_eq!(alice.state, "active", "alice should be active");
    let err = deactivate_alice(&g).await.unwrap_err();
    assert!(
        err.is_invalid() && err.to_string().contains("no active administrator"),
        "expected admin-floor rejection, got {err}"
    );
    let n = g
        .node_by_key(
            &ctx(),
            access::NAMESPACE_ORGANISATION,
            &access::user_key("alice"),
        )
        .await
        .unwrap();
    assert_eq!(n.state, "active", "alice must stay active");
}

// Deactivating the only administrator while granting admin to another active user, in the same commit, succeeds.
#[tokio::test]
async fn admin_floor_allows_handoff_in_one_commit() {
    let g = floor_graph();
    g.bootstrap(&ctx()).await.unwrap();
    Box::pin(goap_graphsvc::ensure_user(&ctx(), &g, "alice"))
        .await
        .unwrap();
    Box::pin(goap_graphsvc::ensure_user(&ctx(), &g, "bob"))
        .await
        .unwrap();
    let ns = access::NAMESPACE_ORGANISATION;
    let alice = g
        .node_by_key(&ctx(), ns, &access::user_key("alice"))
        .await
        .unwrap();
    let bob = g
        .node_by_key(&ctx(), ns, &access::user_key("bob"))
        .await
        .unwrap();
    let head = g
        .branch_head(&ctx(), ns, domain::MAIN_BRANCH)
        .await
        .unwrap();
    let asg = access::Assignment {
        roles: strings(&[access::ROLE_ADMIN]),
        description: "bob becomes administrator".into(),
    };
    Box::pin(g.commit(
        &ctx(),
        Commit {
            project_id: "PROJ-ROOT".into(),
            namespace: ns.into(),
            title: "admin handoff".into(),
            baseline: head.id,
            edits: vec![
                NodeEdit {
                    key: access::platform_assignment_key(&access::user_key("bob")),
                    r#type: access::NODE_TYPE_ASSIGNMENT.into(),
                    props: asg.props(),
                    links: vec![LinkEdit {
                        r#type: access::LINK_ASSIGNS_ORG.into(),
                        to: Some(bob.r#ref()),
                        ..Default::default()
                    }],
                    rationale: "test".into(),
                    ..Default::default()
                },
                NodeEdit {
                    pre: Some(alice.r#ref()),
                    state: "deactivated".into(),
                    rationale: "test".into(),
                    ..Default::default()
                },
            ],
            ..Default::default()
        },
    ))
    .await
    .expect("handoff should succeed");
    let n = g
        .node_by_key(&ctx(), ns, &access::user_key("alice"))
        .await
        .unwrap();
    assert_eq!(n.state, "deactivated", "alice should be deactivated");
}

async fn floor_setup(edits: Vec<NodeEdit>) -> Graph {
    let g = floor_graph();
    g.bootstrap(&ctx()).await.unwrap();
    Box::pin(goap_graphsvc::ensure_user(&ctx(), &g, "alice"))
        .await
        .unwrap();
    Box::pin(g.commit(
        &ctx(),
        Commit {
            project_id: "PROJ-ROOT".into(),
            namespace: access::NAMESPACE_ORGANISATION.into(),
            title: "bob".into(),
            intent: "bob".into(),
            by: "test".into(),
            edits,
            ..Default::default()
        },
    ))
    .await
    .unwrap();
    g
}

fn bob(unit: &str) -> NodeEdit {
    NodeEdit {
        key: access::user_key("bob"),
        r#type: access::NODE_TYPE_USER.into(),
        props: access::User {
            subject: "bob".into(),
            ..Default::default()
        }
        .props(),
        state: "active".into(),
        rationale: "test".into(),
        links: vec![LinkEdit {
            r#type: access::LINK_MEMBER_OF.into(),
            to_key: unit.into(),
            ..Default::default()
        }],
        ..Default::default()
    }
}

// The floor counts the administrators the way Snapshot.Enrich grants the role.
#[tokio::test]
async fn admin_floor_counts_what_enrich_grants() {
    // bob is an administrator through the Assignment of the unit he belongs to
    let asg = access::Assignment {
        roles: strings(&[access::ROLE_ADMIN]),
        ..Default::default()
    };
    let mut ops = Map::new();
    ops.insert("name".into(), Value::from("Ops"));
    let g = floor_setup(vec![
        NodeEdit {
            key: "ORG-OPS".into(),
            r#type: access::NODE_TYPE_ORG_UNIT.into(),
            props: ops,
            rationale: "test".into(),
            links: vec![LinkEdit {
                r#type: access::LINK_PART_OF.into(),
                to_key: access::DEFAULT_ORG.into(),
                ..Default::default()
            }],
            ..Default::default()
        },
        bob("ORG-OPS"),
        NodeEdit {
            key: access::platform_assignment_key("ORG-OPS"),
            r#type: access::NODE_TYPE_ASSIGNMENT.into(),
            props: asg.props(),
            rationale: "test".into(),
            links: vec![LinkEdit {
                r#type: access::LINK_ASSIGNS_ORG.into(),
                to_key: "ORG-OPS".into(),
                ..Default::default()
            }],
            ..Default::default()
        },
    ])
    .await;
    deactivate_alice(&g)
        .await
        .expect("a unit-held administrator must count");

    // a plain second user is no administrator: alice stays
    let g = floor_setup(vec![bob(access::DEFAULT_ORG)]).await;
    let err = deactivate_alice(&g).await.unwrap_err();
    assert!(err.is_invalid(), "a plain user must not count: {err}");
}
