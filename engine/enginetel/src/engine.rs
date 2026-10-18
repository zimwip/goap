use std::collections::HashMap;
use std::error::Error as StdError;
use std::sync::Arc;
use std::time::Instant;

use goap_ctx::Ctx;
use goap_engine::{EndAction, EndProcess, EndTool, Error, Process, STATUS_FAILED, Step, Tracer};
use goap_telemetry::{SCOPE, otel_from, with_otel};
use opentelemetry::global::{self, BoxedTracer};
use opentelemetry::metrics::{Counter, Histogram, Meter, MeterProvider};
use opentelemetry::propagation::TextMapPropagator;
use opentelemetry::trace::{SpanKind, Status, TraceContextExt, Tracer as _, TracerProvider};
use opentelemetry::{Context, KeyValue};
use opentelemetry_sdk::propagation::TraceContextPropagator;
use parking_lot::Mutex;
use serde_json::{Map, Value};

/// EngineTracer implements [`Tracer`] with OpenTelemetry. A process keeps a single trace across its runs: the W3C
/// traceparent of its root span continues the later runs.
///
/// The engine hands the tracer the process read-only, so the traceparent of a process's first span (Go wrote it in
/// `Process.TraceParent`) is remembered by the tracer, by process id, until the process ends; a process that carries
/// one (`trace_parent`) continues it.
pub struct EngineTracer {
    tracer: BoxedTracer,
    actions: Counter<u64>,
    act_dur: Histogram<f64>,
    tokens: Counter<u64>,
    tools: Counter<u64>,
    tool_dur: Histogram<f64>,
    parents: Arc<Mutex<HashMap<String, String>>>,
}

impl Default for EngineTracer {
    fn default() -> Self {
        Self::new()
    }
}

impl EngineTracer {
    /// NewEngineTracer returns the engine instrumentation on the global providers.
    pub fn new() -> Self {
        Self::build(global::tracer(SCOPE), global::meter(SCOPE))
    }

    /// The engine instrumentation on given providers (tests, a service that keeps its own).
    pub fn with_providers<T, M>(tp: &T, mp: &M) -> Self
    where
        T: TracerProvider,
        T::Tracer: opentelemetry::trace::Tracer + Send + Sync + 'static,
        <T::Tracer as opentelemetry::trace::Tracer>::Span: Send + Sync + 'static,
        M: MeterProvider,
    {
        Self::build(
            BoxedTracer::new(Box::new(tp.tracer(SCOPE))),
            mp.meter(SCOPE),
        )
    }

    fn build(tracer: BoxedTracer, m: Meter) -> Self {
        EngineTracer {
            tracer,
            actions: m
                .u64_counter("goap.actions")
                .with_description("Executed actions")
                .with_unit("{action}")
                .build(),
            act_dur: m
                .f64_histogram("goap.action.duration")
                .with_description("Action duration")
                .with_unit("s")
                .build(),
            tokens: m
                .u64_counter("goap.tokens")
                .with_description("LLM tokens consumed by actions")
                .with_unit("{token}")
                .build(),
            tools: m
                .u64_counter("goap.tool.calls")
                .with_description("Tool calls of actions")
                .with_unit("{call}")
                .build(),
            tool_dur: m
                .f64_histogram("goap.tool.duration")
                .with_description("Tool call duration")
                .with_unit("s")
                .build(),
            parents: Arc::default(),
        }
    }

    fn start(
        &self,
        parent: &Context,
        name: String,
        kind: SpanKind,
        attrs: Vec<KeyValue>,
    ) -> Context {
        let span = self
            .tracer
            .span_builder(name)
            .with_kind(kind)
            .with_attributes(attrs)
            .start_with_context(&self.tracer, parent);
        parent.with_span(span)
    }
}

fn process_attrs(p: &Process) -> Vec<KeyValue> {
    vec![
        KeyValue::new("goap.process.id", p.id.clone()),
        KeyValue::new("goap.methodology", p.methodology.clone()),
        KeyValue::new("goap.agent", p.agent.clone()),
        KeyValue::new("goap.planner", p.planner.clone()),
        KeyValue::new("goap.goal", p.goal.clone()),
        KeyValue::new("goap.change.id", p.change_id.to_string()),
        KeyValue::new("goap.parent.id", p.parent_id.clone()),
        KeyValue::new("enduser.id", p.initiator.subject.clone()),
    ]
}

fn non_negative(n: i64) -> u64 {
    u64::try_from(n).unwrap_or(0)
}

