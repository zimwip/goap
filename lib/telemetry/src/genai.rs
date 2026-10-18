//! The instrumentation of LLM calls (GenAI semantic conventions).

use std::time::Instant;

use goap_ctx::Ctx;
use goap_llm as llm;
use opentelemetry::{
    Array, Context, KeyValue, StringValue, Value,
    baggage::BaggageExt,
    global::{self, BoxedTracer},
    metrics::{Counter, Histogram, Meter, MeterProvider},
    trace::{SpanKind, Status, TraceContextExt, Tracer, TracerProvider},
};
use serde::Serialize;

use crate::{SCOPE, otel_from, with_otel};

/// Baggage keys propagating the GOAP caller from the engine to the model gateway, so that LLM spans and token
/// metrics are attributed.
pub const BAGGAGE_PROCESS: &str = "goap.process.id";
pub const BAGGAGE_METHODOLOGY: &str = "goap.methodology";
pub const BAGGAGE_AGENT: &str = "goap.agent";
pub const BAGGAGE_ACTION: &str = "goap.action";

/// ContentEnv is the standard variable turning the capture of the message content on.
pub const CONTENT_ENV: &str = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT";

/// MaxContent caps each content attribute of a span (bytes).
pub const MAX_CONTENT: usize = 16 << 10;

/// GenAI instruments LLM calls with the OpenTelemetry GenAI semantic conventions: one "chat <model>" client span per
/// completion and one "embeddings <model>" span per embedding call, the gen_ai.client.token.usage /
/// gen_ai.client.operation.duration metrics, goap.llm.cache_tokens and goap.llm.retries, attributed to the GOAP agent
/// and action carried by the baggage and to the source the caller declares (llm::CallMeta).
pub struct GenAI {
    tracer: BoxedTracer,
    tokens: Histogram<u64>,
    duration: Histogram<f64>,
    cache: Counter<u64>,
    retries: Counter<u64>,
    /// Content records the prompt and the answer on the chat spans (gen_ai.system_instructions,
    /// gen_ai.input.messages, gen_ai.output.messages), each capped at MAX_CONTENT bytes. Off by default: the change
    /// log and the ledger keep the exchanges (ADR 0059, 0089); [`GenAI::new`] turns it on when
    /// OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT is "true".
    pub content: bool,
}

impl Default for GenAI {
    fn default() -> Self {
        Self::new()
    }
}

impl GenAI {
    /// NewGenAI returns the LLM instrumentation on the global providers.
    pub fn new() -> Self {
        Self::build(global::tracer(SCOPE), global::meter(SCOPE))
    }

    /// The LLM instrumentation on given providers (tests, a service that keeps its own).
    pub fn with_providers<T, M>(tp: &T, mp: &M) -> Self
    where
        T: TracerProvider,
        T::Tracer: Tracer + Send + Sync + 'static,
        <T::Tracer as Tracer>::Span: Send + Sync + 'static,
        M: MeterProvider,
    {
        Self::build(
            BoxedTracer::new(Box::new(tp.tracer(SCOPE))),
            mp.meter(SCOPE),
        )
    }

    fn build(tracer: BoxedTracer, m: Meter) -> Self {
        GenAI {
            tracer,
            content: std::env::var(CONTENT_ENV).is_ok_and(|v| v.eq_ignore_ascii_case("true")),
            tokens: m
                .u64_histogram("gen_ai.client.token.usage")
                .with_description("Number of input and output tokens used")
                .with_unit("{token}")
                .build(),
            duration: m
                .f64_histogram("gen_ai.client.operation.duration")
                .with_description("GenAI operation duration")
                .with_unit("s")
                .build(),
            cache: m
                .u64_counter("goap.llm.cache_tokens")
                .with_description(
                    "Input tokens read from or written to the provider's prompt cache",
                )
                .with_unit("{token}")
                .build(),
            retries: m
                .u64_counter("goap.llm.retries")
                .with_description("Retries of a transient provider failure")
                .with_unit("{retry}")
                .build(),
        }
    }

