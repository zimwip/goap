//! Port of `lib/telemetry` (see docs/rust-port.md).
//!
//! OpenTelemetry: OTLP exporters (traces and metrics, configured by the standard `OTEL_*` environment variables),
//! W3C trace context propagation, the hooks of the Connect layer and of the HTTP server (spans, timing logs), and the
//! GOAP instrumentation of LLM calls (GenAI semantic conventions). The span and attribute names are stable
//! (docs/architecture.md §3.7).
//!
//! The OpenTelemetry context travels in the [`goap_ctx::Ctx`] of a call ([`with_otel`] / [`otel_from`]), the way
//! Go's `context.Context` carried it.

mod genai;
mod telemetry;

pub use genai::*;
pub use telemetry::*;
