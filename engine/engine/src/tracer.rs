//! The instrumentation of the engine (implemented with OpenTelemetry by the services). The core engine does not
//! depend on a tracing SDK.

use std::sync::Arc;

use goap_ctx::Ctx;
use serde_json::{Map, Value};

use crate::engine::Engine;
use crate::error::Error;
use crate::process::{Process, Step};

/// Ends the span of a Run call.
pub type EndProcess = Box<dyn FnOnce(&Process) + Send>;
/// Ends the span of an action execution.
pub type EndAction = Box<dyn FnOnce(&Step, Option<&Error>) + Send>;
/// Ends the span of a tool call.
pub type EndTool = Box<dyn FnOnce(Option<&Error>) + Send>;

/// Tracer instruments the engine.
pub trait Tracer: Send + Sync {
    /// StartProcess starts the span of a Run call.
    fn start_process(&self, ctx: &Ctx, p: &Process) -> (Ctx, EndProcess);
    /// StartAction starts the span of an action execution.
    fn start_action(&self, ctx: &Ctx, p: &Process, action: &str, kind: &str) -> (Ctx, EndAction);
    /// StartTool starts the span of a tool call.
    fn start_tool(&self, ctx: &Ctx, p: &Process, action: &str, tool: &str) -> (Ctx, EndTool);
    /// TraceID returns the trace of ctx ("" when not traced).
    fn trace_id(&self, ctx: &Ctx) -> String;
    /// Event records an event on the span of ctx (Go's optional EventTracer): what happens inside an action that is
    /// not a span of its own (a repaired answer of the model, a refused tool call). Records nothing by default.
    fn event(&self, _ctx: &Ctx, _name: &str, _attrs: &Map<String, Value>) {}
    /// SpanID returns the span of ctx ("" when not traced; Go's optional SpanIDer).
    fn span_id(&self, _ctx: &Ctx) -> String {
        String::new()
    }
}

/// The tracer that records nothing.
pub struct NopTracer;

impl Tracer for NopTracer {
    fn start_process(&self, ctx: &Ctx, _: &Process) -> (Ctx, EndProcess) {
        (ctx.clone(), Box::new(|_| {}))
    }
    fn start_action(&self, ctx: &Ctx, _: &Process, _: &str, _: &str) -> (Ctx, EndAction) {
        (ctx.clone(), Box::new(|_, _| {}))
    }
    fn start_tool(&self, ctx: &Ctx, _: &Process, _: &str, _: &str) -> (Ctx, EndTool) {
        (ctx.clone(), Box::new(|_| {}))
    }
    fn trace_id(&self, _: &Ctx) -> String {
        String::new()
    }
}

impl Engine {
    /// event records an event on the span of ctx when the tracer records events.
    pub(crate) fn event(&self, ctx: &Ctx, name: &str, attrs: Map<String, Value>) {
        self.tracer().event(ctx, name, &attrs);
    }

    pub(crate) fn tracer(&self) -> Arc<dyn Tracer> {
        match &self.tracer {
            Some(t) => t.clone(),
            None => Arc::new(NopTracer),
        }
    }
}