    /// observeRetries makes the retries of the provider call of ctx span events and goap.llm.retries.
    fn observe_retries(&self, ctx: &Ctx, cx: &Context, metric_attrs: &[KeyValue]) -> Ctx {
        let cx = cx.clone();
        let retries = self.retries.clone();
        let metric_attrs = metric_attrs.to_vec();
        llm::with_retry_observer(
            ctx,
            std::sync::Arc::new(move |attempt, err: &llm::Error, wait| {
                let typ = err.error_type();
                cx.span().add_event(
                    "gen_ai.retry",
                    vec![
                        KeyValue::new("goap.llm.attempt", i64::from(attempt)),
                        KeyValue::new("error.type", typ.clone()),
                        KeyValue::new("goap.llm.retry_wait_ms", wait.as_millis() as i64),
                    ],
                );
                let mut a = metric_attrs.clone();
                a.push(KeyValue::new("error.type", typ));
                retries.add(1, &a);
            }),
        )
    }

    /// Instrument wraps one completion on provider/model: call runs with the context of the span.
    pub async fn instrument<F, Fut>(
        &self,
        ctx: &Ctx,
        provider: &str,
        model: &str,
        req: &llm::Request,
        call: F,
    ) -> Result<llm::Response, llm::Error>
    where
        F: FnOnce(Ctx) -> Fut,
        Fut: Future<Output = Result<llm::Response, llm::Error>>,
    {
        let parent = otel_from(ctx);
        let (caller, caller_metric) = caller_attrs(ctx, &parent);
        let mut attrs = vec![
            KeyValue::new("gen_ai.operation.name", "chat"),
            // kept for the dashboards; gen_ai.provider.name is its successor
            KeyValue::new("gen_ai.system", provider.to_string()),
            KeyValue::new("gen_ai.provider.name", provider.to_string()),
            KeyValue::new("gen_ai.request.model", model.to_string()),
            KeyValue::new("gen_ai.request.max_tokens", req.max_tokens),
            KeyValue::new("goap.llm.json", req.json),
            KeyValue::new("goap.llm.cache", req.cache),
            KeyValue::new("goap.llm.messages", req.messages.len() as i64),
        ];
        attrs.extend(caller);
        if req.json {
            attrs.push(KeyValue::new("gen_ai.output.type", "json"));
        }
        let span = self
            .tracer
            .span_builder(format!("chat {model}"))
            .with_kind(SpanKind::Client)
            .with_attributes(attrs)
            .start_with_context(&self.tracer, &parent);
        let cx = parent.with_span(span);
        let mut metric_attrs = vec![
            KeyValue::new("gen_ai.operation.name", "chat"),
            KeyValue::new("gen_ai.system", provider.to_string()),
            KeyValue::new("gen_ai.provider.name", provider.to_string()),
            KeyValue::new("gen_ai.request.model", model.to_string()),
        ];
        metric_attrs.extend(caller_metric);
        let s = cx.span();
        if self.content {
            s.set_attribute(KeyValue::new(
                "gen_ai.system_instructions",
                cap_text(&req.system),
            ));
            s.set_attribute(KeyValue::new(
                "gen_ai.input.messages",
                cap_text(&messages_json(&req.messages)),
            ));
        }
        let start = Instant::now();
        let call_ctx = self.observe_retries(&with_otel(ctx, cx.clone()), &cx, &metric_attrs);
        let out = call(call_ctx).await;
        match &out {
            Err(err) => metric_attrs = fail(&cx, err, metric_attrs),
            Ok(resp) => {
                s.set_attributes([
                    KeyValue::new("gen_ai.response.model", resp.model.clone()),
                    KeyValue::new("gen_ai.usage.input_tokens", resp.usage.input_tokens),
                    KeyValue::new("gen_ai.usage.output_tokens", resp.usage.output_tokens),
                    KeyValue::new(
                        "gen_ai.usage.cache_read.input_tokens",
                        resp.usage.cache_read_tokens,
                    ),
                    KeyValue::new(
                        "gen_ai.usage.cache_creation.input_tokens",
                        resp.usage.cache_write_tokens,
                    ),
                ]);
                if !resp.stop_reason.is_empty() {
                    s.set_attribute(KeyValue::new(
                        "gen_ai.response.finish_reasons",
                        string_slice(std::slice::from_ref(&resp.stop_reason)),
                    ));
                }
                if !resp.behaviors.is_empty() {
                    s.set_attribute(KeyValue::new(
                        "goap.llm.behaviors",
                        string_slice(&resp.behaviors),
                    ));
                }
                if self.content {
                    s.set_attribute(KeyValue::new(
                        "gen_ai.output.messages",
                        cap_text(&messages_json(&[llm::Message {
                            role: "assistant".into(),
                            content: resp.text.clone(),
                            ..Default::default()
                        }])),
                    ));
                }
                let mut ma = metric_attrs.clone();
                ma.push(KeyValue::new("gen_ai.response.model", resp.model.clone()));
                let with = |k: &'static str, v: &'static str| {
                    let mut a = ma.clone();
                    a.push(KeyValue::new(k, v));
                    a
                };
                self.tokens.record(
                    non_negative(resp.usage.input_tokens),
                    &with("gen_ai.token.type", "input"),
                );
                self.tokens.record(
                    non_negative(resp.usage.output_tokens),
                    &with("gen_ai.token.type", "output"),
                );
                if resp.usage.cache_read_tokens > 0 {
                    self.cache.add(
                        non_negative(resp.usage.cache_read_tokens),
                        &with("goap.cache.op", "read"),
                    );
                }
                if resp.usage.cache_write_tokens > 0 {
                    self.cache.add(
                        non_negative(resp.usage.cache_write_tokens),
                        &with("goap.cache.op", "write"),
                    );
                }
            }
        }
        self.duration
            .record(start.elapsed().as_secs_f64(), &metric_attrs);
        s.end();
        out
    }

