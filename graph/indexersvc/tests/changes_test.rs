mod common;

use std::collections::HashMap;
use std::sync::Arc;

use common::{BagEmbedder, DenyType, FakeAccess, as_, service};
use goap_connect::{Code, Request};
use goap_ctx::Ctx;
use goap_domain::{
    CHANGE_ACTIVE, CHANGE_DRAFT, ChangeDocEvent, ChangeID, NodeEvent, NodeID,
    subject_change_indexed, subject_node_written,
};
use goap_gen::index::v1::{self as pb, IndexService};
use goap_index::{
    FACET_KIND, FACET_PROJECT, FACET_STATUS, Filter, KIND_CHANGE, KIND_NODE, MODE_LEXICAL, Query,
    Ref,
};
use goap_indexersvc::{Handler, Service};

// TestChangesFollowTheGraph runs the graph (goap-graph, goap-graphtest): not ported yet, see the report.

async fn publish<T: serde::Serialize>(svc: &Service, subject: &str, v: &T) {
    let data = serde_json::to_vec(v).unwrap();
    svc.handle(&Ctx::background(), subject, &data)
        .await
        .unwrap();
}

async fn seed_access() -> Arc<Service> {
    let svc = service(
        Some(Arc::new(BagEmbedder::default())),
        Some(Arc::new(DenyType("docs@Secret"))),
    );
    svc.set_access(Some(Arc::new(FakeAccess {
        projects: HashMap::from([
            ("admin", vec!["*"]),
            ("alice", vec!["P1", "P1.1"]),
            ("bob", vec!["P2"]),
            ("carol", vec![]),
        ]),
        subs: HashMap::from([("P1", vec!["P1", "P1.1"])]),
    })));
    let node = |id: &str, typ: &str, key: &str, project: &str, title: &str| NodeEvent {
        id: NodeID::from(id),
        version: 1,
        namespace: "docs".into(),
        r#type: typ.into(),
        key: key.into(),
        branch: "main".into(),
        project: project.into(),
        text: [("title".to_string(), title.to_string())].into(),
        ..NodeEvent::default()
    };
    for n in [
        node("n1", "docs@Requirement", "REQ-1", "P1", "password reset"),
        node("n2", "docs@Requirement", "REQ-2", "P2", "password policy"),
        node("n3", "docs@Requirement", "REQ-3", "P1.1", "password vault"),
        node("n4", "docs@Secret", "SEC-1", "P1", "password"),
        node("n5", "docs@Requirement", "REQ-5", "", "password no project"),
    ] {
        publish(
            &svc,
            &subject_node_written(&n.namespace, &n.r#type, &n.id),
            &n,
        )
        .await;
    }
    let change = |id: &str, project: &str, owner: &str, status| ChangeDocEvent {
        id: ChangeID::from(id),
        title: "password change".into(),
        project_id: project.into(),
        owner_org: owner.into(),
        status,
        namespace: "docs".into(),
        ..ChangeDocEvent::default()
    };
    for c in [
        change("C1", "P1", "ORG-A", CHANGE_ACTIVE),
        change("C2", "P2", "ORG-B", CHANGE_ACTIVE),
        change("C3", "P1", "USR:alice", CHANGE_DRAFT),
    ] {
        publish(&svc, &subject_change_indexed(&c.id), &c).await;
    }
    Arc::new(svc)
}

#[tokio::test]
async fn results_are_authorized() {
    let svc = seed_access().await;
    let all = Filter {
        kind: vec![KIND_NODE.into(), KIND_CHANGE.into()],
        ..Filter::default()
    };
    let facets: Vec<String> = [FACET_PROJECT, FACET_KIND, FACET_STATUS]
        .iter()
        .map(|s| s.to_string())
        .collect();
    for (who, want) in [
        // an internal caller has no identity and sees all
        ("", "[C1 C2 C3 n1 n2 n3 n4 n5]"),
        // administrators see every project, not the personal changes of others, and not what the policy denies
        ("admin", "[C1 C2 n1 n2 n3 n5]"),
        ("alice", "[C1 C3 n1 n3 n5]"), // P1 and P1.1, her personal change
        ("bob", "[C2 n2 n5]"),
        ("carol", "[n5]"), // no project: only what no project holds
    ] {
        let ctx = if who.is_empty() {
            Ctx::background()
        } else {
            as_(who)
        };
        let r = svc
            .searcher
            .search(
                &ctx,
                Query {
                    text: "password".into(),
                    mode: MODE_LEXICAL.into(),
                    filter: all.clone(),
                    facets: facets.clone(),
                    limit: 50,
                    snippet: true,
                    ..Query::default()
                },
            )
            .await
            .unwrap();
        let mut ids: Vec<String> = r.hits.iter().map(|h| h.id.to_string()).collect();
        ids.sort();
        let got = format!("[{}]", ids.join(" "));
        assert_eq!(got, want, "{who:?}");
        assert_eq!(r.total, r.hits.len(), "{who:?}: total for hits");
        // nothing is counted or quoted for a document the caller cannot see
        let sum: usize = r
            .facets
            .get(FACET_KIND)
            .map(|cs| cs.iter().map(|c| c.count).sum())
            .unwrap_or(0);
        assert_eq!(sum, r.total, "{who:?}: kind facet counts");
        for c in r.facets.get(FACET_PROJECT).into_iter().flatten() {
            match who {
                "bob" => assert_eq!(c.value, "P2", "bob sees the facet {c:?}"),
                "carol" => panic!("carol sees the facet {c:?}"),
                _ => {}
            }
        }
    }
    // similar_to does not reveal what the caller cannot see
    let res = svc
        .searcher
        .search(
            &as_("bob"),
            Query {
                similar_to: Some(Ref {
                    kind: KIND_CHANGE.into(),
                    id: NodeID::from("C3"),
                    ..Ref::default()
                }),
                filter: all.clone(),
                ..Query::default()
            },
        )
        .await;
    assert!(
        res.is_err(),
        "bob must not find the personal change of alice"
    );
}

fn req(subject: &str, m: pb::SearchRequest) -> Request<pb::SearchRequest> {
    let mut r = Request::new(m);
    if !subject.is_empty() {
        r.headers.insert(
            http_name(goap_identity::HEADER_SUBJECT),
            subject.parse().unwrap(),
        );
    }
    r
}

fn http_name(s: &str) -> goap_connect::axum::http::HeaderName {
    goap_connect::axum::http::HeaderName::try_from(s).unwrap()
}

#[tokio::test]
async fn search_handler() {
    let svc = seed_access().await;
    let h = Handler::new(svc);
    let ctx = Ctx::background();
    let call = async |subject: &str, m: pb::SearchRequest| {
        h.search(&ctx, req(subject, m))
            .await
            .map(|r| r.into_inner())
    };
    // a project and its sub-projects, resolved by the server
    let r = call(
        "alice",
        pb::SearchRequest {
            text: "password".into(),
            mode: "lexical".into(),
            kinds: vec!["node".into()],
            projects: vec!["P1".into()],
            include_subprojects: true,
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let mut got: Vec<&str> = r.hits.iter().map(|x| x.key.as_str()).collect();
    got.sort();
    assert_eq!(got, ["REQ-1", "REQ-3"], "with sub-projects");
    let r = call(
        "alice",
        pb::SearchRequest {
            text: "password".into(),
            mode: "lexical".into(),
            kinds: vec!["node".into()],
            projects: vec!["P1".into()],
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert!(
        r.total == 1 && r.hits[0].key == "REQ-1" && r.hits[0].project == "P1",
        "exact project: {r:?}"
    );
    // the hit of a change
    let r = call(
        "bob",
        pb::SearchRequest {
            text: "password".into(),
            mode: "lexical".into(),
            kinds: vec!["change".into()],
            statuses: vec!["active".into()],
            roots_only: true,
            snippet: true,
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert_eq!(r.total, 1, "{r:?}");
    let x = &r.hits[0];
    let c = x.change.as_ref().expect("change info");
    assert!(
        x.kind == "change"
            && c.change_id == "C2"
            && c.title == "password change"
            && c.status == "active"
            && c.project_id == "P2"
            && c.owner_org == "ORG-B"
            && !x.snippet.is_empty(),
        "hit {x:?}"
    );
    // errors are codes
    let cases: Vec<(&str, pb::SearchRequest, Code)> = vec![
        (
            "kind",
            pb::SearchRequest {
                kinds: vec!["file".into()],
                ..Default::default()
            },
            Code::InvalidArgument,
        ),
        (
            "mode",
            pb::SearchRequest {
                text: "x".into(),
                mode: "fuzzy".into(),
                ..Default::default()
            },
            Code::InvalidArgument,
        ),
        (
            "floor",
            pb::SearchRequest {
                text: "x".into(),
                min_similarity: Some(2.0),
                ..Default::default()
            },
            Code::InvalidArgument,
        ),
        (
            "similar_id",
            pb::SearchRequest {
                similar_to: Some(pb::DocumentRef {
                    kind: "node".into(),
                    ..Default::default()
                }),
                ..Default::default()
            },
            Code::InvalidArgument,
        ),
        (
            "unknown doc",
            pb::SearchRequest {
                similar_to: Some(pb::DocumentRef {
                    kind: "node".into(),
                    id: "nope".into(),
                }),
                ..Default::default()
            },
            Code::NotFound,
        ),
    ];
    for (name, m, code) in cases {
        let err = call("alice", m).await.unwrap_err();
        assert_eq!(err.code(), code, "{name}: {err}");
    }
    // semantic search without an embedding model is a failed precondition; hybrid still answers
    let th = Handler::new(Arc::new(service(None, None)));
    let err = th
        .search(
            &ctx,
            Request::new(pb::SearchRequest {
                text: "x".into(),
                mode: "semantic".into(),
                ..Default::default()
            }),
        )
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::FailedPrecondition, "semantic: {err}");
    let res = th
        .search(
            &ctx,
            Request::new(pb::SearchRequest {
                text: "x".into(),
                ..Default::default()
            }),
        )
        .await
        .unwrap()
        .into_inner();
    assert!(!res.semantic, "hybrid degrades: {res:?}");
    // similar_to by the document of a node
    let sim = call(
        "",
        pb::SearchRequest {
            similar_to: Some(pb::DocumentRef {
                kind: "node".into(),
                id: "n1".into(),
            }),
            kinds: vec!["node".into(), "change".into()],
            min_similarity: Some(0.0),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert!(!sim.hits.is_empty(), "{sim:?}");
    for x in &sim.hits {
        assert!(!(x.id == "n1" && x.kind == "node"), "excludes itself");
    }
}
