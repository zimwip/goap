use goap_ctx::{Ctx, CtxError};
use goap_engine::{Attributes, Error, Process, Step, Tracer, Usage, with_attributes};
use goap_llm as llm;
use goap_telemetry::GenAI;
use opentelemetry::Value;
use opentelemetry_sdk::metrics::SdkMeterProvider;
use opentelemetry_sdk::trace::{InMemorySpanExporter, SdkTracerProvider, SpanData};
use serde_json::json;

use super::*;

fn recorder() -> (InMemorySpanExporter, SdkTracerProvider, SdkMeterProvider) {
    let exp = InMemorySpanExporter::default();
    let tp = SdkTracerProvider::builder()
        .with_simple_exporter(exp.clone())
        .build();
    (exp, tp, SdkMeterProvider::builder().build())
}

fn attr(s: &SpanData, key: &str) -> Option<Value> {
    s.attributes
        .iter()
        .find(|kv| kv.key.as_str() == key)
        .map(|kv| kv.value.clone())
}

fn one<'a>(spans: &'a [SpanData], name: &str) -> &'a SpanData {
    spans
        .iter()
        .find(|s| s.name == name)
        .unwrap_or_else(|| panic!("no span {name:?} in {} spans", spans.len()))
}

fn process(id: &str, agent: &str) -> Process {
    Process {
        id: id.into(),
        agent: agent.into(),
        ..Default::default()
    }
}

#[test]
fn engine_tool_span_and_events() {
    let (exp, tp, mp) = recorder();
    let tr = EngineTracer::with_providers(&tp, &mp);
    let p = process("p1", "a");
    let (ctx, end_action) = tr.start_action(&Ctx::background(), &p, "act", "llm");
    let attrs = json!({"goap.llm.repair": 1, "goap.llm.truncated": true, "error": "bad"});
    tr.event(&ctx, "llm.repair", attrs.as_object().unwrap());
    let (_, end_tool) = tr.start_tool(&ctx, &p, "act", "fs/read");
    end_tool(Some(&Error::Ctx(CtxError::DeadlineExceeded)));
    end_action(&Step::default(), None);
    let spans = exp.get_finished_spans().unwrap();
    let tool = one(&spans, "execute_tool fs/read");
    assert!(
        attr(tool, "goap.mcp") == Some(Value::from("fs"))
            && attr(tool, "error.type") == Some(Value::from("timeout")),
        "tool attributes {:?}",
        tool.attributes
    );
    let action = one(&spans, "action act");
    let events: Vec<_> = action.events.iter().collect();
    assert!(
        events.len() == 1 && events[0].name == "llm.repair" && events[0].attributes.len() == 3,
        "events {events:?}"
    );
}

/// statusError is a provider error typed by its HTTP status, as the model gateway's are.
fn status_error(code: u16) -> llm::Error {
    llm::Error::typed(code.to_string(), format!("status {code}"))
}

#[test]
fn error_types() {
    let joined = Error::msg("w\nEOF");
    let cases: Vec<(Option<Box<dyn std::error::Error + 'static>>, &str)> = vec![
        (None, ""),
        (Some(Box::new(llm::Error::Refusal)), "refusal"),
        (Some(Box::new(CtxError::Canceled)), "canceled"),
        (Some(Box::new(Error::msg("x"))), "_OTHER"),
        (Some(Box::new(status_error(503))), "503"),
        (Some(Box::new(joined)), "_OTHER"),
        // not in the Go suite: the Connect code of an RPC error
        (
            Some(Box::new(goap_connect::ConnectError::new(
                goap_connect::Code::Unavailable,
                "down",
            ))),
            "unavailable",
        ),
    ];
    for (err, want) in cases {
        let got = error_type(err.as_deref());
        assert_eq!(got, want, "{err:?}");
    }
}

#[tokio::test]
async fn gen_ai_span_attributed_to_agent() {
    let (exp, tp, mp) = recorder();
    let g = GenAI::with_providers(&tp, &mp);
    let ctx = with_attributes(
        &Ctx::background(),
        Attributes {
            process_id: "p1".into(),
            agent: "test-designer".into(),
            action: "summarize".into(),
            ..Default::default()
        },
    );
    let ctx = with_caller_baggage(&ctx);
    g.instrument(
        &ctx,
        "anthropic",
        "claude-opus-5",
        &llm::Request {
            max_tokens: 100,
            ..Default::default()
        },
        |_| async {
            Ok(llm::Response {
                model: "claude-opus-5".into(),
                usage: llm::Usage {
                    input_tokens: 12,
                    output_tokens: 5,
                    ..Default::default()
                },
                ..Default::default()
            })
        },
    )
    .await
    .unwrap();
    let spans = exp.get_finished_spans().unwrap();
    assert!(
        spans.len() == 1 && spans[0].name == "chat claude-opus-5",
        "spans {spans:?}"
    );
    let s = &spans[0];
    assert!(
        attr(s, "gen_ai.usage.input_tokens") == Some(Value::I64(12))
            && attr(s, "gen_ai.usage.output_tokens") == Some(Value::I64(5))
            && attr(s, "gen_ai.system") == Some(Value::from("anthropic"))
            && attr(s, "goap.agent") == Some(Value::from("test-designer"))
            && attr(s, "goap.action") == Some(Value::from("summarize")),
        "attributes {:?}",
        s.attributes
    );
}

#[test]
fn process_trace_continues() {
    let (exp, tp, mp) = recorder();
    let tr = EngineTracer::with_providers(&tp, &mp);
    let p = process("p1", "coordinator");
    let (ctx, end) = tr.start_process(&Ctx::background(), &p);
    let trace_id = tr.trace_id(&ctx);
    end(&p);
    assert!(!trace_id.is_empty(), "no trace for the process");
    // a later background run continues the same trace
    let (ctx, end2) = tr.start_process(&Ctx::background(), &p);
    let (_, end_action) = tr.start_action(&ctx, &p, "delegate", "script");
    end_action(
        &Step {
            usage: Usage {
                input_tokens: 3,
                llm_calls: 1,
                ..Default::default()
            },
            ..Default::default()
        },
        None,
    );
    end2(&p);
    let spans = exp.get_finished_spans().unwrap();
    assert_eq!(spans.len(), 3, "expected 3 spans");
    for s in &spans {
        assert_eq!(
            s.span_context.trace_id().to_string(),
            trace_id,
            "span {} is not in the process trace",
            s.name
        );
    }
    let action = one(&spans, "action delegate");
    assert!(
        attr(action, "gen_ai.usage.input_tokens") == Some(Value::I64(3))
            && attr(action, "goap.action.kind") == Some(Value::from("script")),
        "action span wrong: {action:?}"
    );
    // a process that carries its traceparent continues it (another replica, a restart)
    let carried = Process {
        trace_parent: format!("00-{trace_id}-0123456789abcdef-01"),
        ..process("p2", "other")
    };
    let (ctx, end3) = tr.start_process(&Ctx::background(), &carried);
    assert_eq!(tr.trace_id(&ctx), trace_id);
    end3(&carried);
}
