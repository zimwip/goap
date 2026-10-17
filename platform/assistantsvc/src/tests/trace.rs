//! trace_test.go

use opentelemetry::{Value as OtelValue, global};
use opentelemetry_sdk::trace::{InMemorySpanExporter, SdkTracerProvider, SpanData};

use super::*;

fn get<'a>(s: &'a SpanData, k: &str) -> Option<&'a OtelValue> {
    s.attributes
        .iter()
        .find(|kv| kv.key.as_str() == k)
        .map(|kv| &kv.value)
}

fn int(v: Option<&OtelValue>) -> i64 {
    match v {
        Some(OtelValue::I64(n)) => *n,
        _ => 0,
    }
}

fn text(v: Option<&OtelValue>) -> String {
    v.map(|v| v.as_str().into_owned()).unwrap_or_default()
}

#[tokio::test]
async fn a_turn_is_an_invoke_agent_span_with_its_tools() {
    let exp = InMemorySpanExporter::default();
    // the global provider is shared by the tests running alongside: the spans of this turn are told by its
    // conversation
    global::set_tracer_provider(
        SdkTracerProvider::builder()
            .with_simple_exporter(exp.clone())
            .build(),
    );

    let e = new_env(&[
        &call("delete_everything", r#"{"x":1}"#),
        r#"{"message":"I cannot."}"#,
    ])
    .await;
    e.send("do it", Context::default()).await.unwrap();
    e.answer().await; // the turn has ended
    let spans = exp.get_finished_spans().expect("spans");
    let turn = spans
        .iter()
        .find(|s| {
            s.name == "invoke_agent assistant"
                && text(get(s, "gen_ai.conversation.id")) == e.conv.id
        })
        .unwrap_or_else(|| {
            panic!(
                "no turn span in {:?}",
                spans.iter().map(|s| &s.name).collect::<Vec<_>>()
            )
        });
    let tool = spans
        .iter()
        .find(|s| {
            s.name == "execute_tool delete_everything"
                && s.span_context.trace_id() == turn.span_context.trace_id()
        })
        .unwrap_or_else(|| {
            panic!(
                "no tool span in {:?}",
                spans.iter().map(|s| &s.name).collect::<Vec<_>>()
            )
        });
    assert!(
        int(get(turn, "goap.assistant.rounds")) == 2
            && int(get(turn, "goap.tool_calls")) == 1
            && !text(get(turn, "gen_ai.conversation.id")).is_empty()
            && text(get(turn, "gen_ai.agent.name")) == "assistant",
        "turn attributes {:?}",
        turn.attributes
    );
    assert!(
        tool.parent_span_id == turn.span_context.span_id()
            && text(get(tool, "error.type")) == "_OTHER",
        "tool span {:?} parent {:?}",
        tool.attributes,
        tool.parent_span_id
    );
}
