// Package enginetel is the telemetry of the engine: the spans of its processes, actions and tools (engine.Tracer),
// the caller baggage of its model calls and the traces the self-observation reads. Package telemetry, which every
// service sets up, stays free of the engine (pkg/layering, ADR 0125).
package enginetel
