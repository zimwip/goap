# ADR 0164 — The local stack replaces goap-dev

**Status**: accepted, implemented · **Date**: 2026-10 · Supersedes the `goap-dev` composition of ADR 0010 and ADR 0073
(its builders); builds on ADR 0163.

## Context

`goap-dev` composed every Go service in one process on one SQLite file, rebuilt by air on a Go change. Since the graph
service is written in Rust (ADR 0163) and the compose stack runs it, `goap-dev` ran a graph that is no longer the one
deployed, and wired in process what the deployment wires over RPC and NATS (the registry's hooks on the graph, the
index fed directly, the events delivered from the outbox in process). What the developer ran was a third composition,
next to compose and the services, with its own bugs. `goap-local` (the compose stack without Docker: one process per
binary, an embedded NATS, a SQLite file per service) already ran what compose runs, Rust graph included.

## Decision

- `platform/cmd/goap-dev`, `.air.toml` and air are removed. `make dev` (and `.\make.ps1 dev`) runs
  `goap-local -watch -web` on the binaries `make local-build` builds; `make dev-backend` the same without the IDE;
  `make dev-reset` drops `.goap/local`.
- **Watch** (`-watch`, `platform/cmd/goap-local/watch.go`): the sources are polled every second (the Go modules, `rust/`,
  `domains/`, `methodologies/`), and once they settle (300 ms with no new change):
  - the Go binaries the stack runs that depend on a changed package (`go list -deps`) are built into
    `.goap/local/build`; a change of a `go.mod` / `go.sum` builds them all;
  - a change of `rust/`, or of the graph's migrations (embedded by the Rust graph), builds the cargo binaries;
  - a binary is replaced only when its bytes differ; its services are stopped, the binary put in place (Windows
    refuses to overwrite a running program), and started again;
  - a change of `domains/` restarts the graph services (they publish the shipped domains at start), one of
    `methodologies/` the registry;
  - a failed build leaves the services as they were; a change of `goap-local` itself is only reported.
- **Web** (`-web`): `npm install` once, then `npm run dev` in `web/`, under the same supervisor.
- **Every process stops with the stack**: each child runs in its own process group (Setpgid on Unix, a new process
  group on Windows), so that Ctrl-C reaches it and whatever it started (vite under npm, a build). The first Ctrl-C
  interrupts every group and kills those still running after 10 s, a second kills them at once; once the leader of a
  group exited, the group is killed so that no straggler is left.
- The demo: `GOAP_GRAPH_SEED=demo make dev` (the local stack's default is `dev`), or *Settings → Demo data*.

## Consequences

- What runs locally is what compose runs: the Rust graph by default (`LOCAL_GRAPH=go` for the Go one; `make dev` / `dev-backend` the Go one unless
  `LOCAL_GRAPH` is set, while the Rust graph instantiates no lifecycle or guardian of the methodologies), the services
  talking over RPC and NATS.
- The hooks `goap-dev` wired from its in-process registry are the graph's own: the registry declares the
  methodologies (nodes of the platform graph, ADR 0023), and the graph instantiates from them what it asks of a
  change's methodology: the version and goal a change starts with, its lifecycle, guards and gates (ADR 0058), the
  guardian of its commits and its chain of guardians (ADR 0106, 0146), the cascade of its sub-changes (ADR 0066), the
  standing of its steps (ADR 0109), the roles of its operations (ADR 0103). `platform/cmd/graph` holds for this a
  read-only `registrysvc.Service` over the platform graph (in a content graph, the platform graph's client), which
  seeds, publishes and announces nothing. The Rust graph reads the roles of the operations so; the rest of the
  instantiation is still to be written there (a change following a lifecycle is refused at its transition and merge,
  `unresolved_lifecycle`).
- Lost with `goap-dev`: the in-process workspace and Claude coding agent connectors seeded by
  `GOAP_DEV_WORKSPACE_ROOT` / `GOAP_DEV_WORKSPACE_IMAGE` (run `connector-workspace` and give a unit an adapter);
  `GOAP_DEV_DEMO` (`GOAP_GRAPH_SEED=demo`); the IDE served from `web/dist` (vite serves it).
- A start takes longer (a dozen processes, cargo the first time); a rebuild restarts only what the change touches.
- `platform/layering` `TestCompositionsUseBoot` checks `platform/cmd/graph` only.
