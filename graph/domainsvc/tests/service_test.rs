mod common;

use std::sync::Arc;

use common::{alm_domain, as_, casbin, domain_stores, temp_dir, with_alm};
use goap_algo::{Algorithm, Param, USAGE_LINK_VALIDATOR, USAGE_PROPERTY_VALIDATOR};
use goap_connect::{Ctx, Request};
use goap_def::{Domain, NodeType, Record, STATUS_ARCHIVED, STATUS_DRAFT, STATUS_PUBLISHED, Schema};
use goap_domainpb as domainpb;
use goap_domainsvc::{BoxError, Handler, MemoryStore, Publisher, Service, Store, changed_fn};
use goap_gen::registry::v1::{self as pb, DomainService};
use parking_lot::Mutex;
use serde_json::{Map, Value, json};

fn service(store: Arc<dyn Store>) -> Service {
    Service {
        authz: Some(casbin()),
        ..Service::new(store)
    }
}

fn obj(v: Value) -> Map<String, Value> {
    v.as_object().cloned().unwrap_or_default()
}

#[tokio::test]
async fn domain_lifecycle() {
    let stores = domain_stores("lifecycle").await;
    for (name, store) in &stores.all {
        let changed = Arc::new(Mutex::new(Vec::<String>::new()));
        let seen = changed.clone();
        let s = Service {
            changed: Some(changed_fn(move |_ctx, r: Record| {
                let seen = seen.clone();
                async move {
                    seen.lock().push(format!(
                        "{}@{}:{}",
                        r.domain.name, r.domain.version, r.status
                    ));
                }
            })),
            ..service(store.clone())
        };
        let ctx = as_(&["admin"]);

        let err = s
            .save_domain(&as_(&["contributor"]), alm_domain("1"))
            .await
            .unwrap_err();
        assert!(err.is_forbidden(), "{name}: contributor save: {err}");
        let (rec, issues) = s.save_domain(&ctx, alm_domain("1")).await.unwrap();
        assert!(
            issues.is_empty() && rec.status == STATUS_DRAFT,
            "{name}: save: {rec:?} {issues}"
        );
        let got = s.get_domain(&ctx, "alm", "1").await.unwrap();
        assert!(
            got.domain.node_types.len() == 2
                && got.domain.node_types[1].attributes[0].name == "title"
                && got.domain.link_types[0].to == "Requirement",
            "{name}: get: {got:?}"
        );
        let err = s.get_domain(&ctx, "alm", "").await.unwrap_err();
        assert!(
            err.is_not_found(),
            "{name}: no published version yet: {err}"
        );
        assert!(
            changed.lock().is_empty(),
            "{name}: a draft changes no type in force"
        );
        s.publish_domain(&ctx, "alm", "1").await.unwrap();
        let cat = s.types(&ctx, &[]).await.unwrap();
        assert!(
            cat.has_node_type("alm@Requirement") && cat.has_node_type("methodology@Agent"),
            "{name}: type catalogue"
        );

        // published versions are immutable
        let err = s.save_domain(&ctx, alm_domain("1")).await.unwrap_err();
        assert!(err.is_immutable(), "{name}: save published: {err}");

        // a new version that drops a type is published: the domain registry names no methodology (ADR 0125 §3)
        let v2 = s
            .create_domain_version(&ctx, "alm", "1", "2")
            .await
            .unwrap();
        assert_eq!(v2.status, STATUS_DRAFT, "{name}: new version");
        let mut d = v2.domain;
        d.link_types.clear();
        s.save_domain(&ctx, d).await.unwrap();
        s.publish_domain(&ctx, "alm", "2").await.unwrap();
        let mut v3 = alm_domain("3");
        v3.node_types.push(NodeType::named("Need"));
        s.save_domain(&ctx, v3).await.unwrap();
        s.publish_domain(&ctx, "alm", "3").await.unwrap();
        let cat = s.types(&ctx, &[]).await.unwrap();
        assert!(
            cat.has_node_type("alm@Need") && cat.domains()["alm"] == "3",
            "{name}: the latest published version is in force"
        );
        // the version in force can be archived too: the methodologies it breaks are the methodology registry's
        s.delete_domain(&ctx, "alm", "1").await.unwrap();
        let r = s.get_domain(&ctx, "alm", "1").await.unwrap();
        assert_eq!(
            r.status, STATUS_ARCHIVED,
            "{name}: an archived version stays readable"
        );
        assert_eq!(
            *changed.lock(),
            vec![
                "alm@1:published",
                "alm@2:published",
                "alm@3:published",
                "alm@1:published"
            ],
            "{name}: every publication and archive is told"
        );

        // drafts are deletable, lists show the latest published per name
        s.save_domain(&ctx, alm_domain("4")).await.unwrap();
        s.delete_domain(&ctx, "alm", "4").await.unwrap();
        let list = s.domain_versions(&ctx, false).await.unwrap();
        let builtin: Vec<&str> = list
            .iter()
            .filter(|r| r.builtin)
            .map(|r| r.domain.name.as_str())
            .collect();
        let stored: Vec<&Record> = list.iter().filter(|r| !r.builtin).collect();
        assert!(
            stored.len() == 1
                && stored[0].domain.version == "3"
                && builtin == ["methodology", "organisation", "platform"],
            "{name}: latest: {builtin:?} {stored:?}"
        );
    }
}

