# Porting `platform/registrysvc` to Rust (`goap-registrysvc`)

Every Go test is ported (`src/tests/`). The Go package imports `graph/graph` only in its tests, so `goap-graph` is a
dev-dependency and the crate's own code does not depend on it.

## Tests over the graph

- `src/tests/realgraph.rs` holds what the tests need of the real graph: `impl StoreGraph for goap_graph::Graph`,
  `Late` (the registry plugged as the graph's `ChangeLifecycles` and `SubChangeValidator`, bound once the registry over
  a store over that graph exists: Go sets `g.Lifecycles = reg` afterwards) and `Probe` (what a test reads of a graph,
  for both the real graph and `FakeGraph`).
- Over the real graph only (as in Go): `TestChangeLifecycleGate`, the `Apply` paths of `TestActivityGoalsMetGatesApply`
  / `TestActivityGoalsMetRefusesAnUnresolvableRef` and the `CreateChange` path of `TestSubChangeActivityCascade`
  (`src/tests/changelifecycle.rs`), `TestGuardianChain`, `versions_test.go`, `suspend_test.go`.
- Over both `FakeGraph` and the real graph: `TestGraphStore*`, `TestReferencesAreLinks` (both typed by the built-in
  meta-domain), `TestImportPublishedIsOneChange`, `TestPublishStubsMissingAlias*`, and the tests over `stores()`
  (memory, the fake, the real graph).
- The direct checks of the registry (`activity_goals_met_decides_the_merge`, `sub_change_activity_cascade`, ...) stay,
  on hand-made blackboards over `FakeGraph`.

`TestClientActivatesAsTheRegistry` is ported in process (the `Client` over the `Handler` through `Client::with_service`),
with no HTTP server.

## Left to the compositions (goap-dev, cmd/registry)

Go's `Service` satisfies `graph.ChangeLifecycles` structurally, and `*graph.Graph` satisfies `StoreGraph`. In Rust a
composition running the registry over the graph in process wraps them in newtypes (the orphan rule, and no normal
dependency on `goap-graph` here): the registry as `goap_graph::ChangeLifecycles` / `SubChangeValidator` (the inherent
methods `lifecycle`, `merge`, `guardians`, `check_guardians`, `integrates`, `default_goal`, `methodology_version`,
`guard`, `gate`, `sub_change_validator`, errors mapped with `Error::into_graph`), and the graph as `StoreGraph`.
`src/tests/realgraph.rs` is the model of both. `GraphStore` over `goap_graphclient::Client` needs nothing.

## Layering (ADR 0125 §3)

`goap-domainsvc` is a dev-dependency only. The crate provides no `impl Domains for goap_domainsvc::Service`, and no
`From<goap_domainsvc::Error>`. A composition that runs the domain registry in process wraps it in a newtype that
implements `Domains`, mapping its errors with `Error::domains` (see `InProcess` in `src/tests/mod.rs`).
`goap_domainclient::Client` implements `Domains` here.
