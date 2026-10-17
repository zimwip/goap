//! Port of `seed_test.go`.

mod common;

use common::{client, serve, typed_graph};
use goap_access as access;
use goap_connect::Interceptors;
use goap_ctx::Ctx;
use goap_domain::{MAIN_BRANCH, NAMESPACE_PLATFORM};
use goap_graph::{Commit, NodeEdit};
use goap_graphapi::Port;
use goap_graphsvc::{Handler, seed_builtins};
use goap_mcpbuiltin as mcpbuiltin;
use std::sync::Arc;

/// The built-in MCPs follow the code at every start; the instances of the default organisation are seeded once, so
/// that removing one sticks (ADR 0028).
#[tokio::test]
async fn seed_builtins_follow_the_code() {
    let ctx = &Ctx::background();
    let g = typed_graph();
    g.bootstrap(ctx).await.unwrap();
    assert!(
        Box::pin(seed_builtins(ctx, &g)).await.unwrap(),
        "first seed"
    );
    assert!(
        !Box::pin(seed_builtins(ctx, &g)).await.unwrap(),
        "second seed"
    );
    let commit = async |ns: &str, e: NodeEdit| {
        let head = g.branch_head(ctx, ns, MAIN_BRANCH).await.unwrap();
        Box::pin(g.commit(
            ctx,
            Commit {
                project_id: "PROJ-ROOT".into(),
                namespace: ns.into(),
                title: "edit".into(),
                intent: "edit".into(),
                baseline: head.id,
                by: "test".into(),
                edits: vec![e],
                ..Default::default()
            },
        ))
        .await
        .unwrap();
    };
    // an older platform: the MCP lacks a tool and says something else
    let n = g
        .node_by_key(
            ctx,
            NAMESPACE_PLATFORM,
            &goap_mcp::mcp_key(mcpbuiltin::GRAPH),
        )
        .await
        .unwrap();
    commit(
        NAMESPACE_PLATFORM,
        NodeEdit {
            pre: Some(n.r#ref()),
            props: obj! {"description" => "old", "tools" => []},
            rationale: "older".into(),
            ..Default::default()
        },
    )
    .await;
    // an administrator disables the admin MCP for the default organisation (a node is never removed by a change,
    // ADR 0076)
    let a = g
        .node_by_key(
            ctx,
            access::NAMESPACE_ORGANISATION,
            &goap_adapter::key(access::DEFAULT_ORG, mcpbuiltin::ADMIN),
        )
        .await
        .unwrap();
    commit(
        access::NAMESPACE_ORGANISATION,
        NodeEdit {
            pre: Some(a.r#ref()),
            props: obj! {"disabled" => true},
            rationale: "no admin tools".into(),
            ..Default::default()
        },
    )
    .await;

    assert!(Box::pin(seed_builtins(ctx, &g)).await.unwrap(), "resync");
    let head = g
        .branch_head(ctx, NAMESPACE_PLATFORM, MAIN_BRANCH)
        .await
        .unwrap();
    let (nodes, _) = g.baseline_graph(ctx, &head.id).await.unwrap();
    let want = &mcpbuiltin::defs()[0];
    for n in &nodes {
        if n.key == goap_mcp::mcp_key(mcpbuiltin::GRAPH) {
            let d = goap_mcp::def_from_props(&n.properties);
            assert!(
                matches!(&d, Ok(d) if d.description == want.description && d.tools.len() == want.tools.len()),
                "goap-graph not resynced: {d:?}"
            );
        }
    }
    let org_head = g
        .branch_head(ctx, access::NAMESPACE_ORGANISATION, MAIN_BRANCH)
        .await
        .unwrap();
    let (org_nodes, _) = g.baseline_graph(ctx, &org_head.id).await.unwrap();
    for n in &org_nodes {
        if n.key == goap_adapter::key(access::DEFAULT_ORG, mcpbuiltin::ADMIN) {
            assert_eq!(
                n.properties.get("disabled"),
                Some(&serde_json::Value::Bool(true)),
                "the disabled instance was seeded again: {:?}",
                n.properties
            );
        }
    }
}

/// The root project is seeded like ORG-DEFAULT, self-linked project_part_of, and seeding is idempotent.
#[tokio::test]
async fn root_project_seeded() {
    let ctx = &Ctx::background();
    let g = typed_graph();
    g.bootstrap(ctx).await.unwrap();
    let root = g
        .node_by_key(ctx, access::NAMESPACE_ORGANISATION, access::DEFAULT_PROJECT)
        .await
        .unwrap();
    assert_eq!(
        root.r#type,
        access::NODE_TYPE_PROJECT_UNIT,
        "root project = {root:?}"
    );
    let v = g.view(ctx, &root.r#ref()).await.unwrap();
    assert!(
        v.out.len() == 1
            && v.out[0].r#type == access::LINK_PROJECT_PART_OF
            && v.out[0].to.id == root.id,
        "the root project must link project_part_of to itself: {:?}",
        v.out
    );
    let before = g.changes(ctx).await.unwrap();
    g.bootstrap(ctx)
        .await
        .expect("bootstrapping twice must not fail");
    assert_eq!(
        g.changes(ctx).await.unwrap().len(),
        before.len(),
        "bootstrapping twice must not write again"
    );
}

/// The services reading the organisation learn its structures from the graph service (ADR 0054): the client returns
/// what the graph's catalogue tags, a User being a unit.
#[tokio::test]
async fn structures_through_the_service() {
    let g = Arc::new(typed_graph());
    let url = serve(Handler::new(g), Interceptors::none()).await;
    let got = client(&url).structures(&Ctx::background()).await.unwrap();
    assert_eq!(got, goap_typecat::builtin().structures(), "structures");
}
