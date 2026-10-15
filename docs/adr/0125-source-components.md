# ADR 0125 — Source components: a contract, the graph, the engine, the MCP hub, the gateway, the platform

**Status**: accepted; steps 1 to 4 implemented (the contract of the graph, `pkg/graphapi`; the engine and the graph
service no longer import each other; the gateway, the hub and the connectors off the graph and the engine; the two
registries; the engine's tests without the graph), the next steps not yet · **Date**: 2026-10 · Builds on ADR 0061 (`pkg/layering`), ADR
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

In the sources: the domain registry is `internal/domainsvc` (`registry.v1.DomainService`, its store and migrations),
served by `cmd/graph`, which seeds the shipped domains and reloads its type catalogue in process (`Service.Changed`);
the methodology registry, `internal/registrysvc`, reads it through its port `registrysvc.Domains` (the service in
process, `internal/domainclient` over RPC) and the graph through `graphapi`. The shapes both speak are the
contract's: `def.Record`, `def.Status`, the protocol converters `internal/domainpb`. A suspension is never stored: a
published methodology version is suspended while it does not compile against the domains in force (what the graph
judges its writes by: the pins of ADR 0113 choose the types a version compiles against while it is in force, they do
not shield it from the graph it writes to). `DomainsChanged` (on the domain events) and `Recheck` (at start, a
built-in domain changing with the platform) publish `goap.registry.methodology.suspended` / `resumed`;
`MethodologyVersion` and `Methodology` refuse a suspended version (`ErrSuspended`), `List` and the hub leave it out,
the protocol carries the reason (`Methodology.suspended`, `MethodologySummary.suspended`), the methodology editor
shows it.

### 4. The gateway and the MCP hub

The client of the graph service leaves `graphsvc` (`internal/graphclient`), the interface both the engine and the
built-in MCPs call becomes the contract's (`graphapi.Port`, `engine.GraphPort` an alias of it: when two consumers
need the same port it is the contract's), `pkg/access`, `llmcfg` and `graphsnap` name the graph through the contract
(`graphapi.NodeValidator`, `ValidatorQuery`, the errors), and the telemetry of the engine leaves the telemetry every
service sets up (`internal/telemetry/enginetel`). Since the registries are split (§3), `cmd/mcp` reads the domains
through `internal/domainclient` and the methodologies through the generated registry client, never compiled: it
depends on neither the graph, the engine nor the methodology model.

### 5. The engine's tests

The tests of the engine that run it on a real graph are the platform's: `internal/enginee2e` (the runs of a
methodology, the node and decision operations, the companions, the sdlc scenarios). What stays in `pkg/engine` tests the
engine alone (planners, the loop budget, the scope, the preview, the pure helpers) and imports no graph. The integration
tests reach the few unexported workings they check through `engine.Internals` (`InternalsOf(e)`: schedule a run, set
the clock, apply node or decision operations, the host of an action, ...), which no production code calls. A fake
graph port was not written: the scenarios check the graph's rules as much as the engine's, and a fake would have to
repeat them.

## Consequences

- `go list -deps ./pkg/engine` holds neither `pkg/graph` nor a database driver; `internal/graphsvc` no engine type.
- `go list -test -deps ./pkg/engine` holds no graph either (`pkg/layering`, `TestTestLayering`); `engine.Internals`
  is used by tests only (`TestEngineInternalsOnlyInTests`).
- Next steps: the generated TypeScript, the `go.work`.
