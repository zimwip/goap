# Porting `graph/graph` to Rust (`goap-graph`)

**Status: complete.** Every Go file of the package (slices G1, G2, G3) has its Rust module and every Go test
function its Rust counterpart, except `BenchmarkDraftRead` (see the end). The Go package stays in place until the
orchestrator retires it. This note records what the crate holds and the internal API.

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

`lib.rs` no longer has `#![allow(dead_code)]` (removed in G3). Items only the tests call are `#[cfg(test)]`
(`Graph::integrate_tx`, `DraftCache::folded`).

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
  `new_fixture` (NEED-1, REQ-1, TST-1 and their links, like Go's `newFixture`, now landed through `import_node` /
  `import_link`), and `land(g, ns, &[Landing])` (one applied change written directly, for G1's world tests).
- `commit_change(g, id, name)`, `integrate_change(g, id)` and `apply(g, id, name)` (testsupport.rs) call the real
  `Graph::commit_change` / `integrate_change` (no resolutions) / `apply`.
- `assert_kind(r, ErrorKind::Invalid, ..)` accepts `NothingToCommit` too (Go's `errors.Is(err, ErrInvalid)`).
- tests/helpers.rs holds the fixtures of Go's checkout_helpers_test.go (`ImportNode` / `import_node`, `import_link`,
  `import_props`, `Edit` / `edit`, `accept`, `accept_on`, `accept_all`, `accept_impact`, `propose_or_create`,
  `draft_of`, `seen_impact`, `checked_out`, `version_count`, `touch`, `assert_err`, `state_of`); the worlds of the
  test files are `pub(crate)` for reuse: `LcWorld` (tests/lifecycle.rs), `AlgoWorld` (tests/freeze.rs), `FlowWorld`
  (tests/flownodes.rs), `Docs` (tests/restructure.rs), `Lifecycles` (tests/transition.rs, Go's `lifecycles`),
  `MergeGate` (tests/lifecycle.rs).
- `check_impact_logs` compares the drafts of a cold graph with `fold_drafts` from scratch (ADR 0079) and checks the
  verdict causes.
- Tests changing `CHECKPOINT_EVERY` hold `baselinedelta::CHECKPOINT_LOCK` (`set_checkpoint_every`, test only).

## Operation annotations

Every operation method carries `/// goap:operation <code> <doer|reviewer>` (the 21 of Go, same codes and kinds) and
calls `self.allow(ctx, id, operations::CONST).await?` first. goap-opgen now reads the Rust sources (they hold a
directive): a dry run of `goap-cmd-goap-opgen -src graph/graph -out <tmp>` reports no problem, and the catalogue
differs from the committed `contracts/operations/src/operations_gen.rs` only in the `method` names (`Graph.attach_change`
for `Graph.AttachChange`) and in error words of a few docs. Regenerate it (outside this crate):
`GOAP_UPDATE_OPERATIONS=1 cargo test -p goap-opgen catalogue_is_current` (until then that test fails).

## Slice G2: the work on a change before it lands (done)

| Go | Rust | Notes |
|---|---|---|
| draft.go | draft.rs | `draftReader` (`new_draft_reader`), `seen_drafts` / `seen_draft`, `change_node_view` / `change_node` / `change_node_view_by_key`, `fork_draft`, `new_draft_link` |
| draftcache.go | draftcache.rs | `DraftCache` (LRU, `draft_cache_size`, default 128), `Graph::draft_state`, `Graph::drafts`; a transaction that appended to the log publishes nothing |
| checkout.go | checkout.rs | create / checkout / update / link create-update-delete / cancel / transition / withdraw; `check_accepted`, `edited_again` |
| changeimpact.go | changeimpact.rs | `propose_impact`, reviews (`review_tx`, `impact_node_review_batch`, `put_review_record`), `reopen_impacts`, `check_modifies` |
| changeimpact_apply.go | changeimpact_apply.rs | `Applier::prepare_change_impacts`, `write_version`, `walk_change_impacts`, `walk_transitions`, `Graph::land_impacts` (Go's `land`) |
| modification.go, impactvalidation.go | modification.rs, impactvalidation.rs | `draft_modifies`, `impact_validations` |
| lifecycle.go (rest), transition.go (rest) | lifecycle.rs, transition.rs | `check_transition`, `child_of`; `transition_change`, gates, phases and freeze, `check_guardians` |
| restructure.go | restructure.rs | `impact_node_merge` / `impact_node_split`, `check_origins`, `derived_nodes`; the gate is `GateFn` (synchronous) |
| flownodes.go (rest), flow.go, flowdiff.go | flownodes.rs, flow.rs, flowdiff.rs | `FlowNodes` adopt / discard, `open_flow`, `adopt_flow`, `discard_flow`, `flows`, `diff_flows` |
| option.go (rest), decision.go | option.rs, decision.rs | options, `blackboard` / `blackboard_in`, `change_graph`, `compare_options`; decision points through `decision_policy` |
| changeview.go, browse.go | changeview.rs, browse.rs | `change_view` (`VIEW_WRITTEN` / `VIEW_ACCEPTED` / `VIEW_LANDED`), `baseline_nodes`, `baseline_links`, `node_neighbourhood`, `namespaces` |

Ported ahead of G3 because G2's views and stand-ins need them (complete, from their Go files):

- mergecheck.rs: the whole of mergecheck.go (`check_merge`, `own_guardian_merge`, `merge_blackboard`,
  `main_impacts_of`).
- dependency.rs: `check_dependencies` (with G1's `dependency_of`, `dependencies_of`); `SetChangeDependencies` and
  `ChangeDependencies` are G3's.
- rebase.rs: `has_commits`, `Graph::withdrawn_since`, `Graph::unsettled` from rebase_committed.go (the rest of that
  file is G3's).

Operation annotations: every operation method of the G2 files carried `/// pending goap:operation <code> <kind>` (switched to `/// goap:operation` in G3)
(checkout.rs 9, changeimpact.rs 2, restructure.rs 2, as in Go) and calls `self.allow(...)` first.

Tests: every *_test.go of the G2 files is ported under `src/tests/` with the same test names in snake case, over
every repository (`for_each_repo` and its after-test checks): draft, draftcache, checkout, changeimpact,
modification, impactvalidation (memory only, like Go), lifecycle, transition, freeze, restructure, review,
flownodes, flow, flowdiff, option, decision, changeview, browse, planned, resolve. They pass on memory, SQLite and
PostgreSQL, and with `GOAP_MATERIALIZE_EVERY` 1, 3 and 100000.

Differences from Go:

- The Go validators and actions of the tests are written in JavaScript (the Rust DSL runs no Go scripts).
- `BenchmarkDraftRead` (draftcache_test.go) is not ported (no benchmark harness in the crate).
- `goap_domain` does not export the type `StateMove` (`Change::state_moves` returns it): a need of goap-domain
  (`pub use transition::*`), not blocking.

The two tests deferred from G2 (`TestChangeImpactsMerge`, `TestWithdrawACommittedImpact`) are ported in G3.

## Slice G3: the commit, the rebase and the merge (done)

| Go | Rust | Notes |
|---|---|---|
| commit.go | commit.rs | `commit` (`Commit` / `CommitEdits` of goap-graphapi), `check_required_parent`, `check_required_links` |
| apply.go | apply.rs | `commit_change`, `apply`, fast-forward, landing |
| changebranch.go | changebranch.rs | `integrate_change(ctx, id, &BTreeMap<NodeID, Resolution>)`, `shared_nodes` (`SharedNode`), `node_by_key_on` |
| merge.go | merge.rs | `create_branch`, `branches`, `branch`, `set_branch_status` / `_description`, `plan_merge`, `merge_branch` (`MergeRequest`, `MergeResult`, `NewBranch`), `merge_props`, `merge_links`, `common_ancestor` |
| rebase.go, rebase_committed.go | rebase.rs | `rebase_change`, `rebase_state`, `impact_node_resolve`, `commit_state` (`CommitState`), `rebase_committed_tx`, `record_in_parent` |
| subchange.go | subchange.rs | `sub_changes`, `split_by_owner` |
| attach.go | attach.rs | `attach_change`, `detach_change` (`HEADER_PARENT_ID`, `HEADER_GUARDIANS`) |
| dependency.go | dependency.rs | `set_change_dependencies`, `change_dependencies` (`HEADER_DEPENDS_ON`) |
| move.go | move_change.rs | `move_change` (`HEADER_PROJECT_ID`); `move` is a keyword |
| upgrade.go | upgrade.rs | `upgrade_change` (`HEADER_METHODOLOGY_VERSION`) |
| purge.go, tag.go | purge.rs, tag.rs | `purge_change`; `tag_change`, `tags`, `delete_tag`, `state_after` |
| snapshot.go | snapshot.rs | `export_snapshot`, `import_snapshot`, `parse_snapshot` and the `Snapshot*` types |
| (graphapi.Port) | port.rs | `impl goap_graphapi::Port for Graph`: so goap-journal's `Log` (blanket over `Port`) works over a `Graph` |

Tests (G3): attach, branches, changebranch, commit (+ commitcycle, parenting), concurrency, dependency, domainrules,
facet (+ logfacet), journal_log, landed, move_change, prov, purge, rebase_root, snapshot, strictattrs (+ attrcheck),
subchange, subchange_nested, tag, typecat, upgrade, validate, validators_levels (in impactvalidation.rs). A test whose
Rust name is not the snake case of Go's carries a `// Go: TestName` marker. 184 tests; they pass on memory, SQLite
and PostgreSQL with `GOAP_MATERIALIZE_EVERY` 1, 3 and 100000.

Differences from Go:

- Deep async futures overflow the 2 MiB test-thread stack in debug builds: the heavy awaits are `Box::pin`ned
  (apply.rs, changebranch.rs, rebase.rs, `for_each_repo`). A downstream crate driving several commits / merges in one
  future may need the same.
- An empty `Resolution.props` is treated as Go's nil (no override). `records_execution` (purge) is deterministic.
  A snapshot's `exported_at` is in UTC; a malformed snapshot reports serde's error text.
- concurrency.rs runs its writers as futures polled together (`join_all`) on a multi-threaded runtime instead of
  goroutines.

Not ported: `BenchmarkDraftRead` (draftcache_test.go): no benchmark harness in the crate.
