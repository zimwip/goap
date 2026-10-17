//! The spans of a turn (GenAI semantic conventions, `trace.go`): "invoke_agent assistant" for the turn, a "chat" span
//! per round (the gateway's), an "execute_tool <name>" per tool call, "compact_history assistant" for a compaction.
//! Without a configured provider they are no-ops.

use goap_ctx::Ctx;
use goap_llm as llm;
use goap_telemetry::{otel_from, with_otel};
use opentelemetry::trace::{Status, TraceContextExt, Tracer as _};
use opentelemetry::{Context, KeyValue, global};

use crate::service::{ALIAS, Error};
use crate::turn::Turn;
use crate::uitools::PREFIX_UI;

/// The instrumentation scope of the spans.
const SCOPE: &str = "github.com/zimwip/goap/platform/assistantsvc";

/// agentName is the name of the assistant as a GenAI agent.
pub(crate) const AGENT_NAME: &str = "assistant";

/// What a span records of an error: its text and its error.type.
pub(crate) trait SpanError {
    fn text(&self) -> String;
    fn kind(&self) -> String;
}

impl SpanError for Error {
    fn text(&self) -> String {
        self.to_string()
    }
    fn kind(&self) -> String {
        llm::error_type(Some(self))
    }
}

impl SpanError for String {
    fn text(&self) -> String {
        self.clone()
    }
    fn kind(&self) -> String {
        "_OTHER".into()
    }
}

fn start(ctx: &Ctx, name: String, attrs: Vec<KeyValue>) -> (Ctx, Context) {
    let parent = otel_from(ctx);
    let tracer = global::tracer(SCOPE);
    let span = tracer
        .span_builder(name)
        .with_attributes(attrs)
        .start_with_context(&tracer, &parent);
    let cx = parent.with_span(span);
    (with_otel(ctx, cx.clone()), cx)
}

/// startTurn starts the span of a turn.
pub(crate) fn start_turn(t: &Turn, ctx: &Ctx) -> (Ctx, Context) {
    start(
        ctx,
        format!("invoke_agent {AGENT_NAME}"),
        vec![
            KeyValue::new("gen_ai.operation.name", "invoke_agent"),
            KeyValue::new("gen_ai.agent.name", AGENT_NAME),
            KeyValue::new("gen_ai.conversation.id", t.conversation.clone()),
            KeyValue::new("gen_ai.request.model", ALIAS),
            KeyValue::new("enduser.id", t.p.subject.clone()),
            KeyValue::new("goap.project", t.project.clone()),
            KeyValue::new("goap.assistant.ui_tools", t.ui_tools.len() as i64),
        ],
    )
}

/// startTool starts the span of a tool call.
pub(crate) fn start_tool(ctx: &Ctx, name: &str) -> (Ctx, Context) {
    // a screen tool: run (or proposed) by the web, not here
    let typ = if name.len() > PREFIX_UI.len() && name.starts_with(PREFIX_UI) {
        "extension"
    } else {
        "function"
    };
    start(
        ctx,
        format!("execute_tool {name}"),
        vec![
            KeyValue::new("gen_ai.operation.name", "execute_tool"),
            KeyValue::new("gen_ai.tool.name", name.to_string()),
            KeyValue::new("gen_ai.tool.type", typ),
        ],
    )
}

/// startCompact starts the span of a compaction of the history.
pub(crate) fn start_compact(ctx: &Ctx, conversation: &str, n: usize) -> (Ctx, Context) {
    start(
        ctx,
        format!("compact_history {AGENT_NAME}"),
        vec![
            KeyValue::new("gen_ai.conversation.id", conversation.to_string()),
            KeyValue::new("goap.assistant.compacted", n as i64),
        ],
    )
}

/// endSpan sets the closing attributes and the error of a span, then ends it.
pub(crate) fn end_span<E: SpanError + ?Sized>(
    cx: &Context,
    err: Option<&E>,
    attrs: &[(&str, i64)],
) {
    let span = cx.span();
    for (k, v) in attrs {
        span.set_attribute(KeyValue::new(k.to_string(), *v));
    }
    if let Some(e) = err {
        let text = e.text();
        span.add_event(
            "exception",
            vec![KeyValue::new("exception.message", text.clone())],
        );
        span.set_status(Status::error(text));
        span.set_attribute(KeyValue::new("error.type", e.kind()));
    }
    span.end();
}
