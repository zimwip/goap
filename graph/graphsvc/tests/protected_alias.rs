//! Port of `protected_alias_test.go`.

mod common;

use std::sync::Arc;

use common::{client, serve, typed_graph};
use goap_connect::Interceptors;
use goap_ctx::Ctx;
use goap_domain::{NAMESPACE_PLATFORM, Node, REVIEW_ACCEPTED};
use goap_graph::{Graph, NewChange, NodeCreate, NodeEdit, NodeUpdate};
use goap_graphapi::Port;
use goap_graphsvc::{
    Handler, ModelsConfig, Options, Report, boot, seed_change, seed_models, seed_node, system,
};
use goap_llmcfg as llmcfg;

async fn alias_of(g: &Graph, name: &str) -> (llmcfg::Alias, Node) {
    let n = g
        .node_by_key(
            &Ctx::background(),
            NAMESPACE_PLATFORM,
            &llmcfg::alias_key(name),
        )
        .await
        .unwrap_or_else(|e| panic!("alias {name}: {e}"));
    let a = llmcfg::alias_from_props(&n.properties).unwrap();
    (a, n)
}

fn models_with_aliases() -> ModelsConfig {
    let model = |m: &str| llmcfg::Model {
        provider: "fake".into(),
        model: m.into(),
        enabled: true,
        ..Default::default()
    };
    let alias = |a: &str, t: &str| llmcfg::Alias {
        alias: a.into(),
        target: t.into(),
        ..Default::default()
    };
    ModelsConfig {
        providers: vec![llmcfg::Provider {
            name: "fake".into(),
            kind: "fake".into(),
            protocol: "fake".into(),
            enabled: true,
            ..Default::default()
        }],
        models: vec![model("echo"), model("small")],
        aliases: vec![alias("default", "fake/echo"), alias("fast", "fake/small")],
    }
}

fn with_models() -> Options {
    Options {
        models: models_with_aliases(),
        ..Default::default()
    }
}

/// Boot gives every install the protected aliases: the assistant like default, the helper like fast; a second boot
/// writes nothing.
#[tokio::test]
async fn boot_seeds_protected_aliases() {
    let ctx = &Ctx::background();
    let g = typed_graph();
    let r = Box::pin(boot(ctx, &g, &with_models())).await.unwrap();
    assert!(r.aliases, "boot = {r:?}");
    let (a, _) = alias_of(&g, llmcfg::ASSISTANT_ALIAS).await;
    assert!(a.protected && a.target == "fake/echo", "assistant = {a:?}");
    let (a, _) = alias_of(&g, llmcfg::HELPER_ALIAS).await;
    assert!(a.protected && a.target == "fake/small", "helper = {a:?}");
    let cs = g.changes(ctx).await.unwrap();
    let again = Box::pin(boot(ctx, &g, &with_models())).await.unwrap();
    assert_eq!(again, Report::default(), "second boot");
    assert_eq!(
        g.changes(ctx).await.unwrap().len(),
        cs.len(),
        "a second boot wrote changes"
    );
}

/// With no model configured the aliases exist, target nothing, and the snapshot keeps them without a problem.
#[tokio::test]
async fn protected_aliases_without_models() {
    let ctx = &Ctx::background();
    let g = typed_graph();
    Box::pin(boot(ctx, &g, &Options::default())).await.unwrap();
    let (a, n) = alias_of(&g, llmcfg::ASSISTANT_ALIAS).await;
    assert!(a.protected && a.target.is_empty(), "assistant = {a:?}");
    let s = llmcfg::build_snapshot(&Default::default(), &[n], &[]);
    assert!(
        s.problems.is_empty() && s.aliases.len() == 1,
        "snapshot = {s:?}"
    );
}

/// A graph that predates the aliases (models seeded, no protected alias) gets them at the next boot.
#[tokio::test]
async fn protected_aliases_on_existing_graph() {
    let ctx = &Ctx::background();
    let g = typed_graph();
    g.bootstrap(ctx).await.unwrap();
    let cfg = models_with_aliases();
    Box::pin(seed_models(
        ctx,
        &g,
        &cfg.providers,
        &cfg.models,
        &cfg.aliases,
    ))
    .await
    .unwrap();
    assert!(
        g.node_by_key(
            ctx,
            NAMESPACE_PLATFORM,
            &llmcfg::alias_key(llmcfg::ASSISTANT_ALIAS)
        )
        .await
        .is_err(),
        "alias present before the seed"
    );
    let r = Box::pin(boot(ctx, &g, &with_models())).await.unwrap();
    assert!(r.aliases, "boot = {r:?}");
    let (a, _) = alias_of(&g, llmcfg::HELPER_ALIAS).await;
    assert!(a.protected && a.target == "fake/small", "helper = {a:?}");
}

