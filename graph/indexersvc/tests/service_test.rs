mod common;

use std::sync::Arc;

use async_trait::async_trait;
use common::{BagEmbedder, bag, service};
use goap_ctx::Ctx;
use goap_domain::{NodeEvent, NodeID};
use goap_index::{Embedder as _, Query};
use goap_indexersvc::Embedder;
use goap_llm as llm;
use parking_lot::Mutex;

// TestGraphToSearch runs the graph (goap-graph, goap-graphtest): not ported yet, see the report.

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
