# ADR 0170 — The Rust graph service is frozen on a branch of its own

**Status**: accepted, implemented · **Date**: 2026-10 · **Supersedes**: ADR 0163 (its use on `main`)

## Context

ADR 0163 rewrote the graph service in Rust (`goap-graph-rs`, `goap-domains-rs`) behind a comparing proxy
(`goap-proxy`), and its fourth phase made the compose stack run it. Keeping two implementations of the graph answering
alike has a cost on every change of the graph: each new RPC, write path, rule or migration is written twice, the
golden files the Go tests generate for Rust (`casbin_cases.gen.json`, the history cases, `boot.gen.json`, the shape
of `def.Domain`) kept current, the comparison stacks (`perf-rust`, `perf-diff`, `perf-compare`, `goap-local -proxy`)
maintained, and the Docker images and the local stack built with cargo as well as go.

A comparison on PostgreSQL (warm database, the writes of `goap-bench` in both of its modes) measured:

- Rust spends 27 to 40 % less CPU in the service and 20 to 50 % less memory, and serves the heavy reads (pages,
  history, differences, read queries) 1.3 to 2 times faster;
- in `commit` mode (one `CommitEdits` per change) it writes 11 to 16 % more versions per second;
- in `lifecycle` mode (the call-by-call API the IDE uses) Go writes 3 to 8 % more: the time goes to PostgreSQL and
  the round trips, where Go's pipelined writes send fewer statements per version (7.2 against 9.4).

The throughput the IDE sees is the same; the gain of Rust is resources, not latency, and does not pay for two
implementations to keep in step.

## Decision

1. **Go is the graph service.** `main` holds the Go graph only: the compose stack, the performance stack and the
   local stack (`goap-local`) run `platform/cmd/graph` and `platform/cmd/domains`.
2. **Rust is frozen on `frozen/rust-graph`.** The branch keeps the Cargo workspace `rust/`, the proxy, the comparison
   stacks (`deploy/compose/perf.rust.yml`, `perf.compare.yml`, the Grafana dashboard *Go vs Rust*), the Docker targets
   `rust` and `proxy` and the Go tests generating the files Rust reads, as they stood when it was frozen. Nothing on
   `main` is kept compatible with it.
3. **Removed from `main`**: `rust/`; the tests `TestRustCasesAreCurrent` (`contracts/authz`, `contracts/journal`),
   `TestRustBootIsCurrent` (`platform/devseed`), `TestRustYAMLSchemaIsCurrent` (`lib/def`); the Docker targets
   `rust-build`, `rust`, `proxy`; the `proxy` service of the performance stack; the make targets `rust-test`,
   `rust-lint`, `perf-rust*`, `perf-diff`, `perf-compare*` and the variables `LOCAL_GRAPH`, `LOCAL_PROXY`,
   `LOCAL_CARGO_PROFILE`, `PERF_PROXY`; the flags `-graph`, `-proxy` and `-cargo-profile` of `goap-local` and its
   cargo watcher.
4. **Kept**: the protocol, the schema and the migrations (shared by construction), the performance stack measuring the
   Go graph (cAdvisor or the podman exporter, `PERF_CONTAINERS`), and the earlier ADRs as history: where they say
   "Go and Rust", Go is what holds.

## Consequences

- A change of the graph is written once. The lifecycle-mode statements per version stay the lever of the write
  throughput, in Go.
- Taking Rust up again means rebasing `frozen/rust-graph` onto `main` and catching up on every change of the graph
  since the freeze; the comparison tooling comes back with it.
