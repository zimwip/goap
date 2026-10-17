//! versions_test.go.

use std::sync::Arc;

use goap_def::NodeType;

use super::suspend::{alm_domain, ref_meth};
use super::{FakeGraph, as_, must_yaml, registry, ticking_clock};
use crate::graphstore::GraphStore;
use crate::service::Service;
use crate::store::{Record, STATUS_PUBLISHED};

pub(crate) fn versioned() -> (Arc<Service>, Arc<goap_domainsvc::Service>) {
    registry(Arc::new(GraphStore::new(FakeGraph::new())), true, |s| {
        s.now = Some(ticking_clock())
    })
}

// A new change takes the latest published version of its methodology, a change keeps the version it names (ADR 0113).
#[tokio::test]
async fn methodology_version_of_a_change() {
    let (s, d) = versioned();
    let ctx = as_(&["admin"]);
    assert_eq!(
        s.methodology_version(&ctx, "uses").await.expect("unknown"),
        ""
    );
    d.import_domain(&ctx, &must_yaml(&alm_domain("1")), true)
        .await
        .expect("alm");
    let mut m = ref_meth("uses");
    s.import_published(&ctx, m.clone()).await.expect("1");
    m.version = "2".into();
    m.description = "second".into();
    s.import_published(&ctx, m.clone()).await.expect("2");
    m.version = "3".into();
    s.save(&ctx, m).await.expect("3");
    for (r, want) in [("uses", "2"), ("uses@1", "1"), ("uses@2", "2")] {
        assert_eq!(s.methodology_version(&ctx, r).await.expect(r), want, "{r}");
    }
    for r in ["uses@3", "uses@9"] {
        let err = s.methodology_version(&ctx, r).await.err().expect(r);
        assert!(err.is_graph_invalid(), "{r}: {err}");
    }
    // the readers of a methodology take the reference
    let c = s.methodology(&ctx, "uses@1").await.expect("1");
    assert_eq!(c.definition().version, "1");
    let c = s.methodology(&ctx, "uses").await.expect("latest");
    assert_eq!(c.definition().version, "2");
}

// Every start publishes the versions of the shipped files not stored yet (ADR 0113, 0125 §3).
#[tokio::test]
async fn seed_loads_new_versions() {
    let (s, d) = versioned();
    let ctx = as_(&["admin"]);
    let tmp = std::env::temp_dir().join(format!("goap-registrysvc-seed-{}", std::process::id()));
    let (ddir, mdir) = (tmp.join("domains"), tmp.join("methodologies"));
    std::fs::create_dir_all(&ddir).expect("mkdir");
    std::fs::create_dir_all(&mdir).expect("mkdir");
    let result = async {
        std::fs::write(ddir.join("alm.yaml"), must_yaml(&alm_domain("1"))).expect("write");
        std::fs::write(
            mdir.join("uses.yaml"),
            ref_meth("uses").yaml().expect("yaml"),
        )
        .expect("write");
        let seed = |want_d: usize, want_m: usize| {
            let (s, d, ctx, ddir, mdir) = (
                s.clone(),
                d.clone(),
                ctx.clone(),
                ddir.clone(),
                mdir.clone(),
            );
            async move {
                let ds = d.seed_domains(&ctx, &ddir).await.expect("domains");
                assert_eq!(ds.len(), want_d, "domains: {ds:?}");
                let ms = s
                    .seed(&ctx, &mdir)
                    .await
                    .map_err(|e| e.error)
                    .expect("methodologies");
                assert_eq!(ms.len(), want_m, "methodologies: {ms:?}");
            }
        };
        seed(1, 1).await;
        seed(0, 0).await; // nothing new: nothing loaded

        // alm@2 drops the link type uses@1 relies on, uses@2 no longer needs it
        let mut v2 = alm_domain("2");
        v2.schema.link_types.clear();
        v2.schema.node_types.push(NodeType {
            name: "Need".into(),
            ..NodeType::default()
        });
        std::fs::write(ddir.join("alm.yaml"), must_yaml(&v2)).expect("write");
        let mut m2 = ref_meth("uses");
        m2.version = "2".into();
        m2.conditions[0].expr =
            r#"changeImpacts.exists(n, n.hasPost && n.post.type == "alm@Need")"#.into();
        std::fs::write(mdir.join("uses.yaml"), m2.yaml().expect("yaml")).expect("write");
        seed(1, 1).await;

        assert_eq!(
            s.methodology_version(&ctx, "uses").await.expect("version"),
            "2"
        );
        let err = s
            .methodology(&ctx, "uses@1")
            .await
            .err()
            .expect("suspended");
        assert!(
            err.is_suspended(),
            "version 1 relies on a link type alm@2 dropped: {err}"
        );
        let err = s
            .methodology_version(&ctx, "uses@1")
            .await
            .err()
            .expect("refused");
        assert!(
            err.is_graph_invalid() && err.is_suspended(),
            "no change starts on a suspended version: {err}"
        );
        let cur = s.methodology(&ctx, "uses").await.expect("current");
        assert!(
            cur.definition()
                .types
                .as_ref()
                .is_some_and(|t| t.has_node_type("alm@Need"))
        );
        let r = s.get(&ctx, "uses", "2").await.expect("get");
        assert_eq!(
            r.methodology.domains.get("alm").map(String::as_str),
            Some("2"),
            "pins"
        );
    }
    .await;
    let _ = std::fs::remove_dir_all(&tmp);
    result
}

