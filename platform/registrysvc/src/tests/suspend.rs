//! suspend_test.go.

use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_def::{Domain, LinkType, NodeType, Schema};
use goap_engine::{Payload, Publisher};
use goap_methodology::{Action, Condition, Goal, KIND_HUMAN, Methodology};

use super::{FakeGraph, as_, must_yaml, registry, ticking_clock};
use crate::graphstore::GraphStore;

pub(crate) fn alm_domain(version: &str) -> Domain {
    let mut title = goap_def::Attribute::default();
    title.name = "title".into();
    Domain {
        name: "alm".into(),
        version: version.into(),
        schema: Schema {
            node_types: vec![
                NodeType {
                    name: "Requirement".into(),
                    ..NodeType::default()
                },
                NodeType {
                    name: "TestCase".into(),
                    attributes: vec![title],
                    ..NodeType::default()
                },
            ],
            link_types: vec![LinkType {
                name: "verifies".into(),
                from: "TestCase".into(),
                to: "Requirement".into(),
                ..LinkType::default()
            }],
            ..Schema::default()
        },
        ..Domain::default()
    }
}

/// refMeth acts on alm and relies on its link type alm@verifies.
pub(crate) fn ref_meth(name: &str) -> Methodology {
    let mut a = Action::default();
    a.name = "a".into();
    a.kind = KIND_HUMAN.into();
    a.effects.insert("c".into(), true);
    Methodology {
        name: name.into(),
        version: "1".into(),
        namespace: "alm".into(),
        conditions: vec![Condition {
            name: "c".into(),
            expr: r#"changeImpacts.exists(n, n.hasPost && n.post.out.exists(l, l.type == "alm@verifies"))"#
                .into(),
            ..Condition::default()
        }],
        actions: vec![a],
        goals: vec![Goal {
            name: "g".into(),
            pre: [("c".to_string(), true)].into_iter().collect(),
            ..Goal::default()
        }],
        ..Methodology::default()
    }
}

/// subjects records the subjects published.
#[derive(Default)]
struct Subjects(parking_lot::Mutex<Vec<String>>);

#[async_trait]
impl Publisher for Subjects {
    async fn publish(&self, _: &Ctx, subject: &str, v: &Payload<'_>) -> goap_engine::Result<()> {
        let ev = v.json();
        let s = |k: &str| {
            ev.get(k)
                .and_then(|x| x.as_str())
                .unwrap_or_default()
                .to_string()
        };
        self.0.lock().push(format!(
            "{}:{}@{}",
            subject
                .strip_prefix("goap.registry.methodology.")
                .unwrap_or(subject),
            s("name"),
            s("version")
        ));
        Ok(())
    }
}

impl Subjects {
    fn take(&self) -> Vec<String> {
        std::mem::take(&mut *self.0.lock())
    }
}

// A methodology is validated and compiled against the published domains of the domain registry (ADR 0125 §3), pins
// the versions it was published against (ADR 0113), and lists as a user of the domain.
#[tokio::test]
async fn methodologies_follow_the_domain_registry() {
    let (s, d) = registry(Arc::new(GraphStore::new(FakeGraph::new())), true, |_| {});
    let ctx = as_(&["admin"]);
    d.save_domain(&ctx, alm_domain("1"))
        .await
        .expect("save domain");
    // a draft domain does not count
    let (_, issues) = s.save(&ctx, ref_meth("uses")).await.expect("save");
    assert!(!issues.0.is_empty(), "no published alm domain yet");
    let err = s.publish_version(&ctx, "uses", "1").await.err();
    assert!(
        err.as_ref().is_some_and(|e| e.is_invalid()),
        "publish on a draft domain: {err:?}"
    );
    d.publish_domain(&ctx, "alm", "1")
        .await
        .expect("publish domain");
    s.publish_version(&ctx, "uses", "1")
        .await
        .expect("publish methodology");
    let c = s.methodology(&ctx, "uses").await.expect("compiled");
    assert!(
        c.definition()
            .types
            .as_ref()
            .is_some_and(|t| t.has_link_type("alm@verifies")),
        "the compiled methodology sees the types in force"
    );
    let rec = s.get(&ctx, "uses", "1").await.expect("get");
    assert_eq!(
        rec.methodology.domains.get("alm").map(String::as_str),
        Some("1"),
        "pinned at publication"
    );
    let users = s.domain_usage(&ctx, "alm", "1").await.expect("usage");
    assert!(
        users.len() == 1 && users[0].methodology.name == "uses",
        "usage"
    );

    // a compatible version reaches no published methodology: it keeps compiling against its pin
    let mut v2 = alm_domain("2");
    v2.schema.node_types.push(NodeType {
        name: "Need".into(),
        ..NodeType::default()
    });
    d.import_domain(&ctx, &must_yaml(&v2), true)
        .await
        .expect("alm@2");
    let c = s.methodology(&ctx, "uses").await.expect("compiled");
    assert!(
        !c.definition()
            .types
            .as_ref()
            .is_some_and(|t| t.has_node_type("alm@Need")),
        "the methodology compiles against its pinned version"
    );
    // a new version of the methodology moves to the latest domains
    s.create_version(&ctx, "uses", "1", "2")
        .await
        .expect("create version");
    s.publish_version(&ctx, "uses", "2")
        .await
        .expect("publish 2");
    let c = s.methodology(&ctx, "uses").await.expect("compiled");
    assert!(
        c.definition().version == "2"
            && c.definition()
                .types
                .as_ref()
                .is_some_and(|t| t.has_node_type("alm@Need")),
        "version 2 is written against alm@2"
    );
    // the version a methodology pins serves it once archived
    d.delete_domain(&ctx, "alm", "1")
        .await
        .expect("archive alm@1");
    let c = s.methodology(&ctx, "uses@1").await.expect("compiled");
    let t = c.definition().types.clone().expect("types");
    assert!(
        t.has_link_type("alm@verifies") && !t.has_node_type("alm@Need"),
        "an archived pinned version still serves its methodology"
    );
}

