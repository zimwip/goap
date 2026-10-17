mod common;

use std::sync::Arc;

use async_trait::async_trait;
use common::{BagEmbedder, DenyType, bag, docs_graph, republish_from, service, wait_for};
use goap_authz::Principal;
use goap_ctx::Ctx;
use goap_domain::MAIN_BRANCH;
use goap_domain::{NodeEvent, NodeID};
use goap_index::{Embedder as _, Query};
use goap_index::{FACET_TYPE, Filter};
use goap_indexersvc::Embedder;
use goap_llm as llm;
use parking_lot::Mutex;
use serde_json::json;

#[tokio::test]
async fn graph_to_search() {
    let (ctx, _cancel) = Ctx::background().with_cancel();
    let svc = Arc::new(service(
        Some(Arc::new(BagEmbedder::default())),
        Some(Arc::new(DenyType("docs@Secret"))),
    ));
    let g = docs_graph(
        &ctx,
        &svc,
        r#"
name: docs
version: 1.0.0
nodeTypes:
  - {name: Requirement, attributes: [title, priority], search: [{property: title, text: true}, {property: priority, facet: true}]}
  - {name: Secret, attributes: [title], search: [{property: title, text: true}]}
"#,
    );
    let g = Arc::new(g);
    g.branch_head(&ctx, "docs", MAIN_BRANCH).await.unwrap();
    let mk = async |key: &str, typ: &str, props: serde_json::Value| {
        goap_graphtest::import(
            &ctx,
            &g,
            goap_graphtest::Node {
                namespace: "docs".into(),
                key: key.into(),
                r#type: typ.into(),
                properties: props.as_object().unwrap().clone(),
                ..Default::default()
            },
        )
        .await
        .unwrap()
    };
    let r1 = mk(
        "REQ-1",
        "docs@Requirement",
        json!({"title": "Users reset their password", "priority": "high"}),
    )
    .await;
    mk(
        "REQ-2",
        "docs@Requirement",
        json!({"title": "Export a report", "priority": "low"}),
    )
    .await;
    mk("SEC-1", "docs@Secret", json!({"title": "password vault"})).await;
    g.branch_head(&ctx, "docs", MAIN_BRANCH).await.unwrap();

    let user = goap_authz::with_principal(
        &ctx,
        Principal {
            subject: "u".into(),
            roles: vec!["contributor".into()],
            ..Principal::default()
        },
    );
    let main = |text: &str| Query {
        text: text.into(),
        filter: Filter {
            main: Some(true),
            ..Filter::default()
        },
        ..Query::default()
    };
    let q = Query {
        facets: vec!["priority".into(), FACET_TYPE.into()],
        ..main("password")
    };
    // the secret is filtered by ABAC, before counting
    let res = wait_for(&svc, &user, &q, 1).await;
    assert!(
        res.hits[0].key == "REQ-1"
            && res.semantic
            && res.facets[FACET_TYPE][0].value == "docs@Requirement",
        "res = {res:?}"
    );
    // an internal caller (no identity) sees both
    wait_for(&svc, &ctx, &q, 2).await;

    // a new version on main moves the flag; the old version stays searchable off main
    goap_graphtest::edit(
        &ctx,
        &g,
        &r1.id,
        json!({"title": "Users reset their passkey", "priority": "high"})
            .as_object()
            .unwrap()
            .clone(),
        &[],
    )
    .await
    .unwrap();
    g.branch_head(&ctx, "docs", MAIN_BRANCH).await.unwrap();
    wait_for(&svc, &ctx, &main("passkey"), 1).await;
    wait_for(&svc, &ctx, &main("password"), 1).await; // SEC-1 only
    let reqs = Query {
        text: "reset".into(),
        filter: Filter {
            r#type: vec!["docs@Requirement".into()],
            ..Filter::default()
        },
        ..Query::default()
    };
    let all = wait_for(&svc, &ctx, &reqs, 2).await;
    assert!(
        all.hits[0].key.contains("REQ-1") && all.hits[0].version + all.hits[1].version == 3,
        "both versions of REQ-1 expected: {:?}",
        all.hits
    );

    // reindex: the graph publishes again, the index converges to the same content
    republish_from(&svc, &g);
    let n = svc.reindex(&ctx).await.unwrap();
    assert!(n > 0, "reindex: {n}");
    wait_for(&svc, &ctx, &reqs, 2).await;
    wait_for(&svc, &ctx, &main("passkey"), 1).await;
}

