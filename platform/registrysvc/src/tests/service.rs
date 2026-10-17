//! service_test.go.

use std::sync::Arc;

use goap_domain::ChangeImpact;
use goap_llmcfg::{NAMESPACE_PLATFORM, NODE_TYPE_ALIAS, alias_key};
use goap_methodology::{Agent, Methodology, PLANNER_LLM_SCORING};

use super::{FakeGraph, as_, bg, canon, example, registry, stores, with_alm};
use crate::graphstore::{GraphStore, StoreGraph};
use crate::store::{STATUS_ARCHIVED, STATUS_PUBLISHED};

#[tokio::test]
async fn lifecycle() {
    for (name, store) in stores() {
        let (s, d) = registry(store, true, |_| {});
        with_alm(&d).await;
        let ctx = as_(&["admin"]);
        let mut m = example();
        m.version = "2.0.0".into();

        // ABAC: contributors may not edit methodologies
        let err = s
            .save(&as_(&["contributor"]), m.clone())
            .await
            .err()
            .unwrap_or_else(|| panic!("{name}: contributor saved"));
        assert!(err.is_forbidden(), "{name}: contributor save: {err}");
        // a broken draft is stored with its issues
        let mut broken = m.clone();
        broken.conditions[0].expr = "impacts +".into();
        let (_, issues) = s.save(&ctx, broken).await.expect("save broken");
        assert!(
            !issues.0.is_empty() && issues.0[0].path == "conditions[0].expr",
            "{name}: expected an expr issue, got {issues}"
        );
        let err = s.publish_version(&ctx, &m.name, &m.version).await.err();
        assert!(
            err.as_ref().is_some_and(|e| e.is_invalid()),
            "{name}: invalid draft published: {err:?}"
        );
        // fix and publish
        let (_, issues) = s.save(&ctx, m.clone()).await.expect("save");
        assert!(issues.0.is_empty(), "{name}: save: {issues}");
        let rec = s
            .publish_version(&ctx, &m.name, &m.version)
            .await
            .expect("publish");
        assert_eq!(rec.status, STATUS_PUBLISHED, "{name}");
        // structured storage round trip
        let got = s.get(&ctx, &m.name, "").await.expect("get");
        // publishing pinned the domains in force (ADR 0113)
        assert!(
            got.methodology
                .domains
                .get("alm")
                .is_some_and(|v| !v.is_empty()),
            "{name}: published without its domain versions: {:?}",
            got.methodology.domains
        );
        let mut pinned = m.clone();
        pinned.domains = got.methodology.domains.clone();
        assert_eq!(
            canon(&got.methodology),
            canon(&pinned),
            "{name}: round trip mismatch"
        );
        got.methodology.compile().expect("compile");
        // published versions are immutable
        let err = s.save(&ctx, m.clone()).await.err();
        assert!(
            err.as_ref().is_some_and(|e| e.is_immutable()),
            "{name}: published version modified: {err:?}"
        );
        // new draft version, list, export / import
        s.create_version(&ctx, &m.name, "2.0.0", "2.1.0")
            .await
            .expect("create version");
        let all = s.versions(&ctx, true).await.expect("all");
        let latest = s.versions(&ctx, false).await.expect("latest");
        assert!(
            all.len() == 2 && latest.len() == 1 && latest[0].methodology.version == "2.0.0",
            "{name}: list: {} all, latest {}",
            all.len(),
            latest.len()
        );
        let (out, file) = s.export(&ctx, &m.name, "2.1.0").await.expect("export");
        assert_eq!(file, "impact-analysis-2.1.0.yaml", "{name}");
        s.delete(&ctx, &m.name, "2.1.0").await.expect("delete");
        s.import(&ctx, &out, true).await.expect("re-import");
        // deleting a published version archives it
        s.delete(&ctx, &m.name, "2.0.0").await.expect("archive");
        let r = s.get(&ctx, &m.name, "2.0.0").await.expect("get archived");
        assert_eq!(r.status, STATUS_ARCHIVED, "{name}");
        let r = s.get(&ctx, &m.name, "").await.expect("latest published");
        assert_eq!(r.methodology.version, "2.1.0", "{name}");
        let c = s.methodology(&ctx, &m.name).await.expect("engine port");
        assert_eq!(c.definition().version, "2.1.0", "{name}");
    }
}