fn alias_node(a: &llmcfg::Alias) -> NodeEdit {
    seed_node(
        &llmcfg::alias_key(&a.alias),
        llmcfg::NODE_TYPE_ALIAS,
        a.props(),
    )
}

/// A change may retarget a protected alias, never retire it, rename it or drop its flag.
#[tokio::test]
async fn protected_alias_guard() {
    let ctx = &system(&Ctx::background());
    let mut g = typed_graph();
    g.validators = vec![Arc::new(llmcfg::ProtectedAliasValidator)];
    Box::pin(boot(ctx, &g, &with_models())).await.unwrap();
    let edit = async |g: &Graph, mut e: NodeEdit| {
        let (_, n) = alias_of(g, llmcfg::ASSISTANT_ALIAS).await;
        e.pre = Some(n.r#ref());
        Box::pin(seed_change(ctx, g, NAMESPACE_PLATFORM, "edit", vec![e])).await
    };
    let cases: Vec<(&str, NodeEdit)> = vec![
        (
            "retire",
            NodeEdit {
                state: "retired".into(),
                ..Default::default()
            },
        ),
        (
            "rename",
            NodeEdit {
                props: obj! {"alias" => "other"},
                ..Default::default()
            },
        ),
        (
            "flag clear",
            NodeEdit {
                props: obj! {"protected" => null},
                ..Default::default()
            },
        ),
        (
            "flag false",
            NodeEdit {
                props: obj! {"protected" => false},
                ..Default::default()
            },
        ),
        (
            "rename+move",
            NodeEdit {
                props: obj! {"alias" => "x"},
                state: "retired".into(),
                ..Default::default()
            },
        ),
    ];
    for (name, e) in cases {
        let err = edit(&g, e).await;
        assert!(
            matches!(&err, Err(e) if e.is_invalid()),
            "{name}: err = {err:?}, want invalid"
        );
    }
    edit(
        &g,
        NodeEdit {
            props: obj! {"target" => "fake/small"},
            ..Default::default()
        },
    )
    .await
    .expect("retarget");
    let (a, n) = alias_of(&g, llmcfg::ASSISTANT_ALIAS).await;
    assert!(
        a.target == "fake/small" && a.protected && n.state != "retired",
        "assistant = {a:?} ({})",
        n.state
    );
    // a plain alias stays free
    let mine = llmcfg::Alias {
        alias: "mine".into(),
        target: "fake/echo".into(),
        ..Default::default()
    };
    Box::pin(seed_change(
        ctx,
        &g,
        NAMESPACE_PLATFORM,
        "plain",
        vec![alias_node(&mine)],
    ))
    .await
    .unwrap();
    let (_, n) = alias_of(&g, "mine").await;
    let retire = NodeEdit {
        pre: Some(n.r#ref()),
        state: "retired".into(),
        ..Default::default()
    };
    Box::pin(seed_change(
        ctx,
        &g,
        NAMESPACE_PLATFORM,
        "retire",
        vec![retire],
    ))
    .await
    .expect("retire a plain alias");
    // the platform names cannot be created unprotected
    let mut g2 = typed_graph();
    g2.validators = g.validators.clone();
    g2.bootstrap(ctx).await.unwrap();
    let helper = llmcfg::Alias {
        alias: "helper".into(),
        target: "fake/echo".into(),
        ..Default::default()
    };
    let err = Box::pin(seed_change(
        ctx,
        &g2,
        NAMESPACE_PLATFORM,
        "bad",
        vec![alias_node(&helper)],
    ))
    .await;
    assert!(
        matches!(&err, Err(e) if e.is_invalid()),
        "unprotected helper: {err:?}"
    );
}

/// Boot gives every install the built-in terse behaviour, enabled; an administrator's edit or retirement of it is
/// kept (the seed never overrides an existing node), and a second boot writes nothing (ADR 0093).
#[tokio::test]
async fn boot_seeds_terse_behavior_once() {
    let ctx = &Ctx::background();
    let g = typed_graph();
    let r = Box::pin(boot(ctx, &g, &with_models())).await.unwrap();
    assert!(r.behaviors, "boot = {r:?}");
    let n = g
        .node_by_key(ctx, NAMESPACE_PLATFORM, &llmcfg::behavior_key("terse"))
        .await
        .unwrap();
    let b = llmcfg::behavior_from_props(&n.properties);
    assert!(
        matches!(&b, Ok(b) if b.enabled && b.instruction.contains("Answer tersely")),
        "{b:?}"
    );
    let retire = NodeEdit {
        pre: Some(n.r#ref()),
        state: llmcfg::STATE_RETIRED.into(),
        rationale: "r".into(),
        ..Default::default()
    };
    Box::pin(seed_change(
        ctx,
        &g,
        NAMESPACE_PLATFORM,
        "retire",
        vec![retire],
    ))
    .await
    .unwrap();
    let cs = g.changes(ctx).await.unwrap();
    let again = Box::pin(boot(ctx, &g, &with_models())).await.unwrap();
    assert_eq!(again, Report::default(), "second boot");
    assert_eq!(
        g.changes(ctx).await.unwrap().len(),
        cs.len(),
        "a second boot wrote changes"
    );
    let n2 = g
        .node_by_key(ctx, NAMESPACE_PLATFORM, &llmcfg::behavior_key("terse"))
        .await
        .unwrap();
    assert_eq!(
        n2.state,
        llmcfg::STATE_RETIRED,
        "the seed restored a retired behaviour"
    );
}

/// The domain refuses an alias with no usable target when its draft is accepted or lands (validator
/// alias-needs-target of platform@LlmAlias); a protected one may stay unconfigured.
#[tokio::test]
async fn alias_needs_target() {
    let ctx = &system(&Ctx::background());
    let g = typed_graph();
    g.bootstrap(ctx).await.unwrap();
    let land = async |a: llmcfg::Alias| {
        Box::pin(seed_change(
            ctx,
            &g,
            NAMESPACE_PLATFORM,
            &format!("alias {}", a.alias),
            vec![alias_node(&a)],
        ))
        .await
    };
    let alias = |a: &str, t: &str, protected: bool| llmcfg::Alias {
        alias: a.into(),
        target: t.into(),
        protected,
    };
    for target in ["", "nomodel", "/m", "p/"] {
        let err = land(alias("bad", target, false)).await;
        assert!(
            matches!(&err, Err(e) if e.is_invalid() && e.to_string().contains("alias bad needs a target (provider/model)")),
            "target {target:?}: err = {err:?}"
        );
    }
    land(alias("unset", "", true))
        .await
        .expect("protected without target");
    land(alias("good", "p/m", false))
        .await
        .expect("valid target");
}

/// The views of a change tell, on the impact itself, that an alias with no usable target would be refused at its
/// review, in process and through the service (ImpactValidation); the review gives the same message.
#[tokio::test]
async fn alias_needs_target_shown_on_the_impact() {
    let ctx = &system(&Ctx::background());
    let g = Arc::new(typed_graph());
    g.bootstrap(ctx).await.unwrap();
    let url = serve(Handler::new(g.clone()), Interceptors::none()).await;
    let cl = client(&url);

    let c = g
        .create_change(
            ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                namespace: NAMESPACE_PLATFORM.into(),
                title: "alias".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let bad = llmcfg::Alias {
        alias: "bad".into(),
        target: "nomodel".into(),
        ..Default::default()
    };
    let cn = g
        .impact_node_create(
            ctx,
            &c.id,
            NodeCreate {
                rationale: "r".into(),
                key: llmcfg::alias_key("bad"),
                r#type: llmcfg::NODE_TYPE_ALIAS.into(),
                properties: bad.props(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let review =
        Box::pin(g.impact_node_review(ctx, &c.id, &cn.id, REVIEW_ACCEPTED, "r", "ok")).await;
    let err = review.expect_err("review");
    assert!(err.is_invalid(), "review: {err}");
    let text = err.to_string();
    let want = text.strip_suffix(": invalid").unwrap_or(&text).to_string();
    assert!(
        want.contains("alias bad needs a target (provider/model)"),
        "message = {want:?}"
    );
    let got = cl.change(ctx, &c.id).await.unwrap();
    assert_eq!(got.nodes.len(), 1, "client change = {got:?}");
    let v = got.nodes[0].validation.as_ref();
    assert!(
        matches!(v, Some(v) if !v.valid && v.message == want),
        "through the service: {v:?}, want {want:?}"
    );
    let bb = cl.blackboard(ctx, &c.id).await.unwrap();
    let v = bb.change.nodes[0].validation.as_ref();
    assert!(
        matches!(v, Some(v) if !v.valid && v.message == want),
        "blackboard through the service: {v:?}"
    );
    // fixed, the next view says so
    g.impact_node_update(
        ctx,
        &c.id,
        &cn.id,
        NodeUpdate {
            properties: obj! {"target" => "p/m"},
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let got = cl.change(ctx, &c.id).await.unwrap();
    let v = got.nodes[0].validation.as_ref();
    assert!(matches!(v, Some(v) if v.valid), "fixed: {v:?}");
}
