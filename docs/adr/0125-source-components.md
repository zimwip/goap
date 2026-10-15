# ADR 0125 — Source components: a contract, the graph, the engine, the MCP hub, the gateway, the platform

**Status**: accepted and implemented: the contract of the graph (`graphapi`), the engine and the graph service off each
other, the gateway, the hub and the connectors off the graph and the engine, the two registries, the engine's tests
without the graph, the web's types generated and checked, the rules between components, and the `go.work` of seven
modules (§8) · **Date**: 2026-10 · Builds on ADR 0061 (`pkg/layering`), ADR
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
  `algo`, `authz`, `events`, `operations`, `builtins`, `form`, `mcp`, `llm`, the journal's records, the item use cases
  and their policies: `risk`, `assess`, `verify`, `review`, `criticality`, `decision`) and the contract of the graph,
  `pkg/graphapi`. Depends on nothing of the platform.
- **lib**: the shared libraries: the domain model (`def`, `domains/builtin`), `typecat`, `guard` (CEL), `dsl` (goja,
  yaegi), `adapter`, the built-in MCPs (`mcpbuiltin`), `access`, `llmcfg`, `graphsnap`, `brief`, and the service
  plumbing (`internal/platform`, `identity`, `telemetry`, `rpcerr`, `pbconv`, the schema and PostgreSQL test helpers),
  with the clients of the graph and of the domain registry (`graphclient`, `domainclient`, `domainpb`).
- **graph**: `pkg/graph` and what reads it (`okf`, `prov`, `index`), its service, the indexer, the **domain
  registry**. Never the engine.
- **engine**: `pkg/engine`, the planners, the methodology compiler and conditions, the observer and self-improvement,
  the evaluation, its service and sandboxes, its telemetry, the **methodology registry** (still in the platform: see
  §7). Never the graph: it defines its ports with the types of the contract.
- **mcp**: the hub, the connector kit and the connectors. **gateway**: the gateway and the credentials. Neither
  the graph nor the engine: they call them through the generated clients.
- **platform**: the compositions (every `cmd/*`, `goap-dev` first), the services that are none of the above (the
  model gateway, preferences, conversations and the assistant, the event stream, the development data) and the tests
  that need several components together (`internal/e2e/*`).
- **web**: the generated TypeScript only.

Each rule was first a `pkg/layering` rule in the one module; once every rule held, the components became the Go
modules of a `go.work` (§8). The sections below name the packages by their path before the move; §8 gives the map.

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

The tests of the engine that run it on a real graph are the platform's: `internal/e2e/engine` (the runs of a
methodology, the node and decision operations, the companions, the sdlc scenarios). What stays in `pkg/engine` tests the
engine alone (planners, the loop budget, the scope, the preview, the pure helpers) and imports no graph. The integration
tests reach the few unexported workings they check through `engine.Internals` (`InternalsOf(e)`: schedule a run, set
the clock, apply node or decision operations, the host of an action, ...), which no production code calls. A fake
graph port was not written: the scenarios check the graph's rules as much as the engine's, and a fake would have to
repeat them.

### 6. The web's types

`buf generate` writes the TypeScript of the contract into `web/src/gen` (`protoc-gen-es`, with the JSON forms
`<Message>Json` the web reads over Connect JSON). The hand-written types of `web/src/lib/api/types` stay, as they narrow
enums and document the fields, but `contract.test.ts` compares each one named like a message with it: a field the
message has and the type lacks, or the reverse, fails, unless the exception is listed with its reason. New fields can
use the generated types directly (`NodeType.structure` is a `StructureTagJson`). The first run found two drifts, fixed
here: the roles allowed to run an agent or an action (ADR 0043) did not cross the registry's protocol (the methodology
editor lost them on save), and several web types missed fields the services send.

### 7. Every package in a component, tests included

`pkg/layering` places every package of the repository in its component (`components`, by the longest path prefix: a
new package that matches none fails) and checks every import, the tests' included, against what its component may
depend on (`dependsOn`, `TestComponents`): contracts on nothing, lib on contracts, graph / engine / mcp / gateway on
contracts and lib, the platform on all. The tests that need two components together are the platform's
(`internal/e2e/<name>`, an external package `<name>e2e` each): the engine on a real graph (`engine`, `enginesvc`,
`selfimprove`, `condition` for the conditions over a change's blackboard), the hub and the connectors over a graph
the graph service seeded (`mcpsvc`, `connectorkit`, `builtin`), the access read from such a graph (`access`). What
stays in a component tests it alone: the graph's lifecycle tests stand for the guardian with `pkg/guard` rather than
`pkg/condition`, the graph service's boot test writes its own development data rather than `internal/devseed`'s. The
command interceptor every service installs moved from the event stream to `internal/identity`. The methodology
registry stays in the platform until it moves to the engine (it still names the graph's service in its tests); every
`cmd/*` is the platform's composition of the components it serves.

### 8. The go.work

Each component is a Go module, one directory at the root of the repository: `contracts`, `lib`, `graph`, `engine`,
`mcp`, `gateway`, `platform`, joined by `go.work`. A module's path is `github.com/zimwip/goap/<component>` and its
packages sit directly under it (no `pkg/`, no `internal/`: a package another module imports cannot be internal):
`pkg/graph` is `graph/graph`, `internal/graphsvc` `graph/graphsvc`, `pkg/domain` `contracts/domain`, `pkg/domain/def`
`lib/def`, `domains/builtin` `lib/domains/builtin`, `internal/telemetry/enginetel` `engine/enginetel`, `gen`
`contracts/gen` (buf's `go_package_prefix`), `cmd/*` `platform/cmd/*`, `internal/e2e/*` `platform/e2e/*`,
`pkg/layering` `platform/layering`. The protos stay in `proto/`, the data (`domains/alm.yaml`, `methodologies/`,
`evals/`) and the web at the root.

Each `go.mod` requires the modules its component may depend on, with a `replace` to the sibling directory, so a module
builds alone (`GOWORK=off`), and the images keep building from the root (`Dockerfile`: the go.mod files first, then
`./platform/cmd/<service>`). The workspace lets a package import any of its modules whatever its `go.mod` says, so
`platform/layering` checks the imports, tests included (`TestComponents`), and the requirements of each `go.mod`
(`TestModuleRequirements`); `make lint` refuses a `go.mod` that is not tidy. `./...` at the root matches no package:
`make` and `make.ps1` name the modules (`PKGS`). The Go scripts of the methodologies and domains keep importing the DSL
as `github.com/zimwip/goap/pkg/dsl`: it is the name `lib/dsl` exports to the interpreter, data that outlives the
layout. `devlocal` is unchanged (one `goap-dev`, rebuilt by air from `./platform/cmd/goap-dev`).

## Consequences

- `go list -deps ./engine/engine` holds neither `graph/graph` nor a database driver; `graph/graphsvc` no engine type.
- `go list -test -deps ./engine/engine` holds no graph either (`platform/layering`, `TestTestLayering`); `engine.Internals`
  is used by tests only (`TestEngineInternalsOnlyInTests`).
- Each component is a module with its own `go.mod`: `engine` requires neither `graph` nor `mcp`, `mcp` and `gateway`
  neither `graph` nor `engine`; only `platform` requires them all.
- The methodology registry still lives in the platform (its tests use the graph's service); moving it to the engine is
  the next step of §3.
