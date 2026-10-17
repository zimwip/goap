//! compilecache_test.go.

use std::sync::Arc;

use goap_connect::{Code, Request};
use goap_gen::registry::v1::{self as pb, RegistryService};
use goap_methodology::{Action, Condition, KIND_HUMAN, Process, Step};

use super::{as_, example, memory, registry, with_alm};
use crate::handler::Handler;
use crate::service::Service;

async fn cached_service() -> (Arc<Service>, Arc<goap_domainsvc::Service>) {
    let (s, d) = registry(memory(), true, |_| {});
    with_alm(&d).await;
    let m = example();
    let (_, issues) = s.save(&as_(&["admin"]), m.clone()).await.expect("save");
    assert!(issues.0.is_empty(), "save: {issues}");
    s.publish_version(&as_(&["admin"]), &m.name, &m.version)
        .await
        .expect("publish");
    (s, d)
}

// A second read of a methodology is the compile of the first; a new version, a new domain version and a change of
// the draft in place each make the next read compile again.
#[tokio::test]
async fn methodology_compile_is_cached() {
    let (s, d) = cached_service().await;
    let m = example();
    let ctx = as_(&["admin"]);
    let before = s.compiled.count();
    let c1 = s.methodology(&ctx, &m.name).await.expect("first");
    let c2 = s.methodology(&ctx, &m.name).await.expect("second");
    assert!(
        Arc::ptr_eq(&c1, &c2),
        "second read must be the cached compile"
    );
    assert_eq!(s.compiled.count() - before, 1, "compiles");
    let list = s.list(&ctx).await.expect("list");
    assert!(
        list.len() == 1 && Arc::ptr_eq(&list[0], &c1),
        "List shares the cache"
    );
    assert_eq!(s.compiled.count() - before, 1, "compiles after List");

    // a new published version
    s.create_version(&ctx, &m.name, &m.version, "2")
        .await
        .expect("create version");
    s.publish_version(&ctx, &m.name, "2")
        .await
        .expect("publish");
    let c3 = s.methodology(&ctx, &m.name).await.expect("third");
    assert!(
        !Arc::ptr_eq(&c3, &c1) && c3.definition().version == "2",
        "a published version is compiled afresh"
    );

    // a domain publish (its check of the published versions compiles them against the new types, ADR 0125 §3)
    let n = s.compiled.count();
    d.import_domain(&ctx, b"name: other\nversion: \"1\"\nnodeTypes: [X]\n", true)
        .await
        .expect("domain");
    let c4 = s.methodology(&ctx, &m.name).await.expect("fourth");
    assert!(
        !Arc::ptr_eq(&c4, &c3) && s.compiled.count() > n,
        "a domain publish invalidates"
    );
}

// Parallel readers share one cache.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn methodology_cache_concurrent_readers() {
    let (s, _d) = cached_service().await;
    let name = example().name;
    let mut tasks = Vec::new();
    for _ in 0..16 {
        let s = s.clone();
        let name = name.clone();
        tasks.push(tokio::spawn(async move {
            for _ in 0..20 {
                s.methodology(&as_(&["admin"]), &name).await.expect("read");
            }
        }));
    }
    for t in tasks {
        t.await.expect("join");
    }
}

// A draft with a warning only (nothing can start on an empty change) is saved with the warning and publishes.
#[tokio::test]
async fn warnings_do_not_block_publish() {
    let (s, d) = registry(memory(), true, |_| {});
    with_alm(&d).await;
    let mut m = example();
    m.name = "stuck".into();
    m.goal = "p".into();
    for n in ["wk_a", "wk_c"] {
        m.conditions.push(Condition {
            name: n.into(),
            expr: "false".into(),
            ..Condition::default()
        });
    }
    let mut x = Action::default();
    x.name = "wk_x".into();
    x.kind = KIND_HUMAN.into();
    x.pre.insert("wk_c".into(), true);
    x.effects.insert("wk_a".into(), true);
    m.actions.push(x);
    m.processes = vec![Process {
        name: "p".into(),
        steps: vec![Step {
            name: "one".into(),
            action: "wk_x".into(),
            ..Step::default()
        }],
        ..Process::default()
    }];
    let (_, issues) = s.save(&as_(&["admin"]), m.clone()).await.expect("save");
    let w = issues.warnings();
    assert!(
        !issues.has_errors() && w.0.len() == 1 && w.0[0].path == "goal",
        "save: {issues}"
    );
    s.publish_version(&as_(&["admin"]), &m.name, &m.version)
        .await
        .expect("a warning must not block the publish");
}

// The view and the flow of a stored version are read by its name and version (ADR 0126 step 4).
#[tokio::test]
async fn stored_version_view_and_flow() {
    let (s, _d) = cached_service().await;
    let m = example();
    let h = Handler::new(s);
    let ctx = as_(&["admin"]);
    let v = h
        .get_methodology_view(
            &ctx,
            Request::new(pb::GetMethodologyViewRequest {
                name: m.name.clone(),
                version: m.version.clone(),
                ..Default::default()
            }),
        )
        .await
        .expect("view");
    let view = v.msg.view.expect("a view");
    assert!(view.methodology == m.name && view.version == m.version);
    if let Some(p) = m.processes.first() {
        let g = h
            .get_process_graph(
                &ctx,
                Request::new(pb::GetProcessGraphRequest {
                    name: m.name.clone(),
                    version: m.version.clone(),
                    process: p.name.clone(),
                    ..Default::default()
                }),
            )
            .await
            .expect("graph");
        assert!(g.msg.graph.is_some());
    }
    let err = h
        .get_methodology_view(
            &ctx,
            Request::new(pb::GetMethodologyViewRequest {
                name: "nope".into(),
                version: "1".into(),
                ..Default::default()
            }),
        )
        .await
        .err()
        .expect("an error");
    assert_eq!(
        err.code(),
        Code::NotFound,
        "an unknown version is not found: {err}"
    );
}
