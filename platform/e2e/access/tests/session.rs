//! The session (ADR 0070) the web reads once.

mod common;

use common::*;
use goap_access as access;
use goap_graph::NodeEdit;

// The session is what the web reads once: the structures and names it builds nodes with, the platform roles it
// lists, and what the caller may attempt, derived by the authorizer.
#[tokio::test]
async fn session_of_admin_member_and_reader() {
    let (g, a) = setup();
    g.bootstrap(&ctx()).await.unwrap();
    goap_graphsvc::seed_unit(&ctx(), &g, "team-a", "Team A", "team", "")
        .await
        .unwrap();
    let team = g
        .node_by_key(&ctx(), access::NAMESPACE_ORGANISATION, "team-a")
        .await
        .unwrap();
    let head = g
        .branch_head(
            &ctx(),
            access::NAMESPACE_ORGANISATION,
            goap_domain::MAIN_BRANCH,
        )
        .await
        .unwrap();
    Box::pin(g.commit(
        &ctx(),
        goap_graph::Commit {
            project_id: "PROJ-ROOT".into(),
            namespace: access::NAMESPACE_ORGANISATION.into(),
            title: "reader".into(),
            baseline: head.id,
            by: "test".into(),
            baseline_name: "reader".into(),
            edits: vec![NodeEdit {
                key: access::platform_assignment_key("team-a"),
                r#type: access::NODE_TYPE_ASSIGNMENT.into(),
                props: access::Assignment {
                    roles: strings(&[access::ROLE_READER]),
                    ..Default::default()
                }
                .props(),
                rationale: "t".into(),
                links: vec![goap_graph::LinkEdit {
                    r#type: access::LINK_ASSIGNS_ORG.into(),
                    to: Some(team.r#ref()),
                    ..Default::default()
                }],
                ..Default::default()
            }],
            ..Default::default()
        },
    ))
    .await
    .unwrap();

    let admin = a
        .session(&ctx(), &principal("root", "", &[access::ROLE_ADMIN]))
        .await
        .unwrap();
    assert!(
        admin.can.administer && admin.can.approve,
        "admin: {:?}",
        admin.can
    );
    let reader = a
        .session(&ctx(), &principal("dev", "team-a", &[]))
        .await
        .unwrap();
    assert!(
        !reader.can.administer && reader.can.approve,
        "a platform reader works on the root project but administers nothing: {:?}",
        reader.can
    );
    let stranger = a
        .session(&ctx(), &principal("stranger", "other-org", &[]))
        .await
        .unwrap();
    assert!(
        !stranger.can.administer && !stranger.can.approve,
        "a caller with no role may attempt nothing: {:?}",
        stranger.can
    );

    // what every session carries
    let st = &admin.structures;
    assert_eq!(st.organisation().root, access::DEFAULT_ORG);
    assert_eq!(st.project().root, access::DEFAULT_PROJECT);
    assert!(
        admin.names.types.org_unit == st.organisation().r#type
            && admin.names.links.project_part_of == st.project().parent,
        "names do not follow the structures: {:?}",
        admin.names
    );
    let k = &admin.names.keys;
    assert!(
        k.user == access::USER_PREFIX
            && k.assignment == access::ASSIGNMENT_PREFIX
            && k.platform_scope == access::PLATFORM_SCOPE,
        "keys {k:?}"
    );
    let names: Vec<String> = admin
        .platform_roles
        .iter()
        .map(|r| r.name.clone())
        .collect();
    assert_eq!(names, *access::PLATFORM_ROLES);

    // the payload keeps the principal's own fields at the top level
    let m = serde_json::to_value(&reader).unwrap();
    assert!(
        m["subject"] == "dev"
            && m["org"] == "team-a"
            && !m["can"].is_null()
            && !m["structures"].is_null()
            && !m["platformRoles"].is_null(),
        "payload {m}"
    );
}

// pkg/mcp may not import pkg/domain (pkg/layering), so the type of an MCP is written in both: they must agree.
#[test]
fn mcp_type_agrees() {
    assert_eq!(goap_domain::TYPE_MCP, goap_mcp::NODE_TYPE_MCP);
}

#[test]
fn platform_assignment_key_is_an_assignment_key() {
    assert_eq!(
        access::platform_assignment_key("u"),
        access::assignment_key("u", access::PLATFORM_SCOPE)
    );
}