#[tokio::test]
async fn embedding_unavailable_degrades_to_text() {
    let ctx = Ctx::background();
    let svc = service(Some(Arc::new(BagEmbedder { fail: true })), None);
    for (i, title) in ["alpha beta", "gamma"].iter().enumerate() {
        let ev = NodeEvent {
            id: NodeID::from(((b'a' + i as u8) as char).to_string()),
            version: 1,
            namespace: "x".into(),
            key: title.to_string(),
            r#type: "T".into(),
            branch: "main".into(),
            text: [("title".to_string(), title.to_string())].into(),
            ..NodeEvent::default()
        };
        svc.indexer.on_node(&ctx, &ev).await.unwrap();
    }
    let res = svc
        .searcher
        .search(
            &ctx,
            Query {
                text: "beta".into(),
                ..Query::default()
            },
        )
        .await
        .unwrap();
    assert!(res.total == 1 && !res.semantic, "text-only search: {res:?}");
    assert!(
        !svc.status().semantic,
        "status must report no semantic search"
    );
}

struct Spy(Mutex<llm::CallMeta>);

#[async_trait]
impl llm::Embedder for Spy {
    async fn embed(
        &self,
        ctx: &Ctx,
        req: llm::EmbedRequest,
    ) -> Result<llm::EmbedResponse, llm::Error> {
        *self.0.lock() = llm::meta_from(ctx);
        Ok(bag(&req.texts))
    }
}

/// The embeddings of the index declare themselves to the ledger of the gateway (ADR 0089).
#[tokio::test]
async fn embedder_stamps_its_source() {
    let spy = Arc::new(Spy(Mutex::new(llm::CallMeta::default())));
    let emb = Embedder::new(spy.clone());
    emb.embed(&Ctx::background(), &["hello".to_string()])
        .await
        .unwrap();
    let got = spy.0.lock().clone();
    assert_eq!(got.source, llm::SOURCE_INDEXER, "{got:?}");
}

/// The sink of the all-in-one mode hands the events the index follows to the service, off the writer's path (the
/// part of TestGraphToSearch that needs no graph).
#[tokio::test]
async fn sink_feeds_the_index() {
    let (ctx, _cancel) = Ctx::background().with_cancel();
    let svc = Arc::new(service(Some(Arc::new(BagEmbedder::default())), None));
    let sink = goap_indexersvc::Sink::new(&ctx, svc.clone());
    let ev = NodeEvent {
        id: NodeID::from("n1"),
        version: 1,
        namespace: "docs".into(),
        key: "REQ-1".into(),
        r#type: "docs@Requirement".into(),
        branch: "main".into(),
        text: [(
            "title".to_string(),
            "Users reset their password".to_string(),
        )]
        .into(),
        ..NodeEvent::default()
    };
    sink.publish(
        &ctx,
        "goap.changed.C1",
        &serde_json::json!({"ignored": true}),
    )
    .await
    .unwrap();
    sink.publish(
        &ctx,
        &goap_domain::subject_node_written("docs", "docs@Requirement", &ev.id),
        &ev,
    )
    .await
    .unwrap();
    let mut total = 0;
    for _ in 0..200 {
        total = svc
            .searcher
            .search(
                &ctx,
                Query {
                    text: "password".into(),
                    ..Query::default()
                },
            )
            .await
            .unwrap()
            .total;
        if total == 1 {
            break;
        }
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
    }
    assert_eq!(total, 1);
    let st = svc.status();
    assert!(
        st.semantic && st.nodes == 1 && st.store == "Memory",
        "{st:?}"
    );
}