// A methodology published before the pins existed follows the domain versions in force when it was published.
#[tokio::test]
async fn unpinned_published_methodology_follows_its_time() {
    let (s, d) = versioned();
    let ctx = as_(&["admin"]);
    d.import_domain(&ctx, &must_yaml(&alm_domain("1")), true)
        .await
        .expect("alm@1");
    let at = chrono::Utc::now(); // the clock of the domain registry
    s.store
        .save(
            &ctx,
            Record {
                methodology: ref_meth("legacy"),
                status: STATUS_PUBLISHED,
                created_at: Some(at),
                updated_at: Some(at),
                published_at: Some(at),
                ..Record::default()
            },
        )
        .await
        .expect("save");
    let mut v2 = alm_domain("2");
    v2.schema.node_types.push(NodeType {
        name: "Need".into(),
        ..NodeType::default()
    });
    d.import_domain(&ctx, &must_yaml(&v2), true)
        .await
        .expect("alm@2");
    let c = s.methodology(&ctx, "legacy").await.expect("compiled");
    let t = c.definition().types.clone().expect("types");
    assert!(
        t.has_link_type("alm@verifies") && !t.has_node_type("alm@Need"),
        "compiled against alm@1"
    );
    let mut v3 = alm_domain("3");
    v3.schema.link_types.clear();
    d.import_domain(&ctx, &must_yaml(&v3), true)
        .await
        .expect("a domain is published whatever the methodologies");
    let err = s
        .methodology(&ctx, "legacy")
        .await
        .err()
        .expect("suspended");
    assert!(err.is_suspended(), "alm@3 breaks it: {err}");
}

// A draft pins only what it declares; a pin must name a published version of a stored domain.
#[tokio::test]
async fn domain_pins() {
    let (s, d) = versioned();
    let ctx = as_(&["admin"]);
    d.import_domain(&ctx, &must_yaml(&alm_domain("1")), true)
        .await
        .expect("alm@1");
    let mut m = ref_meth("pins");
    for (pin, path) in [
        (("alm", "7"), "domains.alm"),
        (("organisation", "1"), "domains.organisation"),
    ] {
        m.domains = [(pin.0.to_string(), pin.1.to_string())]
            .into_iter()
            .collect();
        let (_, issues) = s.save(&ctx, m.clone()).await.expect("save");
        assert!(
            !issues.0.is_empty() && issues.0[0].path == path,
            "{path}: {issues}"
        );
    }
    m.domains = [("alm".to_string(), "1".to_string())].into_iter().collect();
    let (_, issues) = s.save(&ctx, m).await.expect("save");
    assert!(!issues.has_errors(), "pinned on alm@1: {issues}");
}