    /// InstrumentEmbed wraps one embedding call on provider/model: call runs with the context of the span.
    pub async fn instrument_embed<F, Fut>(
        &self,
        ctx: &Ctx,
        provider: &str,
        model: &str,
        texts: usize,
        call: F,
    ) -> Result<llm::EmbedResponse, llm::Error>
    where
        F: FnOnce(Ctx) -> Fut,
        Fut: Future<Output = Result<llm::EmbedResponse, llm::Error>>,
    {
        let parent = otel_from(ctx);
        let (caller, caller_metric) = caller_attrs(ctx, &parent);
        let mut attrs = vec![
            KeyValue::new("gen_ai.operation.name", "embeddings"),
            KeyValue::new("gen_ai.provider.name", provider.to_string()),
            KeyValue::new("gen_ai.request.model", model.to_string()),
            KeyValue::new("goap.llm.texts", texts as i64),
        ];
        attrs.extend(caller);
        let span = self
            .tracer
            .span_builder(format!("embeddings {model}"))
            .with_kind(SpanKind::Client)
            .with_attributes(attrs)
            .start_with_context(&self.tracer, &parent);
        let cx = parent.with_span(span);
        let mut metric_attrs = vec![
            KeyValue::new("gen_ai.operation.name", "embeddings"),
            KeyValue::new("gen_ai.provider.name", provider.to_string()),
            KeyValue::new("gen_ai.request.model", model.to_string()),
        ];
        metric_attrs.extend(caller_metric);
        let start = Instant::now();
        let out = call(with_otel(ctx, cx.clone())).await;
        match &out {
            Err(err) => metric_attrs = fail(&cx, err, metric_attrs),
            Ok(resp) => {
                cx.span()
                    .set_attribute(KeyValue::new("gen_ai.usage.input_tokens", resp.tokens));
                let mut a = metric_attrs.clone();
                a.push(KeyValue::new("gen_ai.token.type", "input"));
                self.tokens.record(non_negative(resp.tokens), &a);
            }
        }
        self.duration
            .record(start.elapsed().as_secs_f64(), &metric_attrs);
        cx.span().end();
        out
    }
}

fn non_negative(n: i64) -> u64 {
    u64::try_from(n).unwrap_or(0)
}

fn string_slice(v: &[String]) -> Value {
    Value::Array(Array::String(
        v.iter().map(|s| StringValue::from(s.clone())).collect(),
    ))
}

