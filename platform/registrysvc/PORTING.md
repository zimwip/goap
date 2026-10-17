# Porting `platform/registrysvc` to Rust (`goap-registrysvc`)

The crate does not depend on `goap-graph`. Where Go's tests run the registry over the real graph, the Rust tests use
`src/tests/fakegraph.rs` (`FakeGraph`, an in-memory implementation of the `StoreGraph` port; `FakeGraph::typed()` is
strict about attributes and link types, as `typecat::builtin()` says). The tests below need the real graph, so they
are not ported yet. They are to be ported once `goap-graph` is available, in an e2e crate (`platform/e2e/*`) or here as
a dev-dependency.

## Tests left (they need `goap-graph`)

- `TestChangeLifecycleGate` (`changelifecycle_test.go`): `Graph.TransitionChange` gated by the registry as
  `ChangeLifecycles`.
- `TestActivityGoalsMetGatesApply` (`activitygoals_test.go`): the part that goes through `Graph.Apply` / the merge
  of a real change. The decision itself is ported as `activity_goals_met_decides_the_merge`, which calls `Service::merge`
  directly.
- `TestActivityGoalsMetRefusesAnUnresolvableRef`: same split. The direct check is ported, the `Apply` path is not.
- `TestSubChangeActivityCascade` (`subactivity_test.go`): the part wired through `Graph.CreateChange` with
  `SubChangeValidator`. `sub_change_validator` itself is ported (`sub_change_activity_cascade`).
- `TestGraphStore*`, `TestReferencesAreLinks`, `TestImportPublishedIsOneChange`, `TestPublishStubsMissingAlias*`:
  ported over `FakeGraph`. Run them again over `goap_graph::Graph` once it implements `StoreGraph`, to check the
  versioning and conflict semantics the fake only imitates.

`TestClientActivatesAsTheRegistry` is ported in process (the `Client` over the `Handler` through `Client::with_service`),
with no HTTP server.

## Code left (it needs `goap-graph`)

- `impl goap_graph::ChangeLifecycles for Service`. Because of the orphan rule it must live in this crate. Until then the
  methods are inherent on `Service` (`lifecycle`, `merge`, `guardians`, `check_guardians`, `integrates`,
  `default_goal`, `methodology_version`, `operation_roles`, ...), and `Error::into_graph()` is ready for the mapping of
  their errors.
- `impl StoreGraph for goap_graph::Graph`, so that `GraphStore` runs in process (it runs over `goap_graphclient::Client`
  today).
- `Service::sub_change_validator` and `merge` are to be plugged as `Graph.SubChangeValidator` / the guardian by the
  compositions.

## Layering (ADR 0125 §3)

`goap-domainsvc` is a dev-dependency only. The crate provides no `impl Domains for goap_domainsvc::Service`, and no
`From<goap_domainsvc::Error>`. A composition that runs the domain registry in process wraps it in a newtype that
implements `Domains`, mapping its errors with `Error::domains` (see `InProcess` in `src/tests/mod.rs`).
`goap_domainclient::Client` implements `Domains` here.