impl Tracer for EngineTracer {
    fn start_process(&self, ctx: &Ctx, p: &Process) -> (Ctx, EndProcess) {
        let mut parent = otel_from(ctx);
        let known = if p.trace_parent.is_empty() {
            self.parents.lock().get(&p.id).cloned().unwrap_or_default()
        } else {
            p.trace_parent.clone()
        };
        if !parent.span().span_context().is_valid() && !known.is_empty() {
            let carrier = HashMap::from([("traceparent".to_string(), known.clone())]);
            parent = TraceContextPropagator::new().extract_with_context(&parent, &carrier);
        }
        let cx = self.start(
            &parent,
            format!("process {}", p.agent),
            SpanKind::Internal,
            process_attrs(p),
        );
        if known.is_empty() {
            let mut carrier = HashMap::new();
            TraceContextPropagator::new().inject_context(&cx, &mut carrier);
            if let Some(tp) = carrier.remove("traceparent") {
                self.parents.lock().insert(p.id.clone(), tp);
            }
        }
        let parents = self.parents.clone();
        let end_cx = cx.clone();
        let end: EndProcess = Box::new(move |p: &Process| {
            let span = end_cx.span();
            span.set_attributes([
                KeyValue::new("goap.status", p.status.as_str().to_string()),
                KeyValue::new("goap.steps", p.steps.len() as i64),
                KeyValue::new("gen_ai.usage.input_tokens", p.usage.input_tokens),
                KeyValue::new("gen_ai.usage.output_tokens", p.usage.output_tokens),
                KeyValue::new("goap.llm_calls", p.usage.llm_calls),
                KeyValue::new("goap.tool_calls", p.usage.tool_calls),
            ]);
            if p.status.as_str() == STATUS_FAILED {
                span.set_status(Status::error(p.error.clone()));
            }
            span.end();
            if p.status.terminal() {
                parents.lock().remove(&p.id);
            }
        });
        (with_otel(ctx, cx), end)
    }

