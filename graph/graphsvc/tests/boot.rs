mod common;

use common::typed_graph;
use goap_access as access;
use goap_authz::FLOOR_POLICIES;
use goap_ctx::Ctx;
use goap_domain::NAMESPACE_PLATFORM;
use goap_graph::Graph;
use goap_graphsvc::{BoxError, BoxFuture, ModelsConfig, Options, Report, boot, dev_fn};
use goap_llmcfg as llmcfg;
use goap_mcpbuiltin as mcpbuiltin;

fn boot_models() -> ModelsConfig {
    ModelsConfig {
        providers: vec![llmcfg::Provider {
            name: "fake".into(),
            kind: "fake".into(),
            ..Default::default()
        }],
        ..Default::default()
    }
}

/// demo stands for the development data a composition passes as Options::dev (platform/devseed, which the graph's
/// tests do not import).
async fn demo(ctx: &Ctx, g: &Graph) -> goap_graph::Result<()> {
    goap_graphtest::import(
        ctx,
        g,
        goap_graphtest::Node {
            namespace: "alm".into(),
            key: "NEED-1".into(),
            r#type: "alm@Need".into(),
            properties: obj! {"title" => "Pay online"},
            ..Default::default()
        },
    )
    .await
    .map(|_| ())
}

/// Boot writes the platform and nothing else: roots, floor policy, built-in MCPs and roles, models; a second call
/// writes nothing; no demo data (the alm namespace stays empty) until the dev seed runs.
#[tokio::test]
async fn boot_writes_the_platform() {
    let ctx = &Ctx::background();
    let g = typed_graph();
    let o = Options {
        models: boot_models(),
        ..Default::default()
    };
    let r = Box::pin(boot(ctx, &g, &o)).await.unwrap();
    assert!(r.access && r.builtins && r.models, "report = {r:?}");
    for (ns, key) in [
        (
            access::NAMESPACE_ORGANISATION,
            access::DEFAULT_ORG.to_string(),
        ),
        (
            access::NAMESPACE_ORGANISATION,
            access::DEFAULT_PROJECT.to_string(),
        ),
        (
            access::NAMESPACE_ORGANISATION,
            access::policy_key(&FLOOR_POLICIES[0]),
        ),
        (NAMESPACE_PLATFORM, goap_mcp::mcp_key(mcpbuiltin::GRAPH)),
        (
            NAMESPACE_PLATFORM,
            goap_adapter::def_key(&mcpbuiltin::adapter_defs()[0].name),
        ),
        (NAMESPACE_PLATFORM, access::role_key(access::ROLE_ADMIN)),
        (NAMESPACE_PLATFORM, llmcfg::provider_key("fake")),
    ] {
        g.node_by_key(ctx, ns, &key)
            .await
            .unwrap_or_else(|e| panic!("{ns}/{key}: {e}"));
    }
    // the document repository and the demo are not platform defaults
    assert!(
        g.node_by_key(
            ctx,
            NAMESPACE_PLATFORM,
            &goap_mcp::mcp_key("document-repository")
        )
        .await
        .is_err(),
        "Boot seeded the document-repository MCP"
    );
    assert!(
        g.node_by_key(ctx, "alm", "NEED-1").await.is_err(),
        "Boot seeded the demo"
    );
    let cs = g.changes(ctx).await.unwrap();
    let again = Box::pin(boot(ctx, &g, &o)).await.unwrap();
    assert_eq!(again, Report::default(), "second boot");
    assert_eq!(
        g.changes(ctx).await.unwrap().len(),
        cs.len(),
        "a second boot wrote changes"
    );
    Box::pin(demo(ctx, &g)).await.unwrap();
    g.node_by_key(ctx, "alm", "NEED-1")
        .await
        .expect("development data after Boot");
}

fn dev<'a>(ctx: &'a Ctx, g: &'a Graph) -> BoxFuture<'a, Result<(), BoxError>> {
    Box::pin(async move {
        g.node_by_key(
            ctx,
            access::NAMESPACE_ORGANISATION,
            &access::policy_key(&FLOOR_POLICIES[0]),
        )
        .await?;
        demo(ctx, g).await?;
        Ok(())
    })
}

/// Options::dev runs after the platform steps.
#[tokio::test]
async fn boot_runs_dev_last() {
    let ctx = &Ctx::background();
    let g = typed_graph();
    let o = Options {
        dev: Some(dev_fn(dev)),
        ..Default::default()
    };
    Box::pin(boot(ctx, &g, &o)).await.unwrap();
    g.node_by_key(ctx, "alm", "NEED-1").await.unwrap();
}

/// A composition that serves callers may not boot before its hooks are set.
#[tokio::test]
#[should_panic]
async fn boot_requires_hooks() {
    let o = Options {
        require_hooks: true,
        ..Default::default()
    };
    let _ = Box::pin(boot(&Ctx::background(), &typed_graph(), &o)).await;
}
