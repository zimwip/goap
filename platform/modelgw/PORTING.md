# goap-modelgw: notes of the Rust port

The crate ports the Go package `platform/modelgw` (the `.go` files beside `src/`): providers (Anthropic,
OpenAI-compatible, Gemini, fake) with native tools and the text tool protocol, routing, retries and quotas, the
ledger `llm_call` and its exchanges (both SQL dialects), behaviours with their measured cost and calibration, the
Anthropic proxy, the helper (`Suggest`), the Connect handler and client of `model.v1`.

## Tests not ported

These Go tests need the real graph, `graphtest` or `graphsvc`, none of which is ported yet:

- `TestEngineRowsFindTheirExchangeInTheChangeLog` (`exchange_test.go`): it runs the engine (`engine.Engine`,
  `methodology.LoadFile`) on a `graph.Graph` seeded with `graphtest`, through the gateway.
- The re-seed assertion of `TestDefaultConfigIsSeededOnce` (`service_test.go`): "an administered graph must not be
  re-seeded" checks `graphsvc.SeedModels`. The rest of that test is ported.

## Tests ported on a fake graph

Every other Go test is ported (`src/tests/`). The Go tests read the configuration from a `graph.Graph` seeded with
`graphsvc.SeedModels` and edited with `graph.Commit`. Neither is ported, so the Rust tests use a fake instead:
`tests/helpers.rs` `FakeGraph`, which implements `goap_graphsnap::Graph`. It gives a new baseline at every edit and
keeps the nodes of the platform namespace. `seed` stands in for `SeedModels`: it seeds when no provider node exists.
`update` and `retire` stand in for the edits and the move to `retired`. The fake also holds the `env:` values that
the test secrets resolve, so no test sets a process variable.

Once graph/graph and graph/graphsvc are ported, run these tests again on the real graph:

- `service.rs`: `test_service_policy`, `test_provider_key_is_a_reference`, `test_default_config_is_seeded_once`,
  `test_embed`
- `suggest.rs`: `test_suggest_authorization`
- `ledger*.rs`, `exchange.rs`, `behavior.rs` and `behaviorcost.rs`: every test, through `ledger_service` and
  `new_cost_harness`
- `anthropicproxy.rs`: every test

## Differences from Go

- `config::ModelsConfig` (providers, models, aliases) stands in for `graphsvc.ModelsConfig`, which `BootConfig`
  returns in Go. It should move to graphsvc once graphsvc is ported.
- The Anthropic provider sends its own HTTP requests (Messages API, `?beta=true`, the server-side fallback beta) and
  handles retries the way the SDK does. Go uses the Anthropic SDK.
- `Proposal.value` is the raw JSON text, a `String`. `Discovered`, `ProviderView` and `CatalogEntry` hold the record
  in a field (`model`, `provider`) instead of embedding it. `BehaviorCost` holds its `CostKey` as `key`.
  `CostView.measured_at` is an `Option`.
- The Anthropic proxy writes the ledger row once the provider's answer is read to its end, just before the body
  sent to the client closes. Go writes it after the handler returns.
- A `Service` must be built with `Service::new(..)` and then `.shared()`, which gives an `Arc` that knows itself.
  Only a shared service runs the background calibrations. The fields are set before sharing it.
- Logging goes through `tracing`; there is no `Log` field.
- The guard `TestEveryModelCallGoesThroughTheLedger` (`tests/ledger_guard.rs`) reads the workspace's `.rs` files with
  `syn`. It skips test code (`#[cfg(test)]`, `tests/`, `tests.rs`, `*_test.rs`).