#[tokio::test]
async fn builtin_domains_and_references() {
    let s = service(Arc::new(MemoryStore::new()));
    let ctx = as_(&["admin"]);
    s.import_domain(
        &ctx,
        b"name: alm\nversion: \"1\"\nnodeTypes: [Requirement]\n",
        true,
    )
    .await
    .unwrap();
    for name in ["methodology", "organisation", "platform"] {
        let d = Domain {
            name: name.into(),
            version: "9".into(),
            schema: Schema {
                node_types: vec![NodeType::named("X")],
                ..Schema::default()
            },
            ..Domain::default()
        };
        let err = s.save_domain(&ctx, d).await.unwrap_err();
        assert!(
            err.is_immutable(),
            "the built-in domain {name} is frozen: {err}"
        );
        let err = s
            .create_domain_version(&ctx, name, "", "9")
            .await
            .unwrap_err();
        assert!(
            err.is_immutable(),
            "no new version of the frozen domain {name}: {err}"
        );
        let r = s.get_domain(&ctx, name, "").await.unwrap();
        assert!(
            r.builtin && r.status == STATUS_PUBLISHED,
            "the built-in domain {name} is readable: {r:?}"
        );
    }
    let err = s.delete_domain(&ctx, "organisation", "").await.unwrap_err();
    assert!(
        err.is_immutable(),
        "a built-in domain cannot be archived: {err}"
    );

    let ext = Domain {
        name: "ext".into(),
        version: "1".into(),
        schema: Schema {
            node_types: vec![NodeType {
                extends: "alm@Nope".into(),
                ..NodeType::named("X")
            }],
            ..Schema::default()
        },
        ..Domain::default()
    };
    let (_, issues) = s.save_domain(&ctx, ext).await.unwrap();
    assert!(
        !issues.is_empty(),
        "a reference to an unknown type of another domain is reported"
    );
}

