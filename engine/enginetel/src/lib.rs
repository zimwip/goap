//! Port of `engine/enginetel` (see docs/rust-port.md).
//!
//! Package enginetel is the telemetry of the engine: the spans of its processes, actions and tools
//! ([`goap_engine::Tracer`]), the caller baggage of its model calls and the traces the self-observation reads. Package
//! telemetry, which every service sets up, stays free of the engine (platform/layering, ADR 0125).

mod baggage;
mod engine;
mod jaeger;

pub use baggage::{LLMClient, with_caller_baggage};
pub use engine::{EngineTracer, error_type};
pub use jaeger::{JaegerTraces, self_improvement_from_env};

#[cfg(test)]
mod tests;
