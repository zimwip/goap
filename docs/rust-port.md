# Porting GOAP to Rust

The platform moves from Go to Rust on the branch of this port. This page is the contract of the port: where things go,
how a Go construct is written in Rust, which libraries replace which. It goes once the Go code is gone (its rules then
move to `CLAUDE.md`).

## Layout

- One Cargo workspace at the root (`Cargo.toml`, `rust-toolchain.toml`: the stable channel, edition 2024).
- **One crate per Go package, in the same directory** (`contracts/domain/Cargo.toml` next to the `.go` files until the
  Go goes): the seven Go modules stay the seven top directories, and the dependency rules of ADR 0125
  (contracts ← lib ← graph / engine / mcp / gateway ← platform) are the dependencies of the crates, written once in each
  `Cargo.toml` from the Go imports. A crate is named `goap-<package>` (`goap-domain`, `goap-graph`, `goap-engine`),
  `goap-connector-<name>` under `mcp/connectors/`, `goap-domains-builtin` for `lib/domains/builtin`,
  `goap-e2e-<name>` under `platform/e2e/`, `goap-cmd-<name>` under `platform/cmd/` (its binary keeps the Go name:
  `goap-dev`, `graph`, ...). Every crate is a `[workspace.dependencies]` entry: depend with `{ workspace = true }`.
- `contracts/gen` (`goap-gen`) is generated at build time from `proto/` (prost + pbjson, compiled by protox: no `protoc`,
  no `buf` for Rust): `goap_gen::graph::v1::Node`, with serde in the protobuf JSON form (the one Connect's JSON codec and
  the web speak). `buf generate` still writes the web's TypeScript.
- `contracts/ctx` (`goap-ctx`) is Go's `context.Context` as an explicit value (`Ctx`): values by type, cancellation,
  deadline. Every function that took a `context.Context` takes `&Ctx` first.

## Writing Go in Rust

- Names: types keep their Go name (`Change`, `NodeRef`), functions and methods go snake_case (`ImpactNodeCreate` →
  `impact_node_create`), constants SCREAMING_SNAKE (`DefaultOrg` → `DEFAULT_ORG`) or an enum when they enumerate
  (`ChangeStatus`). The doc comments are carried over, in English.
- JSON is a contract (stored in the database, sent to the web, read from YAML): every struct serialized in Go keeps its
  exact field names (`#[serde(rename = "...")]` or `rename_all = "camelCase"` when it matches each tag), `omitempty`
  becomes `#[serde(default, skip_serializing_if = "...")]`, `map[string]any` is `serde_json::Map<String, Value>`
  (sorted keys, like `encoding/json`), `any` is `serde_json::Value`, `time.Time` is `chrono::DateTime<Utc>` (an
  `Option` when the Go code tests `IsZero`). A Go `[]T` that encodes as `null` when nil and `[]` when empty is a `Vec`
  with `skip_serializing_if` only where the tag has `omitempty`; check the Go test fixtures.
- Errors: one `thiserror` enum per package; Go's sentinels (`ErrConflict`, `ErrInvalid`, `ErrNotFound`, ...) are
  variants or a `kind()`, and `errors.Is(err, X)` becomes a method (`err.is_conflict()`), never a string match. A wrapped
  message keeps the Go text (tests and the web read some).
- Interfaces are traits (`Send + Sync` when they cross threads), held as `Arc<dyn Trait>`; a nil-by-default seam of a
  struct (`Graph.ReviewPolicy`) is an `Option<Arc<dyn ...>>`.
- Goroutines and channels: see *Execution model*; `sync.Mutex` is `parking_lot::Mutex`.
- Tests are ported with the code, test by test (same names in snake_case, the fixtures under `testdata/` shared), in the
  crate (`#[cfg(test)] mod tests` for the internal ones, `tests/*.rs` for the external `_test` packages). The suite stays
  green crate by crate: a crate is ported when its tests pass.
- No `unsafe` (`unsafe_code = "forbid"` for the workspace); `cargo fmt` and `cargo clippy -D warnings` must pass
  (`make rs-lint`).

## Libraries

| Go | Rust |
|---|---|
| protobuf + connect-go | prost + pbjson (`goap-gen`), a Connect layer of our own over axum (unary JSON and binary, server streaming, the Connect error format) in `lib/platform` |
| echo | axum (+ tower-http) |
| cel-go | `cel` |
| casbin | `casbin` (casbin-rs) |
| yaml.v3 | `serde_yaml_ng` |
| modernc sqlite | `rusqlite` (bundled, FTS5) |
| pgx + pgvector | to be chosen with the execution model (`sqlx` or `postgres`) + `pgvector` |
| nats.go | `async-nats` |
| golang-jwt | `jsonwebtoken` |
| x/crypto/bcrypt | `bcrypt` |
| robfig/cron | `cron` |
| google/uuid | `uuid` |
| goja (JavaScript sandbox) | to be chosen (`boa_engine` or `rquickjs`) |
| yaegi (Go scripts) | none in Rust: to be decided |
| OpenTelemetry | `opentelemetry` + `tracing` |

## Execution model

To be decided (async tokio throughout, or a synchronous core with threads and async only at the HTTP edge).

## Phases

1. Workspace, toolchain (`make toolchain` / `.\make.ps1 toolchain` install rustup and the pinned toolchain when
   missing), generated contracts, `goap-ctx`.
2. `contracts` (pure model, `graphapi`, `authz`, `operations` and its generator).
3. `lib`.
4. `graph`, `engine`, `mcp`, `gateway`.
5. `platform` (services, `cmd`, `e2e`, `layering` as a test over `cargo metadata`).
6. The switch: the make targets build, test and run the Rust binaries, the Dockerfile builds them, the Go goes.

While the Go is still there, `make build` / `test` / `lint` stay the Go ones and `make rs-build` / `rs-test` /
`rs-lint` run the Rust workspace.

## Open pull requests on Go code

A pull request still open on Go code when the port lands is reconciled by porting its diff to the Rust crate of each
package it touches (its Go tests show the behaviour to keep), on its branch rebased onto the port.