#[tokio::test]
async fn domain_import_export() {
    let s = service(Arc::new(MemoryStore::new()));
    let ctx = as_(&["admin"]);
    let src = "name: alm\nversion: \"1\"\nnodeTypes:\n  - name: Need\n  - name: Requirement\n    extends: Need\nlinkTypes:\n  - name: derives\n    from: Requirement\n    to: Need\n";
    let (r, _) = s.import_domain(&ctx, src.as_bytes(), true).await.unwrap();
    assert_eq!(r.status, STATUS_PUBLISHED, "import");
    let (out, name) = s.export_domain(&ctx, "alm", "1", "").await.unwrap();
    let out = String::from_utf8(out).unwrap();
    assert!(
        name == "domain-alm-1.yaml" && out.contains("extends: Need"),
        "export: {name} {out}"
    );
    // JSON: the same document, imported back as another version
    let (js, name) = s.export_domain(&ctx, "alm", "1", "json").await.unwrap();
    let js = String::from_utf8(js).unwrap();
    assert!(
        name == "domain-alm-1.json" && js.contains(r#""extends": "Need""#),
        "export JSON: {name} {js}"
    );
    let (r2, _) = s
        .import_domain(
            &ctx,
            js.replacen(r#""version": "1""#, r#""version": "2""#, 1)
                .as_bytes(),
            false,
        )
        .await
        .unwrap();
    assert!(
        r2.domain.version == "2"
            && r2.domain.node_types.len() == 2
            && r2.domain.link_types.len() == 1,
        "import JSON: {r2:?}"
    );
    let err = s.export_domain(&ctx, "alm", "1", "xml").await.unwrap_err();
    assert!(err.is_invalid(), "unknown format: {err}");
}

#[tokio::test]
async fn seed_domains() {
    let dir = temp_dir("seed");
    std::fs::write(
        dir.0.join("alm.yaml"),
        "name: alm\nversion: \"1\"\nnodeTypes:\n  - name: Need\n",
    )
    .unwrap();
    let s = Service::new(Arc::new(MemoryStore::new()));
    let got = s.seed_domains(&as_(&["admin"]), &dir.0).await.unwrap();
    assert_eq!(got, vec!["alm@1"], "seed");
    let got = s.seed_domains(&as_(&["admin"]), &dir.0).await.unwrap();
    assert!(
        got.is_empty(),
        "seed again must skip stored versions: {got:?}"
    );
    let r = s.get_domain(&as_(&["admin"]), "alm", "").await.unwrap();
    assert_eq!(r.status, STATUS_PUBLISHED, "published");
}

#[tokio::test]
async fn domain_composed_of_node_types_link_types_and_lifecycles() {
    let yaml = r#"
name: docs
version: 1.0.0
lifecycles:
  - name: req
    initial: draft
    states: [{name: draft, notLandable: true}, {name: approved}]
    transitions: [{name: approve, from: draft, to: approved, permission: "requirement:approve", requires: {attributes: [title]}}]
nodeTypes:
  - {name: Requirement, lifecycle: req}
  - {name: Spec, document: {contains: [Requirement]}, lifecycle: req, changeControlled: true}
linkTypes:
  - {name: contains, from: Spec, to: Requirement}
"#;
    let d = goap_def::parse_domain(yaml.as_bytes()).unwrap();
    // protobuf round trip keeps the three parts
    let back = domainpb::domain_from_pb(Some(&domainpb::domain_to_pb(&Record {
        domain: d.clone(),
        status: STATUS_DRAFT,
        ..Record::default()
    })));
    assert!(
        back.lifecycles.len() == 1
            && back.lifecycles[0].transitions[0].requires.attributes[0] == "title"
            && back.node_types[1].lifecycle == "req"
            && back.node_types[1].document.is_some()
            && back.link_types.len() == 1,
        "pb round trip: {back:?}"
    );
    let stores = domain_stores("composed").await;
    for (name, store) in &stores.all {
        let s = service(store.clone());
        let ctx = as_(&["admin"]);
        let (_, issues) = s.save_domain(&ctx, d.clone()).await.unwrap();
        assert!(issues.is_empty(), "{name}: save: {issues}");
        let got = s.get_domain(&ctx, "docs", "1.0.0").await.unwrap();
        assert!(
            got.domain.lifecycles.len() == 1
                && got.domain.lifecycles[0].name == "req"
                && got.domain.node_types[0].lifecycle == "req",
            "{name}: stored domain: {:?}",
            got.domain
        );
        let l = got.domain.lifecycle_of("Spec");
        assert!(
            l.is_some_and(|l| !l.landable("draft") && l.landable("approved")),
            "{name}: resolved lifecycle: {l:?}"
        );
        // a reference to a lifecycle the domain does not have is rejected
        let mut bad = d.clone();
        bad.version = "1.0.1".into();
        bad.node_types = vec![NodeType {
            lifecycle: "nope".into(),
            ..NodeType::named("Requirement")
        }];
        let (_, issues) = s.save_domain(&ctx, bad).await.unwrap();
        assert!(
            !issues.is_empty(),
            "{name}: unknown lifecycle must be an issue"
        );
    }
}

/// A domain with algorithms survives the store and the wire format, and a broken plug or script is reported when the
/// draft is saved.
#[tokio::test]
async fn domain_algorithms() {
    let src = std::fs::read("../../domains/alm.yaml").unwrap();
    let d = goap_def::parse_domain(&src).unwrap();
    assert!(
        !d.algorithms.is_empty() && !d.instances.is_empty(),
        "alm.yaml declares algorithms"
    );
    // wire round trip
    let back = domainpb::domain_from_pb(Some(&domainpb::domain_to_pb(&Record {
        domain: d.clone(),
        ..Record::default()
    })));
    assert!(
        back.algorithms.len() == d.algorithms.len() && back.instances.len() == d.instances.len(),
        "wire round trip lost algorithms: {}/{}",
        back.algorithms.len(),
        back.instances.len()
    );
    assert_eq!(
        back.instances[2].values.get("pattern"),
        d.instances[2].values.get("pattern"),
        "instance values"
    );
    assert_eq!(
        back.node_types[1].attributes[0].validators[0], d.node_types[1].attributes[0].validators[0],
        "validators lost: {:?}",
        back.node_types[1]
    );
    let stores = domain_stores("algorithms").await;
    for (name, store) in &stores.all {
        let s = service(store.clone());
        let ctx = as_(&["admin"]);
        let (_, issues) = s.save_domain(&ctx, d.clone()).await.unwrap();
        assert!(issues.is_empty(), "{name}: save: {issues}");
        let got = s.get_domain(&ctx, &d.name, &d.version).await.unwrap();
        assert!(
            got.domain.algorithms.len() == d.algorithms.len()
                && got.domain.algorithms[0].params[0].r#type == "regex",
            "{name}: get: {:?}",
            got.domain.algorithms
        );
        // a broken plug and a broken script are reported with their path
        let mut bad = d.clone();
        bad.version = "2".into();
        bad.algorithms[0].code = "function (".into();
        bad.instances[1].algorithm = "missing".into();
        let (_, issues) = s.save_domain(&ctx, bad).await.unwrap();
        assert!(issues.len() >= 2, "{name}: expected issues: {issues}");
        let text = issues.to_string();
        assert!(
            text.contains("algorithms[0].code") && text.contains(r#"unknown algorithm "missing""#),
            "{name}: issues: {text}"
        );
    }
}

#[tokio::test]
async fn run_algorithm() {
    let s = service(Arc::new(MemoryStore::new()));
    let a = Algorithm {
        name: "len".into(),
        r#type: USAGE_PROPERTY_VALIDATOR,
        language: "javascript".into(),
        params: vec![Param {
            name: "max".into(),
            r#type: "number".into(),
            required: true,
            ..Param::default()
        }],
        code: r#"if (String(ctx.value()).length > ctx.param("max")) ctx.fail("too long")"#.into(),
        ..Algorithm::default()
    };
    let out = s
        .run_algorithm(
            &as_(&["admin"]),
            &a,
            &obj(json!({"max": 3.0})),
            &obj(json!({"property": "p", "value": "abcd"})),
        )
        .await
        .unwrap();
    assert!(!out.ok() && out.failures[0] == "too long", "{out:?}");
    let err = s
        .run_algorithm(&as_(&["admin"]), &a, &Map::new(), &Map::new())
        .await
        .unwrap_err();
    assert!(err.is_invalid(), "missing required param: {err}");
    let err = s
        .run_algorithm(
            &as_(&["contributor"]),
            &a,
            &obj(json!({"max": 3.0})),
            &Map::new(),
        )
        .await
        .unwrap_err();
    assert!(err.is_forbidden(), "contributor: {err}");
}

#[tokio::test]
async fn run_link_validator() {
    let s = service(Arc::new(MemoryStore::new()));
    let a = Algorithm {
        name: "same-state".into(),
        r#type: USAGE_LINK_VALIDATOR,
        language: "javascript".into(),
        code: r#"if (ctx.from().state !== ctx.to().state) ctx.fail(ctx.link().type + ": " + ctx.from().state + " vs " + ctx.to().state)"#.into(),
        ..Algorithm::default()
    };
    let input = obj(json!({"link": {"type": "refines"},
        "from": {"key": "A", "state": "draft"}, "to": {"key": "B", "state": "done"}}));
    let out = s
        .run_algorithm(&as_(&["admin"]), &a, &Map::new(), &input)
        .await
        .unwrap();
    assert!(
        !out.ok() && out.failures[0] == "refines: draft vs done",
        "{out:?}"
    );
}

#[test]
fn domain_node_type_editors() {
    let d = goap_def::parse_domain(goap_domains_builtin::PLATFORM.as_bytes()).unwrap();
    let issues = d.validate();
    assert!(issues.is_empty(), "{issues}");
    // wire round trip
    let back = domainpb::domain_from_pb(Some(&domainpb::domain_to_pb(&Record {
        domain: d,
        ..Record::default()
    })));
    let editors: std::collections::HashMap<&str, &str> = back
        .node_types
        .iter()
        .map(|n| (n.name.as_str(), n.editor.as_str()))
        .collect();
    for (typ, want) in [
        ("MCP", "mcp"),
        ("AdapterDef", "adapter"),
        ("LlmProvider", ""),
    ] {
        assert_eq!(
            editors.get(typ).copied().unwrap_or(""),
            want,
            "editor of {typ}"
        );
    }
}

#[tokio::test]
async fn list_types() {
    let s = Arc::new(Service::new(Arc::new(MemoryStore::new())));
    with_alm(&s).await;
    let res = Handler::new(s)
        .list_types(
            &as_(&["contributor"]),
            Request::new(pb::ListTypesRequest {}),
        )
        .await
        .unwrap()
        .into_inner();
    let by_ref: std::collections::HashMap<&str, &pb::TypeInfo> =
        res.types.iter().map(|t| (t.r#ref.as_str(), t)).collect();
    let sec = by_ref.get("alm@SecurityRequirement").copied();
    assert!(
        sec.is_some_and(|sec| sec.lifecycle.as_ref().map(|l| l.name.as_str())
            == Some("requirement")
            && sec.ancestors.len() == 2)
            && by_ref["methodology@Agent"].editor == "agent",
        "types: {sec:?}"
    );
    assert!(
        res.domains.get("alm").is_some_and(|v| !v.is_empty()),
        "the version in force of each domain is listed"
    );
    assert!(
        res.link_types.iter().any(|l| l.r#ref == "alm@verifies"
            && l.from == "alm@TestCase"
            && l.to == "alm@Requirement"),
        "link types are listed with their ends"
    );
}

/// The registry tags the structures of the graph through its domains (ADR 0054): the built-in organisation domain
/// tags the organisation and the project; a domain tagging one of them again is reported and never published.
#[tokio::test]
async fn domain_structures() {
    let s = service(Arc::new(MemoryStore::new()));
    let ctx = as_(&["admin"]);
    let cat = s.types(&ctx, &[]).await.unwrap();
    for kind in [
        goap_domain::STRUCTURE_ORGANISATION,
        goap_domain::STRUCTURE_PROJECT,
    ] {
        let st = cat.structure(kind);
        assert!(
            st.is_some_and(|st| !st.root.is_empty() && !st.r#type.is_empty()),
            "structure {kind} = {st:?}"
        );
    }
    let src = "name: hr\nversion: \"1\"\nnodeTypes:\n  - {name: Team, structure: {kind: project, parent: within, root: HR-ROOT}}\nlinkTypes:\n  - {name: within, from: Team, to: Team}\n";
    let (_, issues) = s.import_domain(&ctx, src.as_bytes(), false).await.unwrap();
    assert!(
        issues
            .to_string()
            .contains("structure project is already tagged"),
        "a second project structure is reported: {issues}"
    );
    let err = s.publish_domain(&ctx, "hr", "1").await.unwrap_err();
    assert!(err.is_invalid(), "and never published: {err}");
}

#[derive(Default)]
struct Recorder {
    subjects: Mutex<Vec<String>>,
}

#[goap_connect::async_trait]
impl Publisher for Recorder {
    async fn publish(&self, _ctx: &Ctx, subject: &str, _v: &Value) -> Result<(), BoxError> {
        self.subjects.lock().push(subject.to_string());
        Ok(())
    }
}

/// Importing a published domain is one write and one event.
#[tokio::test]
async fn import_published_is_one_event() {
    let ctx = goap_authz::with_principal(
        &Ctx::background(),
        goap_authz::Principal {
            subject: "system:registry".into(),
            roles: vec!["admin".into()],
            ..Default::default()
        },
    );
    let ev = Arc::new(Recorder::default());
    let s = Service {
        events: Some(ev.clone()),
        ..Service::new(Arc::new(MemoryStore::new()))
    };
    let (r, _) = s
        .import_domain(
            &ctx,
            b"name: imp\nversion: \"1\"\nnodeTypes: [{name: A}]\n",
            true,
        )
        .await
        .unwrap();
    assert!(
        r.status == STATUS_PUBLISHED && r.published_at.is_some(),
        "imported as published: {r:?}"
    );
    {
        let subjects = ev.subjects.lock();
        assert!(
            subjects.len() == 1 && subjects[0].ends_with("domain.published"),
            "one event: {subjects:?}"
        );
    }
    // a domain with issues is kept as a draft and refused
    ev.subjects.lock().clear();
    let err = s
        .import_domain(
            &ctx,
            b"name: bad\nversion: \"1\"\nnodeTypes: [{name: A, extends: Nope}]\n",
            true,
        )
        .await
        .unwrap_err();
    match err {
        goap_domainsvc::Error::Draft { record, issues, .. } => assert!(
            !issues.is_empty() && record.status == STATUS_DRAFT,
            "a domain with issues stays a draft: {record:?} {issues}"
        ),
        e => panic!("a domain with issues stays a draft: {e}"),
    }
}