#[tokio::test]
async fn agents_and_scripts_round_trip() {
    for (name, store) in stores() {
        let (s, d) = registry(store, false, |_| {});
        d.seed_domains(&as_(&["admin"]), "../../domains")
            .await
            .expect("seed domains");
        let data = std::fs::read("../../methodologies/examples/test-design.yaml").expect("read");
        s.import(&as_(&["admin"]), &data, true)
            .await
            .expect("import");
        let want = Methodology::parse(&data).expect("parse");
        let got = s.get(&bg(), "test-design", "").await.expect("get");
        assert_eq!(
            canon(&got.methodology.agents),
            canon(&want.agents),
            "{name}: agents not stored"
        );
        assert_eq!(
            got.methodology.actions[1].code, want.actions[1].code,
            "{name}"
        );
        assert_eq!(
            got.methodology.actions[3].utility, want.actions[3].utility,
            "{name}"
        );
        let all = s.list(&bg()).await.expect("list");
        assert!(
            all.len() == 1 && all[0].agent_list().len() == want.agents.len(),
            "{name}: engine port list"
        );
    }
}

async fn pending_alias_impacts(g: &FakeGraph, alias: &str) -> Vec<ChangeImpact> {
    let changes = g.changes(&bg()).await.expect("changes");
    let key = alias_key(alias);
    changes
        .into_iter()
        .filter(|c| c.namespace == NAMESPACE_PLATFORM)
        .flat_map(|c| c.nodes)
        .filter(|cn| cn.key == key && cn.r#type == NODE_TYPE_ALIAS)
        .collect()
}

fn with_model(name: &str, version: &str, model: &str) -> Methodology {
    let mut m = example();
    m.name = name.into();
    m.version = version.into();
    let mut a = Agent::default();
    a.name = "plannertest".into();
    a.planner = PLANNER_LLM_SCORING.into();
    a.model = model.into();
    m.agents = vec![a];
    m
}

#[tokio::test]
async fn publish_stubs_missing_alias() {
    let g = FakeGraph::new();
    let (s, d) = registry(Arc::new(GraphStore::new(g.clone())), false, |_| {});
    with_alm(&d).await;
    let ctx = as_(&["admin"]);
    let m = with_model(&example().name, "9.0.0", "unconfigured-test-alias");
    let src = m.yaml().expect("yaml");
    s.import(&ctx, src.as_bytes(), true).await.expect("import");
    let impacts = pending_alias_impacts(&g, "unconfigured-test-alias").await;
    assert!(
        impacts.len() == 1
            && impacts[0].review == goap_domain::REVIEW_PROPOSED
            && impacts[0].intent == goap_domain::INTENT_CREATED,
        "expected one pending alias impact, got {impacts:?}"
    );

    // republishing a new version referencing the same missing alias does not duplicate the pending stub
    let mut m2 = m.clone();
    m2.version = "9.1.0".into();
    let src2 = m2.yaml().expect("yaml");
    s.import(&ctx, src2.as_bytes(), true).await.expect("import");
    let impacts = pending_alias_impacts(&g, "unconfigured-test-alias").await;
    assert_eq!(impacts.len(), 1, "the pending stub is not duplicated");
}

// Two methodologies referencing the same missing alias, published concurrently, leave exactly one open stub.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn publish_stubs_missing_alias_once() {
    let g = FakeGraph::new();
    let (s, d) = registry(Arc::new(GraphStore::new(g.clone())), false, |_| {});
    with_alm(&d).await;
    let mut tasks = Vec::new();
    for name in ["stub-once-a", "stub-once-b"] {
        let src = with_model(name, "9.0.0", "shared-test-alias")
            .yaml()
            .expect("yaml");
        let s = s.clone();
        tasks.push(tokio::spawn(async move {
            s.import(&as_(&["admin"]), src.as_bytes(), true).await
        }));
    }
    for t in tasks {
        t.await.expect("join").expect("import");
    }
    let impacts = pending_alias_impacts(&g, "shared-test-alias").await;
    assert_eq!(impacts.len(), 1, "exactly one open stub: {impacts:?}");
}
