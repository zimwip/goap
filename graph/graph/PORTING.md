# Porting `graph/graph` to Rust (`goap-graph`)

The Go package stays in place until every slice is ported. This note records what the Rust crate holds, the
internal API later slices build on, and the Go files still to port with the place each one plugs in.

## Slice G1: the foundation (done)

Ported, one Rust module per Go file:

| Go | Rust | Notes |
|---|---|---|
| graph.go | graph.rs | `Graph`, its seams, `NewChange`, create / update / list changes, `add_items`, `node_changes`, `view`, `baseline_graph`, `suspect_links` |
| api.go | api.rs | request / answer types |
| repo.go | repo.rs | `Repo`, `Tx` (async), `BaselineEntry`, `BaselineHeader` |
| memory.go | memory.rs | `Memory` |
| sqlite.go, postgres.go | sqlite.rs, postgres.rs, sqltx.rs | sqlx; `sqltx.rs` is the shared executor (`SqlTx` over a `SqlConn`), the dialect files keep the scanning and encodings |
| sqlbuild.go | sqlbuild.rs | `Dialect`, `Arg` (typed bound values instead of `[]any`) |
| changeimpact_sql.go | changeimpact_sql.rs | |
| guard.go (+ the observer of events.go) | guard.rs | `GraphTx` |
| changelog.go, impactevents.go | changelog.rs, impactevents.rs | `Graph::emit` is the only writer of the projection |
| state.go | state.rs | `StateCache`, `default_materialize_every` / `set_default_materialize_every` (GOAP_MATERIALIZE_EVERY) |
| baselinedelta.go, baselinediff.go | baselinedelta.rs, baselinediff.rs | |
| bootstrap.go, structure.go | bootstrap.rs, structure.rs | |
| events.go | events.rs | `EventSink`, `GraphEvent`, `Published`, `Graph::observe`, `republish` |
| operation.go | operation.rs | `Graph::allow` |
| validator.go, validate.go | validator.rs, validate.rs | |
| itemtypes.go, algorithms.go | itemtypes.rs, algorithms.rs | |

Both migration directories are embedded unchanged (`MIGRATIONS`, `SQLITE_MIGRATIONS`).

G1 also holds the pieces of other Go files the foundation needs, each in the module named after its Go file, so
the slice that ports the rest of that file completes the module instead of creating it:

| Module | What is there | What its slice adds |
|---|---|---|
| lifecycle.rs | `TypeIndex`, `types_at`, `lifecycle_controlled`, `invalidf`, the views a guard reads (`node_view`, `impact_view`, `draft_view`) | `checkTransition`, `childOf` |
| merge.rs | `Graph::branch_head`, `Graph::versions`, `branch_of_tx`, `branch_head` | the merges |
| changebranch.rs | `change_branch_name`, `own_branch`, `Graph::ensure_own_branch` | integrate |
| apply.rs | `Graph::advance_branch`, `next_version`, `Cpost` | the applier, `CommitChange` / `IntegrateChange` / `Apply` |
| changeimpact.rs | `change_open`, `find_change_impact` | the impact operations |
| flownodes.rs | `flow_chain`, `stale_of`, `node_of`, `FlowNodes` | the rest of flownodes.go |
| option.rs | `BlackboardFacet`, `LogFacet` / `LogFacetFold`, `log_entries`, `with_log_facets`, `facets_of`, `flow_ids_of` | options, `BlackboardIn` |
| subchange.rs | `prepare_sub_change`, `Graph::sub_changes`, `abandon_sub_changes`, `open_sub_changes` | `checkRequiredLinks`, the rest |
| transition.rs | `guardian_chain`, `chain_under` | `TransitionChange` |

`lib.rs` has a crate-level `#![allow(dead_code)]`: some foundation pieces have no caller until a later slice. Remove
it once the crate is complete.

## Internal API

### Errors and context

Every operation takes `&goap_ctx::Ctx` first and returns `crate::Result<T>` over `goap_graphapi::Error`
(re-exported with `ErrorKind`). `Error::wrap(ErrorKind::Invalid, msg)` is Go's `fmt.Errorf("msg: %w", ErrInvalid)`
(the text ends in `: invalid`); `invalidf` (lifecycle.rs) is the shorthand; `e.context(...)` prefixes a message.
Error texts are Go's: keep them so.

### Graph and its seams

`Graph::new(repo)`; the clock, the ids and the catalogue are set with `set_now`, `set_new_id`, `set_types`
(`Graph::types_at()` gives the `TypeIndex` a change is judged by). Each nil-by-default seam of Go is a public
`Option<Arc<dyn Trait>>` field: `authorizer`, `sub_change_validator`, `decision_policy`, `review_policy`,
`item_authorizer`, `item_policy`, `lifecycles` (`ChangeLifecycles`), `change_authorizer`, `operation_authorizer`,
`project_move_gate`; plus `validators: Vec<Arc<dyn NodeValidator>>`, `facets`, `log_facets`, `caller`,
`materialize_every`, `draft_cache_size`. The traits are in graph.rs and are `#[async_trait]`.

### Transactions: `Tx`, `GraphTx`, the guard

- `Repo::begin(ctx) -> Box<dyn Tx>`. `Tx` is async; `lock_change` / `lock_branch` are PostgreSQL advisory locks taken
  with `pg_advisory_xact_lock` (released by commit or rollback), nothing on SQLite and memory.
- `Tx::commit(self: Box<Self>)` and `Tx::rollback(self: Box<Self>)` (default `Ok`). `Tx::rolls_back_late()` is true
  for the SQL stores: a sqlx transaction dropped without commit rolls back only when its connection is next used,
  and on PostgreSQL it keeps its locks meanwhile.
