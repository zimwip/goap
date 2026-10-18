use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_llm as llm;
use goap_telemetry::{
    BAGGAGE_ACTION, BAGGAGE_AGENT, BAGGAGE_METHODOLOGY, BAGGAGE_PROCESS, otel_from, with_otel,
};
use opentelemetry::baggage::BaggageExt;

/// WithCallerBaggage copies the engine caller attributes of ctx into the OpenTelemetry baggage (propagated to
/// downstream services).
pub fn with_caller_baggage(ctx: &Ctx) -> Ctx {
    let Some(a) = goap_engine::attributes_from(ctx) else {
        return ctx.clone();
    };
    let cx = otel_from(ctx);
    let mut b: opentelemetry::baggage::Baggage = cx
        .baggage()
        .iter()
        .map(|(k, (v, m))| (k.clone(), (v.clone(), m.clone())))
        .collect();
    for (k, v) in [
        (BAGGAGE_PROCESS, a.process_id),
        (BAGGAGE_METHODOLOGY, a.methodology),
        (BAGGAGE_AGENT, a.agent),
        (BAGGAGE_ACTION, a.action),
    ] {
        if !v.is_empty() {
            b.insert(k, v);
        }
    }
    let items: Vec<opentelemetry::baggage::KeyValueMetadata> = b
        .iter()
        .map(|(k, (v, m))| {
            opentelemetry::baggage::KeyValueMetadata::new(k.clone(), v.clone(), m.clone())
        })
        .collect();
    with_otel(ctx, cx.with_baggage(items))
}

/// LLMClient instruments an llm::Client: it propagates the caller baggage.
#[derive(Clone)]
pub struct LLMClient {
    pub next: Arc<dyn llm::Client>,
}

#[async_trait]
impl llm::Client for LLMClient {
    async fn complete(&self, ctx: &Ctx, req: llm::Request) -> Result<llm::Response, llm::Error> {
        self.next.complete(&with_caller_baggage(ctx), req).await
    }
}