/// callerAttrs are the attributes of the caller of a call: the engine baggage, and what the caller declares. The
/// first holds them all; the second only the bounded ones (no process, change or conversation id).
fn caller_attrs(ctx: &Ctx, cx: &Context) -> (Vec<KeyValue>, Vec<KeyValue>) {
    let bag = cx.baggage();
    let (mut span, mut metric) = (Vec::new(), Vec::new());
    for k in [
        BAGGAGE_PROCESS,
        BAGGAGE_METHODOLOGY,
        BAGGAGE_AGENT,
        BAGGAGE_ACTION,
    ] {
        if let Some(v) = bag.get(k).map(|v| v.as_str().to_string())
            && !v.is_empty()
        {
            span.push(KeyValue::new(k, v.clone()));
            if k != BAGGAGE_PROCESS {
                metric.push(KeyValue::new(k, v));
            }
        }
    }
    let m = llm::meta_from(ctx);
    span.push(KeyValue::new("goap.llm.source", m.source.clone()));
    metric.push(KeyValue::new("goap.llm.source", m.source));
    if !m.conversation_id.is_empty() {
        span.push(KeyValue::new("gen_ai.conversation.id", m.conversation_id));
    }
    if !m.change_id.is_empty() {
        span.push(KeyValue::new("goap.change.id", m.change_id));
    }
    let bag_process = bag
        .get(BAGGAGE_PROCESS)
        .is_some_and(|v| !v.as_str().is_empty());
    if !m.process_id.is_empty() && !bag_process {
        span.push(KeyValue::new(BAGGAGE_PROCESS, m.process_id));
    }
    (span, metric)
}

/// fail records err on the span of cx and returns the metric attributes with its error.type.
fn fail(cx: &Context, err: &llm::Error, mut metric_attrs: Vec<KeyValue>) -> Vec<KeyValue> {
    let typ = err.error_type();
    let span = cx.span();
    span.record_error(err);
    span.set_status(Status::error(err.to_string()));
    span.set_attribute(KeyValue::new("error.type", typ.clone()));
    metric_attrs.push(KeyValue::new("error.type", typ));
    metric_attrs
}

/// messagesJSON renders messages in the shape of the GenAI semantic conventions
/// ({role, parts: [{type: text, content}]}).
fn messages_json(msgs: &[llm::Message]) -> String {
    #[derive(Serialize)]
    struct Part<'a> {
        r#type: &'a str,
        content: &'a str,
    }
    #[derive(Serialize)]
    struct Message<'a> {
        role: &'a str,
        parts: Vec<Part<'a>>,
    }
    let out: Vec<Message> = msgs
        .iter()
        .map(|m| Message {
            role: &m.role,
            parts: vec![Part {
                r#type: "text",
                content: &m.content,
            }],
        })
        .collect();
    serde_json::to_string(&out).unwrap_or_default()
}

/// capText cuts s at MAX_CONTENT bytes, on a rune boundary.
fn cap_text(s: &str) -> String {
    if s.len() <= MAX_CONTENT {
        return s.to_string();
    }
    let mut cut = MAX_CONTENT;
    while cut > 0 && !s.is_char_boundary(cut) {
        cut -= 1;
    }
    format!("{}…(truncated)", &s[..cut])
}

#[cfg(test)]
mod tests {
    use super::*;
    use opentelemetry_sdk::{
        metrics::SdkMeterProvider,
        trace::{InMemorySpanExporter, SdkTracerProvider, SpanData},
    };

