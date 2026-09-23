# ADR 0008 — OpenTelemetry observability, one process = one trace

**Status**: accepted · **Date**: 2026-09

## Context
All calls must be tracked, especially every LLM call and every tool used, with
tokens consumed, and attributed to the process, the agent, and the action.

## Decision
- OpenTelemetry (Go SDK), OTLP export to a **collector** (Jaeger for traces, Prometheus for
  metrics, Grafana for visualization); configured via the standard `OTEL_*` variables.
- Instrumentation: Echo, Connect (`otelconnect`, trusted remote parent between internal
  services), pgx, NATS (context in headers), gateway proxy.
- Business spans: `process <agent>` (the `traceparent` is persisted in the process so that
  background executions, resumptions, and sub-agents stay in the same trace), `action <name>`,
  `chat <model>` (GenAI conventions) in the model gateway, `execute_tool <name>`.
- LLM calls attributed via **baggage** (`goap.process.id`, `goap.agent`, `goap.action`,
  `goap.methodology`) set by the engine and read by the model gateway; the process ID is not
  put into metrics (cardinality).
- Counters (tokens, calls) are also persisted in the process for the IDE.

## Consequences
- A trace can last a long time (process waiting on a human): acceptable for Jaeger, to watch
  for sampling purposes (tail sampling in the collector if needed).
