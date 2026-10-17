//! The router instrumented with the GenAI spans of the telemetry every service sets up (docs/architecture.md §3.7).

use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use goap_ctx::Ctx;
use goap_llm as llm;
use goap_telemetry::GenAI;
use opentelemetry::Value;
use opentelemetry_sdk::metrics::SdkMeterProvider;
use opentelemetry_sdk::trace::{InMemorySpanExporter, SdkTracerProvider, SpanData};

use super::helpers::{Reply, upstream};
use crate::providers::{Fake, OpenAICompatible};
use crate::router::{Instrument, InstrumentEmbed, Router};

fn recorder() -> (InMemorySpanExporter, Arc<GenAI>) {
    let exp = InMemorySpanExporter::default();
    let tp = SdkTracerProvider::builder()
        .with_simple_exporter(exp.clone())
        .build();
    let mp = SdkMeterProvider::builder().build();
    (exp, Arc::new(GenAI::with_providers(&tp, &mp)))
}

fn one(spans: Vec<SpanData>, name: &str) -> SpanData {
    let n = spans.len();
    spans
        .into_iter()
        .find(|s| s.name == name)
        .unwrap_or_else(|| panic!("no span {name:?} in {n} spans"))
}

fn attr(s: &SpanData, key: &str) -> Option<Value> {
    s.attributes
        .iter()
        .find(|kv| kv.key.as_str() == key)
        .map(|kv| kv.value.clone())
}

fn string(s: &SpanData, key: &str) -> String {
    match attr(s, key) {
        Some(Value::String(v)) => v.to_string(),
        _ => String::new(),
    }
}

fn instrument(g: Arc<GenAI>) -> Instrument {
    Arc::new(move |ctx, p, m, req, call| {
        let g = g.clone();
        Box::pin(async move { g.instrument(&ctx, &p, &m, &req, call).await })
    })
}

fn instrument_embed(g: Arc<GenAI>) -> InstrumentEmbed {
    Arc::new(move |ctx, p, m, n, call| {
        let g = g.clone();
        Box::pin(async move { g.instrument_embed(&ctx, &p, &m, n, call).await })
    })
}

#[tokio::test]
async fn test_gen_ai_records_retries_and_the_error_type() {
    let (exp, g) = recorder();
    let n = Arc::new(AtomicUsize::new(0));
    let c = n.clone();
    let url = upstream(move |_| {
        if c.fetch_add(1, Ordering::SeqCst) < 2 {
            return Reply::status(429, "busy\n");
        }
        Reply::status(400, "bad\n")
    })
    .await;
    let mut r = Router::new();
    r.retry_base = Duration::from_millis(1);
    r.add_provider(
        "oa",
        Arc::new(OpenAICompatible {
            base_url: url,
            ..Default::default()
        }),
    );
    r.instrument = Some(instrument(g));
    let res = r
        .complete(
            &Ctx::background(),
            llm::Request {
                model: "oa/m".into(),
                ..Default::default()
            },
        )
        .await;
    assert!(res.is_err(), "the 400 must fail the call");
    let s = one(exp.get_finished_spans().unwrap(), "chat m");
    assert_eq!(string(&s, "error.type"), "400");
    let retries: Vec<String> = s
        .events
        .iter()
        .filter(|e| e.name == "gen_ai.retry")
        .flat_map(|e| {
            e.attributes
                .iter()
                .filter(|kv| kv.key.as_str() == "error.type")
        })
        .map(|kv| kv.value.to_string())
        .collect();
    assert_eq!(retries.join(","), "429,429", "retry events");
}

#[tokio::test]
async fn test_gen_ai_embeddings_span() {
    let (exp, g) = recorder();
    let mut r = Router::new();
    r.add_provider("f", Arc::new(Fake));
    r.instrument_embed = Some(instrument_embed(g));
    let ctx = llm::with_meta(
        &Ctx::background(),
        llm::CallMeta {
            source: llm::SOURCE_INDEXER.into(),
            ..Default::default()
        },
    );
    r.embed(
        &ctx,
        llm::EmbedRequest {
            model: "f/e".into(),
            texts: vec!["a".into(), "b".into()],
        },
    )
    .await
    .unwrap();
    let s = one(exp.get_finished_spans().unwrap(), "embeddings e");
    assert!(
        string(&s, "gen_ai.operation.name") == "embeddings"
            && matches!(attr(&s, "goap.llm.texts"), Some(Value::I64(2)))
            && string(&s, "goap.llm.source") == "indexer",
        "attributes {:?}",
        s.attributes
    );
}
