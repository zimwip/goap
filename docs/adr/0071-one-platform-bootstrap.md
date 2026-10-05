# ADR 0071 — One platform bootstrap; demo data is not part of it

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0028, 0040, 0054, 0058, 0066.

## Context

`goap-dev` and `cmd/graph` each spelled their own seed sequence, in different orders (the access policies before or
after the methodologies, the models first or last, the demo unconditional or behind `GOAP_GRAPH_SEED`), and the
"platform defaults" mixed in development concepts: `SeedDefaults` created the `document-repository` MCP and its localfs
adapter definition in every deployment, `SeedDemo` lived next to the platform seeds, `goap-dev` hard-coded the
`localfs` adapter. The registry, the graph and the engine each wrote the literal `system:registry` / `system:` subjects.

## Decision

- **One function**, `graphsvc.Boot(ctx, g, Options)`, runs for every composition: `Graph.Bootstrap`, `SeedAccess`,
  `SeedBuiltins`, `SeedModels`, then `Options.Dev`. Idempotent. The caller assigns the hooks of the graph and loads the
  type catalogue before, and seeds the methodologies after (the registry needs the model aliases). `Options.RequireHooks`
  panics when the access hooks are missing; a graph without `LandingGate` / `SubChangeValidator` / `Lifecycles` (no registry
  in process: `cmd/graph`) gets one warning, no behaviour change (the gap of ADR 0066 stays, now visible).
- **Platform defaults carry no development concept.** `SeedDefaults` is gone (nothing generic remained once the bootstrap
  is `Graph.Bootstrap`). `internal/devseed` holds the ALM demo (`Demo`), the document-repository MCP and its localfs
  adapter definition (`DocumentRepository`) and the directory-backed repository of the default organisation (`LocalFS`).
  `goap-dev` passes them as `Options.Dev`; `cmd/graph` only with `GOAP_GRAPH_SEED=demo`.
- **Service identity has one constructor**, `authz.System(name, roles...)` (`authz.SystemPrefix` + name), used for
  `system:registry`, `system:graph`, `system:client` and the trigger / companion principals.

## Consequences

- A production `cmd/graph` no longer creates the `document-repository` MCP; it appears with the demo seed or a dev run.
- `cmd/graph` still bootstraps the roots synchronously before serving (the catalogue may load later); `Boot` repeats it
  as a no-op.
- The first human to sign in remains the administrator (ADR 0040, 0047); nothing here changes it.