// A domain version that breaks a published methodology is published all the same; the methodology registry
// suspends the broken ones (ADR 0125 §3). A compatible domain version, or a methodology version written against the
// new domain, brings it back.
#[tokio::test]
async fn a_domain_that_breaks_a_methodology_suspends_it() {
    let ev = Arc::new(Subjects::default());
    let evp = ev.clone();
    let (s, d) = registry(super::memory(), true, move |s| {
        s.events = Some(evp);
        s.now = Some(ticking_clock());
    });
    let ctx = as_(&["admin"]);
    d.import_domain(&ctx, &must_yaml(&alm_domain("1")), true)
        .await
        .expect("alm@1");
    s.import_published(&ctx, ref_meth("uses"))
        .await
        .expect("import");
    let got = s.recheck(&ctx).await.expect("recheck");
    assert!(got.is_empty(), "in force: {got:?}");
    ev.take();

    // alm@2 drops the link type uses@1 relies on
    let mut v2 = alm_domain("2");
    v2.schema.link_types.clear();
    d.import_domain(&ctx, &must_yaml(&v2), true)
        .await
        .expect("a domain is never refused for a methodology");
    assert_eq!(
        ev.take(),
        vec!["suspended:uses@1"],
        "the suspension is published"
    );
    let r = s.get(&ctx, "uses", "1").await.expect("get");
    let why = s.suspended(&ctx, &r).await;
    assert!(
        why.contains("alm@verifies"),
        "the reason names what broke: {why:?}"
    );
    let err = s.methodology(&ctx, "uses").await.err().expect("refused");
    assert!(err.is_suspended(), "no run reads it: {err}");
    let err = s
        .methodology_version(&ctx, "uses")
        .await
        .err()
        .expect("refused");
    assert!(err.is_graph_invalid(), "no change starts on it: {err}");
    let cs = s.list(&ctx).await.expect("list");
    assert!(cs.is_empty(), "the list leaves it out");

    // a version written against alm@2 is published, the suspended one stays so
    let mut m2 = ref_meth("uses");
    m2.version = "2".into();
    m2.conditions[0].expr =
        r#"changeImpacts.exists(n, n.hasPost && n.post.type == "alm@TestCase")"#.into();
    s.import_published(&ctx, m2).await.expect("import 2");
    assert_eq!(
        s.methodology_version(&ctx, "uses").await.expect("version"),
        "2"
    );
    let cs = s.list(&ctx).await.expect("list");
    assert!(
        cs.len() == 1 && cs[0].definition().version == "2",
        "listed again"
    );

    // alm@3 brings the link type back: uses@1 comes back in force
    ev.take();
    d.import_domain(&ctx, &must_yaml(&alm_domain("3")), true)
        .await
        .expect("alm@3");
    assert_eq!(ev.take(), vec!["resumed:uses@1"], "the return is published");
    s.methodology(&ctx, "uses@1").await.expect("back in force");
}