    fn start_action(&self, ctx: &Ctx, p: &Process, action: &str, kind: &str) -> (Ctx, EndAction) {
        let mut attrs = process_attrs(p);
        attrs.push(KeyValue::new("goap.action", action.to_string()));
        attrs.push(KeyValue::new("goap.action.kind", kind.to_string()));
        let cx = self.start(
            &otel_from(ctx),
            format!("action {action}"),
            SpanKind::Internal,
            attrs,
        );
        let start = Instant::now();
        let (agent, action, kind) = (p.agent.clone(), action.to_string(), kind.to_string());
        let (tokens, actions, act_dur) = (
            self.tokens.clone(),
            self.actions.clone(),
            self.act_dur.clone(),
        );
        let end_cx = cx.clone();
        let end: EndAction = Box::new(move |s: &Step, err: Option<&Error>| {
            let span = end_cx.span();
            let mut outcome = "ok";
            if let Some(err) = err {
                outcome = "error";
                span.record_error(err);
                span.set_status(Status::error(err.to_string()));
            } else if s.pending() {
                outcome = "waiting";
            }
            span.set_attributes([
                KeyValue::new("gen_ai.usage.input_tokens", s.usage.input_tokens),
                KeyValue::new("gen_ai.usage.output_tokens", s.usage.output_tokens),
                KeyValue::new("goap.llm_calls", s.usage.llm_calls),
                KeyValue::new("goap.tool_calls", s.usage.tool_calls),
                KeyValue::new("goap.items", s.items.len() as i64),
                KeyValue::new("goap.sandbox", s.sandbox.clone()),
            ]);
            for c in &s.tool_calls {
                span.add_event(
                    format!("tool {}", c.name),
                    vec![
                        KeyValue::new("duration_ms", c.duration_ms),
                        KeyValue::new("error", c.error.clone()),
                    ],
                );
            }
            let base = [
                KeyValue::new("goap.agent", agent.clone()),
                KeyValue::new("goap.action", action.clone()),
            ];
            let with = |t: &'static str| {
                let mut a = base.to_vec();
                a.push(KeyValue::new("gen_ai.token.type", t));
                a
            };
            tokens.add(non_negative(s.usage.input_tokens), &with("input"));
            tokens.add(non_negative(s.usage.output_tokens), &with("output"));
            let ma = [
                KeyValue::new("goap.agent", agent.clone()),
                KeyValue::new("goap.action", action.clone()),
                KeyValue::new("goap.action.kind", kind.clone()),
                KeyValue::new("goap.outcome", outcome),
            ];
            actions.add(1, &ma);
            act_dur.record(start.elapsed().as_secs_f64(), &ma);
            span.set_attribute(KeyValue::new("goap.outcome", outcome));
            span.end();
        });
        (with_otel(ctx, cx), end)
    }

    /// StartTool implements Tracer (GenAI "execute_tool" span; goap.tool.calls and goap.tool.duration by tool and
    /// outcome).
    fn start_tool(&self, ctx: &Ctx, p: &Process, action: &str, tool: &str) -> (Ctx, EndTool) {
        let mcp_name = tool.split('/').next().unwrap_or_default().to_string();
        let cx = self.start(
            &otel_from(ctx),
            format!("execute_tool {tool}"),
            SpanKind::Client,
            vec![
                KeyValue::new("gen_ai.operation.name", "execute_tool"),
                KeyValue::new("gen_ai.tool.name", tool.to_string()),
                KeyValue::new("gen_ai.tool.type", "extension"),
                KeyValue::new("goap.mcp", mcp_name.clone()),
                KeyValue::new("goap.process.id", p.id.clone()),
                KeyValue::new("goap.agent", p.agent.clone()),
                KeyValue::new("goap.action", action.to_string()),
            ],
        );
        let start = Instant::now();
        let (tools, tool_dur) = (self.tools.clone(), self.tool_dur.clone());
        let (tool, agent) = (tool.to_string(), p.agent.clone());
        let end_cx = cx.clone();
        let end: EndTool = Box::new(move |err: Option<&Error>| {
            let span = end_cx.span();
            let mut ma = vec![
                KeyValue::new("gen_ai.tool.name", tool),
                KeyValue::new("goap.mcp", mcp_name),
                KeyValue::new("goap.agent", agent),
            ];
            if let Some(err) = err {
                let typ = engine_error_type(err);
                span.record_error(err);
                span.set_status(Status::error(err.to_string()));
                span.set_attribute(KeyValue::new("error.type", typ.clone()));
                ma.push(KeyValue::new("error.type", typ));
            }
            tools.add(1, &ma);
            tool_dur.record(start.elapsed().as_secs_f64(), &ma);
            span.end();
        });
        (with_otel(ctx, cx), end)
    }

    fn trace_id(&self, ctx: &Ctx) -> String {
        let cx = otel_from(ctx);
        let sc = cx.span().span_context().clone();
        if sc.is_valid() {
            sc.trace_id().to_string()
        } else {
            String::new()
        }
    }

    /// Event implements Go's EventTracer: an event on the span of ctx.
    fn event(&self, ctx: &Ctx, name: &str, attrs: &Map<String, Value>) {
        let cx = otel_from(ctx);
        let span = cx.span();
        if !span.is_recording() {
            return;
        }
        let kv = attrs
            .iter()
            .map(|(k, v)| match v {
                Value::String(s) => KeyValue::new(k.clone(), s.clone()),
                Value::Bool(b) => KeyValue::new(k.clone(), *b),
                Value::Number(n) if n.is_i64() => {
                    KeyValue::new(k.clone(), n.as_i64().unwrap_or_default())
                }
                Value::Number(n) => KeyValue::new(k.clone(), n.as_f64().unwrap_or_default()),
                other => KeyValue::new(k.clone(), goap_dsl::sprint(std::slice::from_ref(other))),
            })
            .collect::<Vec<_>>();
        span.add_event(name.to_string(), kv);
    }

    /// SpanID implements Go's SpanIDer: the current span of ctx.
    fn span_id(&self, ctx: &Ctx) -> String {
        let cx = otel_from(ctx);
        let sc = cx.span().span_context().clone();
        if sc.is_valid() {
            sc.span_id().to_string()
        } else {
            String::new()
        }
    }
}

/// errorType is llm::error_type, with the Connect code of an RPC error (a tool of the hub, another service).
pub fn error_type(err: Option<&(dyn StdError + 'static)>) -> String {
    let typ = goap_llm::error_type(err);
    if typ == "_OTHER"
        && let Some(e) = err
    {
        let code = goap_connect::code_of(e);
        if code != goap_connect::Code::Unknown {
            return code.as_str().to_string();
        }
    }
    typ
}

/// The error type of an engine error: the error it carries (its transparent variants hide it from the chain).
fn engine_error_type(err: &Error) -> String {
    match err.root() {
        Error::Llm(e) => error_type(Some(e)),
        Error::Ctx(e) => error_type(Some(e)),
        Error::Dsl(e) => error_type(Some(e)),
        Error::Graph(e) => error_type(Some(e)),
        Error::Authz(e) => error_type(Some(e)),
        other => error_type(Some(other)),
    }
}
