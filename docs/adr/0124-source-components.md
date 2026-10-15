# ADR 0124 — Source components: a contract, the graph, the engine, the MCP hub, the gateway, the platform

**Status**: accepted; step 1 implemented (the contract of the graph, `pkg/graphapi`; the engine and the graph service
no longer import each other), the next steps not yet · **Date**: 2026-10 · Builds on ADR 0061 (`pkg/layering`), ADR
0063 (the engine's scope port), ADR 0066 (the hooks of the graph), ADR 0113 (pinned domains)

## Context

The services are deployed apart (`cmd/graph`, `cmd/engine`, `cmd/mcp`, `cmd/gateway`, ...) and the engine already
reaches the graph through a port it defines, `engine.GraphPort`, implemented by `*graph.Graph` in process and by
`graphsvc.Client` over RPC. The sources do not follow: the port is written with the requests and errors of
`pkg/graph` (`graph.NewChange`, `NodeCreate`, `MergeInput`, `ErrConflict`, ...), so `pkg/engine` imported the whole
graph and its storage (pgx, the SQLite driver), and `internal/graphsvc` imported the engine for the port and its
`Publisher`. The MCP hub and the gateway reach the whole platform the same way (through `graphsvc`, `pkg/access`
and `internal/telemetry`). The web hand-writes the types of the RPCs. The registry holds two things: the domains,
which define the graph, and the methodologies, compiled by the engine's compiler and planned by its planner.

## Decision

### 1. Components, and who may depend on whom

- **contracts**: the protos and the generated code (Go, then TypeScript for the web), the pure model (`pkg/domain`,
  `def`, `algo`, `authz`, `events`, `operations`, `builtins`, `form`, `mcp`, the item use cases) and the contract of
  the graph, `pkg/graphapi`. Depends on nothing of the platform.
- **lib**: the shared execution libraries (`typecat`, `guard` (CEL), `dsl` (goja, yaegi), `access`).
- **graph**: `pkg/graph` and what reads it (`graphsnap`, `okf`, `prov`, `index`), its service, the **domain
  registry**. Never the engine.
- **engine**: `pkg/engine`, the methodology compiler and conditions, the journal, the observer, its service and
  sandboxes, the **methodology registry**. Never the graph: it defines its ports with the types of the contract.
- **mcp**: the hub, the connector kit and the connectors. **gateway**: the gateway and the credentials. Neither
  the graph nor the engine: they call them through the generated clients.
- **platform**: the composition (`goap-dev`, the services that are none of the above) and the tests that need the
  graph and the engine together.
- **web**: the generated TypeScript only.

Each rule is first a `pkg/layering` rule in the one module; the components become Go modules of a `go.work` once
every rule holds.

### 2. The graph's contract

`pkg/graphapi` holds the requests and answers of the operations on a change and the errors of the graph (`ErrNotFound`,
`ErrConflict`, `ErrInvalid`, `ErrNothingToCommit`), and `IsWorking` / `DraftBranch`; it imports `pkg/domain` only.
`pkg/graph` keeps their names as aliases (`graph.NewChange = graphapi.NewChange`), so `*graph.Graph` still implements
`engine.GraphPort`. `internal/graphsvc` declares its own `Publisher` (same method as the engine's); the check that
`graphsvc.Client` is an `engine.GraphPort` is a test.

### 3. Two registries

The domain registry goes with the graph (it defines the graph, which loads its catalogue from it). The methodology
registry goes with the engine: it compiles, serves `engine.MethodologyPort`, implements the port the graph defines
for the methodology of a change (`graph.ChangeLifecycles`) and stores its nodes through the graph's contract.
Publishing a domain no longer asks the methodologies: the domain registry publishes and emits an event, the
methodology registry checks the methodologies again, and a methodology a new domain version breaks is suspended,
never the domain nor the graph. A methodology stays compatible with its domains when it is updated (warnings at its
publication). Migrating the data of a domain is a separate matter (a change rewriting the nodes it touches).

## Consequences

- `go list -deps ./pkg/engine` holds neither `pkg/graph` nor a database driver; `internal/graphsvc` no engine type.
- The engine's unit tests still build a real graph (`graph.NewMemory`): they move to a fake port or to the
  platform's integration tests before the engine becomes a module.
- Next steps: the MCP hub and the gateway off the graph and the engine (the graph client out of `graphsvc`,
  `access` behind a port reading the organisation, `telemetry` split), the two registries, the engine's tests, the
  generated TypeScript, the `go.work`.