- `Graph::begin(ctx) -> GraphTx<'_>`, `Graph::change_tx(ctx, id)` = begin + `lock_change` (open every operation on a
  change with it, or `change_open`). `GraphTx` is Go's guardTx and observedTx in one: it implements `Tx`, checks
  every write naming a change (owner, project, versions immutable once written, baselines from a change), verifies
  at `commit` like deferred foreign keys, then commits and publishes the events (best effort).
- Rollback: `GraphTx::rollback(self)` explicitly; dropping an uncommitted `GraphTx` over a SQL store spawns the
  rollback on the tokio runtime (its `Drop`), so an early `?` return never leaves a transaction holding locks. A
  later slice may keep using `?` freely inside a `GraphTx`.
- The memory store holds a mutex per transaction and `goap_platform::open_sqlite` opens one connection: never open
  a second transaction while one is open in the same task (it deadlocks).
- `Graph::emit(ctx, tx, events)` is the only writer of `change_impact` (ADR 0029).

### Test support (`#[cfg(test)] testsupport.rs`)

Tests are unit tests (`src/tests/*.rs`), so they reach `pub(crate)` items. Reuse:

- `for_each_repo(async |r: &TestRepo| { ... })`: memory, SQLite in a temporary file, PostgreSQL in a fresh schema
  when `GOAP_TEST_PG_DSN` is set; after each it runs `check_impact_logs`, `check_landings`, `check_states`
  (`check_verdict_causes` is called by `check_impact_logs`). `TestRepo::graph()`, `TestRepo::store` (the pool, for a
  test reading the database).
- `must`, `assert_kind`, `ctx()`, `props`, `root_org`, `root_project`, the organisation names (`NODE_TYPE_*`,
  `LINK_*`).
- Fixtures: `seed_node` (a version written directly in a change of its own), `pin_baseline`, `test_change`,
  `new_fixture` (NEED-1, REQ-1, TST-1 and their links, like Go's `newFixture`), and `land(g, ns, &[Landing])`.
  `land` is a stand-in for Commit / Apply: one applied change, the versions, a `proposed` and a `landed` event per
  node, a commit baseline, the head moved. Once `apply.rs` is ported, replace its body by a real commit (or keep it
  for the tests that only need a world) and port the Go tests that need Commit.
- `TestTypes` / `TestType` (Go's `testTypes`) and `use_types(&mut g, Arc<dyn TypeCatalog>)`.
- `check_impact_logs` does not yet compare the drafts with a fold from scratch (ADR 0079): add it with draftcache.rs.
- Tests changing `CHECKPOINT_EVERY` hold `baselinedelta::CHECKPOINT_LOCK` (`set_checkpoint_every`, test only).

## Operation annotations: warning for the later slices

G1 adds no `goap:operation` annotation. goap-opgen reads only the Rust files as soon as one Rust file of the
crate carries a `goap:operation` directive. Until every annotated Go method has a Rust port, write the doc line of
a ported operation as `/// pending goap:operation <code> <doer|reviewer>` and still call
`self.allow(ctx, id, goap_operations::CONST).await?` first; switch every placeholder to `/// goap:operation ...` in
the slice that completes the catalogue, then run `go generate ./contracts/operations/` (or its Rust twin).

## Go files left, and where each plugs in

| Go file | Plugs in |
|---|---|
| draft.go, draftcache.go | `Graph::drafts` (per-change cache over the log, validated by its last seq; field `draft_cache_size` is there), `draftReader`; complete `check_impact_logs` with the draft fold |
| checkout.go, modification.go, impactvalidation.go | `impl Graph` over `change_tx` + `emit`; `ImpactValidation`, `draft_modifies` |
| changeimpact.go, changeimpact_apply.go | changeimpact.rs (has `change_open`, `find_change_impact`); `ProposeImpact`, reviews (`reviewTx`, `ImpactNodeReviewBatch`), withdraw; uses `Cpost` and `run_node_validators` |
| commit.go, apply.go, mergecheck.go | apply.rs (has `advance_branch`, `next_version`); `CommitChange` / `IntegrateChange` / `Apply`, `writeVersion`, `checkMerge`; replaces the `land` stand-in |
| merge.go, rebase.go, rebase_committed.go | merge.rs (has `branch_head`); `MergeBranch`, `rebaseCommittedTx`, `RebaseChange` |
| changebranch.go (integrate) | changebranch.rs |
| subchange.go (rest), attach.go, dependency.go, move.go | subchange.rs; new attach.rs, dependency.rs, move.rs; gates through the seams already on `Graph` (`project_move_gate`, `lifecycles`) |
| option.go (rest), flow.go, flowdiff.go, flownodes.go (rest) | option.rs, flownodes.rs; new flow.rs, flowdiff.rs |
| decision.go | new decision.rs, through `decision_policy` |
| transition.go (rest), lifecycle.go (checkTransition) | transition.rs, lifecycle.rs; `run_guards` / `run_actions` / `TransitionEnv` of algorithms.rs |
| restructure.go | new restructure.rs (merge / split) |
| snapshot.go, browse.go, changeview.go | new modules, read-only, over `begin` |
| purge.go, tag.go, upgrade.go | new modules over `change_tx` |

The Go tests left follow their files (checkout_test.go with checkout.go, and so on); repos_test.go,
sqlbuild_test.go, guard_test.go, changelog_test.go, baselinedelta_test.go, baselinediff_test.go, structure_test.go,
events_test.go, schema_test.go, namespace_test.go, algorithms_test.go, operation_test.go, itemtypes_test.go and
validate_test.go are ported, the parts needing a later slice (Commit, ProposeImpact, flows) noted in each test
module.