    fn recorder() -> (InMemorySpanExporter, SdkTracerProvider, SdkMeterProvider) {
        let exp = InMemorySpanExporter::default();
        let tp = SdkTracerProvider::builder()
            .with_simple_exporter(exp.clone())
            .build();
        (exp, tp, SdkMeterProvider::builder().build())
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

    fn first(s: &SpanData, key: &str) -> String {
        match attr(s, key) {
            Some(Value::Array(Array::String(v))) => v[0].to_string(),
            other => panic!("{key} = {other:?}"),
        }
    }

    #[tokio::test]
    async fn gen_ai_span_carries_caller_cache_and_stop() {
        let (exp, tp, mp) = recorder();
        let g = GenAI::with_providers(&tp, &mp);
        let ctx = llm::with_meta(
            &Ctx::background(),
            llm::CallMeta {
                source: llm::SOURCE_ASSISTANT.into(),
                conversation_id: "c1".into(),
                change_id: "ch1".into(),
                ..Default::default()
            },
        );
        let req = llm::Request {
            system: "be brief".into(),
            messages: vec![llm::Message {
                role: "user".into(),
                content: "hi".into(),
                ..Default::default()
            }],
            json: true,
            cache: true,
            ..Default::default()
        };
        let _ = g
            .instrument(&ctx, "anthropic", "claude-x", &req, |_| async {
                Ok(llm::Response {
                    text: r#"{"a":1}"#.into(),
                    model: "claude-x".into(),
                    stop_reason: llm::STOP_MAX_TOKENS.into(),
                    behaviors: vec!["terse".into()],
                    usage: llm::Usage {
                        input_tokens: 100,
                        output_tokens: 5,
                        cache_read_tokens: 80,
                        cache_write_tokens: 10,
                    },
                    ..Default::default()
                })
            })
            .await;
        let s = one(exp.get_finished_spans().unwrap(), "chat claude-x");
        for (k, want) in [
            ("goap.llm.source", "assistant"),
            ("gen_ai.conversation.id", "c1"),
            ("goap.change.id", "ch1"),
            ("gen_ai.provider.name", "anthropic"),
            ("gen_ai.output.type", "json"),
        ] {
            assert_eq!(string(&s, k), want, "{k}");
        }
        assert_eq!(
            attr(&s, "gen_ai.usage.cache_read.input_tokens"),
            Some(Value::I64(80))
        );
        assert_eq!(
            attr(&s, "gen_ai.usage.cache_creation.input_tokens"),
            Some(Value::I64(10))
        );
        assert_eq!(first(&s, "gen_ai.response.finish_reasons"), "max_tokens");
        assert_eq!(attr(&s, "goap.llm.cache"), Some(Value::Bool(true)));
        assert_eq!(first(&s, "goap.llm.behaviors"), "terse");
        // no content by default
        assert!(
            string(&s, "gen_ai.input.messages").is_empty()
                && string(&s, "gen_ai.system_instructions").is_empty(),
            "content recorded without being asked"
        );
    }

    #[tokio::test]
    async fn gen_ai_captures_content_on_request() {
        let (exp, tp, mp) = recorder();
        let mut g = GenAI::with_providers(&tp, &mp);
        g.content = true;
        let long = "é".repeat(MAX_CONTENT); // 2 bytes a rune: cut on a rune boundary
        let req = llm::Request {
            system: long,
            messages: vec![llm::Message {
                role: "user".into(),
                content: "hi".into(),
                ..Default::default()
            }],
            ..Default::default()
        };
        let _ = g
            .instrument(&Ctx::background(), "p", "m", &req, |_| async {
                Ok(llm::Response {
                    text: "hello".into(),
                    ..Default::default()
                })
            })
            .await;
        let s = one(exp.get_finished_spans().unwrap(), "chat m");
        let sys = string(&s, "gen_ai.system_instructions");
        assert!(
            sys.ends_with("…(truncated)")
                && sys.len() <= MAX_CONTENT + "…(truncated)".len()
                && sys.starts_with("éé"),
            "system instructions {} bytes",
            sys.len()
        );
        assert_eq!(
            string(&s, "gen_ai.input.messages"),
            r#"[{"role":"user","parts":[{"type":"text","content":"hi"}]}]"#
        );
        assert_eq!(
            string(&s, "gen_ai.output.messages"),
            r#"[{"role":"assistant","parts":[{"type":"text","content":"hello"}]}]"#
        );
    }

    // Not in the Go suite: the retries of the call are span events, an error its error.type.
    #[tokio::test]
    async fn gen_ai_records_retries_and_errors() {
        let (exp, tp, mp) = recorder();
        let g = GenAI::with_providers(&tp, &mp);
        let _ = g
            .instrument(
                &Ctx::background(),
                "p",
                "m",
                &llm::Request::default(),
                |ctx| async move {
                    let err = llm::Error::typed("429", "slow down");
                    (llm::retry_observer_from(&ctx))(1, &err, std::time::Duration::from_millis(5));
                    Err(err)
                },
            )
            .await;
        let s = one(exp.get_finished_spans().unwrap(), "chat m");
        assert_eq!(string(&s, "error.type"), "429");
        assert!(s.events.iter().any(|e| e.name == "gen_ai.retry"));
    }
}
