# ADR 0073 — `goap-dev` as builders, the web API client as modules

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0053, 0070, 0071.

## Context

`cmd/goap-dev/main.go` was one 350-line `main` wiring the whole platform (stores, graph, registry, seeds, model gateway,
node index, MCP hub, engine, triggers, HTTP routes) with `platform.Fatal` on every error and a forward-declared
`triggers` variable the change and registry hooks closed over: nothing could build it without exiting the process, so
nothing tested it. `web/src/lib/api.ts` was 2,600 lines (transport, 190 exports of proto3 shapes, every service client,
the session calls, display helpers) imported by 85 files.

## Decision

- **`goap-dev`**: `newApp(ctx, cfg, log) (*app, error)` calls one builder per concern, in the order of the startup
  sequence of ADR 0071 (unchanged): `openStores`, `buildGraph`, `buildRegistry` (hooks, domains, type catalogue, `Boot`,
  methodologies), `buildPlatform`, `buildEngine`, `buildServer` (`buildAuth` for the mode's middleware). Each returns a
  small struct (`graphPart`, `registryPart`, `platformPart`, `enginePart`) and an error, never exits. `run(ctx, cfg)` builds
  and serves (`platform.Server.RunContext`, which also stops with its context; `Run` is `RunContext(Background)`), `main`
  loads the configuration and is the only caller of `platform.Fatal`. The late binding of the trigger manager is a
  `triggerRef` (an atomic pointer) that the change hook and the registry hook hold; events before `buildEngine` are
  ignored, as before. The wiring stays in `package main` (no `internal/devapp`): the test lives next to it.
- **Smoke test** (`app_test.go`): the whole platform on a temporary SQLite database with `GOAP_AUTH_MODE=none`, served
  through `httptest` over the Echo handler (`/readyz`, `/api/status`, `/api/whoami`, a graph RPC), and `run` stopping with
  its context; `-race` clean (about 20 s under the race detector, 2 s without).
- **`cmd/engine` is left alone**: it wires the engine over RPC clients, NATS events, a lease-based trigger leader and an LLM
  ranker; only the executor table is literally shared, which is not worth a package.
- **Web API**: `web/src/lib/api.ts` re-exports `web/src/lib/api/`: `transport.ts` (base URL, token store, `RpcError`,
  `rpc`, `reportUnauthorized`), `types/{common,registry,access,graph,engine,models,mcp,nodeIndex}.ts` (shapes only, but
  `TRIGGER_EVENTS` and `ITEM_SUPERSEDED`), one client per service (`registry`, `graph`, `engine`, `models` with
  `preferencesApi`, `mcp`, `nodeIndex`), `status.ts`, `authApi.ts` (the session calls; named so it does not suggest
  `stores/session.svelte.ts`) and `helpers.ts`. No module imports a module above it; the types do not import clients. The
  importers are unchanged; `api.test.ts` covers the transport. `Int64`, `FlowOrigin`, `FlowEvent`, `OptionSpec` and
  `DecisionPolicy` moved down (common, graph) to keep the engine and graph shapes acyclic. The Go tests that read the web
  (`pkg/events` for `TRIGGER_EVENTS`) point at the new file.

## Consequences

Behaviour does not change. A new platform part has a place in `newApp` and a builder; a new call is added to the module of
its service. Not done: splitting the other large web files.
