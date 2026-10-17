use std::sync::Arc;

use goap_access as access;
use goap_adapter as adapter;
use goap_ctx::Ctx;
use goap_domain::{NAMESPACE_PLATFORM, NodeID};
use goap_graph::{Graph, Memory};
use goap_graphsvc as graphsvc;
use goap_mcp as mcp;

/// typedGraph is a graph judged by the domains of the repository (ADR 0012).
fn typed_graph() -> Graph {
    let ds = goap_def::load_domains("../../domains").expect("load the domains");
    let cat = Arc::new(goap_typecat::new(&ds).expect("catalogue"));
    let mut g = Graph::new(Arc::new(Memory::new()));
    g.set_types(move || Some(cat.clone() as Arc<dyn goap_graph::TypeCatalog>));
    g
}

fn ctx() -> Ctx {
    Ctx::background()
}

/// A fresh temporary directory (Go's t.TempDir).
fn temp_dir(name: &str) -> String {
    let d = std::env::temp_dir().join(format!(
        "goap-devseed-{name}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&d).unwrap();
    d.to_string_lossy().into_owned()
}

// The seeds only write nodes and links the domains of the repository declare.
#[tokio::test]
async fn seeds_follow_the_domains() {
    let ctx = &ctx();
    let g = typed_graph();
    goap_devseed::demo(ctx, &g).await.unwrap();
    let again = goap_devseed::demo(ctx, &g).await.unwrap();
    assert!(!again, "the demo is seeded once");
    graphsvc::seed_access(ctx, &g).await.unwrap();
    g.bootstrap(ctx).await.unwrap();
    let unit = g
        .node_by_key(ctx, "organisation", "ORG-CHECKOUT")
        .await
        .unwrap();
    assert_eq!(unit.r#type, access::NODE_TYPE_ORG_UNIT);
    let app = g.node_by_key(ctx, "alm", "APP-1").await.unwrap();
    assert_eq!(app.r#type, "alm@Application");
    assert_eq!(
        app.owner, unit.id,
        "APP-1 must be owned by ORG-CHECKOUT across namespaces: {app:?}"
    );
    let uv = g.view(ctx, &unit.r#ref()).await.unwrap();
    assert!(
        uv.out.len() == 1 && uv.out[0].r#type == access::LINK_PART_OF,
        "unit hierarchy: {:?}",
        uv.out
    );
}

// The demo seed runs after the bootstrap (ADR 0054): its organisation hangs under the root unit from the start, and
// every one of its writes is an applied change held by a unit and acting in a project.
#[tokio::test]
async fn seed_demo_hangs_under_the_root() {
    let ctx = &ctx();
    let g = typed_graph();
    goap_devseed::demo(ctx, &g).await.unwrap();
    let def = g
        .node_by_key(ctx, "organisation", access::DEFAULT_ORG)
        .await
        .unwrap();
    let acme = g
        .node_by_key(ctx, "organisation", "ORG-ACME")
        .await
        .unwrap();
    let v = g.view(ctx, &acme.r#ref()).await.unwrap();
    assert!(
        v.out.len() == 1 && v.out[0].r#type == access::LINK_PART_OF && v.out[0].to.id == def.id,
        "ORG-ACME must be part_of ORG-DEFAULT: {:?}",
        v.out
    );
    assert!(
        acme.owner == def.id && !acme.change_id.is_empty() && !acme.project.is_empty(),
        "ORG-ACME is owned by the root unit, written by a change, created in a project: {acme:?}"
    );
    for c in g.changes(ctx).await.unwrap() {
        assert!(
            !c.owner_org.is_empty() && !c.project_id.is_empty(),
            "change {} {:?} names no unit or no project",
            c.id,
            c.title
        );
    }
}

// The demo import is one change per namespace (the organisation, the alm data with its links), not one per node.
#[tokio::test]
async fn seed_demo_is_one_change_per_namespace() {
    let ctx = &ctx();
    let g = typed_graph();
    goap_devseed::demo(ctx, &g).await.unwrap();
    let imports: Vec<String> = g
        .changes(ctx)
        .await
        .unwrap()
        .into_iter()
        .filter(|c| c.title.starts_with("Import "))
        .map(|c| c.title)
        .collect();
    assert_eq!(
        imports.len(),
        2,
        "the demo import is 2 changes, got {imports:?}"
    );
    let cmp = g.node_by_key(ctx, "alm", "CMP-1").await.unwrap();
    let out = g.out_links_of(ctx, &cmp.r#ref()).await.unwrap();
    assert!(!out.is_empty(), "the links come with the import");
}

// The document repository is development data: seeded once, with its adapter definition, and a directory becomes the
// default organisation's repository only when asked.
#[tokio::test]
async fn document_repository_and_local_fs() {
    let ctx = &ctx();
    let g = typed_graph();
    g.bootstrap(ctx).await.unwrap();
    assert!(
        goap_devseed::document_repository(ctx, &g).await.unwrap(),
        "first seed"
    );
    assert!(
        !goap_devseed::document_repository(ctx, &g).await.unwrap(),
        "second seed"
    );
    for key in [
        mcp::mcp_key("document-repository"),
        adapter::def_key(goap_devseed::LOCAL_FS_ADAPTER_NAME),
    ] {
        g.node_by_key(ctx, NAMESPACE_PLATFORM, &key)
            .await
            .unwrap_or_else(|e| panic!("{key}: {e}"));
    }
    for i in 0..2 {
        let dir = temp_dir(&format!("localfs{i}"));
        goap_devseed::local_fs(ctx, &g, &dir).await.unwrap();
        let _ = std::fs::remove_dir_all(&dir);
    }
    g.node_by_key(
        ctx,
        access::NAMESPACE_ORGANISATION,
        &adapter::key(access::DEFAULT_ORG, "document-repository"),
    )
    .await
    .unwrap();
}

// The demo project hangs under the root project, is bound to the demo organisation by an assignment, and the alm data
// of the demo acts in it.
#[tokio::test]
async fn seed_demo_project_bound_to_the_organisation() {
    let ctx = &ctx();
    let g = typed_graph();
    goap_devseed::demo(ctx, &g).await.unwrap();
    let project = g
        .node_by_key(
            ctx,
            access::NAMESPACE_ORGANISATION,
            goap_devseed::DEMO_PROJECT,
        )
        .await
        .unwrap();
    assert_eq!(project.r#type, access::NODE_TYPE_PROJECT_UNIT);
    let asg = g
        .node_by_key(
            ctx,
            access::NAMESPACE_ORGANISATION,
            &access::assignment_key("ORG-ACME", goap_devseed::DEMO_PROJECT),
        )
        .await
        .unwrap();
    let acme = g
        .node_by_key(ctx, access::NAMESPACE_ORGANISATION, "ORG-ACME")
        .await
        .unwrap();
    let out = g.out_links_of(ctx, &asg.r#ref()).await.unwrap();
    let to: std::collections::HashMap<String, NodeID> = out
        .iter()
        .map(|l| (l.r#type.clone(), l.to.id.clone()))
        .collect();
    assert!(
        to.get(access::LINK_ASSIGNS_ORG) == Some(&acme.id)
            && to.get(access::LINK_ASSIGNS_PROJECT) == Some(&project.id),
        "the assignment binds ORG-ACME to the demo project: {out:?}"
    );
    let need = g.node_by_key(ctx, "alm", "NEED-1").await.unwrap();
    assert_eq!(
        need.project, project.id,
        "the alm data acts in the demo project"
    );
}

// A graph holding the demo of before the demo project gains the project, and nothing else is written again.
#[tokio::test]
async fn seed_demo_adds_the_project_to_an_older_demo() {
    let ctx = &ctx();
    let g = typed_graph();
    g.bootstrap(ctx).await.unwrap();
    for u in [
        ["ORG-ACME", "company", ""],
        ["ORG-DIGITAL", "direction", "ORG-ACME"],
        ["ORG-CHECKOUT", "team", "ORG-DIGITAL"],
        ["ORG-CRM", "team", "ORG-DIGITAL"],
        ["ORG-FINANCE", "team", "ORG-DIGITAL"],
        ["ORG-SECURITY", "team", "ORG-DIGITAL"],
    ] {
        graphsvc::seed_unit(ctx, &g, u[0], u[0], u[1], u[2])
            .await
            .unwrap();
    }
    goap_devseed::demo(ctx, &g).await.unwrap();
    g.node_by_key(
        ctx,
        access::NAMESPACE_ORGANISATION,
        &access::assignment_key("ORG-ACME", goap_devseed::DEMO_PROJECT),
    )
    .await
    .unwrap();
    let again = goap_devseed::demo(ctx, &g).await.unwrap();
    assert!(!again, "seeded once");
}

// DemoLoader reports the demo project and whether it loaded (no directory given).
#[tokio::test]
async fn demo_loader_reports_the_project() {
    let ctx = &ctx();
    let g = typed_graph();
    let load = goap_devseed::demo_loader(None);
    let (loaded, project) = load(ctx, &g).await.unwrap();
    assert!(loaded);
    assert_eq!(project, goap_devseed::DEMO_PROJECT);
    let (loaded, _) = load(ctx, &g).await.unwrap();
    assert!(!loaded);
}
