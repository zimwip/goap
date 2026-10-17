# goap-engine: port status

Port of the Go package `engine/engine` to the Rust crate `goap-engine` (tokio).

## Status per Go file

Every Go source file has a Rust module (`src/<name>.rs`):

- Process model, ports, store, scope, tracer, broker: `process.rs`, `ports.rs`, `scope.rs`, `tracer.rs`, `broker.rs`.
- Engine, cycle and planners: `engine.rs`, `cycle.rs`, `planners.rs`.
- Run mechanics: `steps.rs`, `scoped.rs`, `foreach.rs`, `awaiting.rs`, `unblock.rs`, `board.rs`, `flow.rs`,
  `persistent.rs`, `history.rs`, `oversight.rs`, `resume.rs`, `locks.rs`, `cancel.rs`.
- Executors and host: `executors.rs`, `host.rs`, `condense.rs`, `gotemplate.rs`, `journal.rs`, `items.rs`.
- Graph operations: `nodeops.rs`, `decisions.rs`, `derogation.rs`, `merge.rs`, `rebase.rs`, `form.rs`.
- Choreography and triggers: `companion.rs`, `triggers.rs` (with `cron.rs`, a robfig-style parser), `eventing.rs`.
- Read models: `progress.rs`, `changestate.rs`, `startingpoints.rs`, `asks.rs`, `assessmentstates.rs`,
  `preview.rs`.
- Test hooks: `internals.rs` (`Engine::internals_of`).
- Tests: `src/tests/` (unit, planners, preview, scope; a `NullGraph` fake in `fakes.rs`), `condense.rs`, `cron.rs`
  and `gotemplate.rs` inline tests.

Every Go file is ported and every Go test has a Rust counterpart. `cargo test -p goap-engine` (35 tests),
`cargo clippy --no-deps -p goap-engine --all-targets -- -D warnings` and `cargo fmt -p goap-engine` are clean.

Not covered here: the engine's own tests use fakes only (`NullGraph`), so the paths that run on a real graph
(cycles, nodeops, merges, companions, triggers) are exercised only by the platform's end-to-end suite
(`platform/e2e/engine`), still to be ported. Differences from Go to keep in mind:

- `Engine` is shared as `Arc<Engine>`; methods that spawn or recurse take `self: &Arc<Self>`.
- `TriggerManager::new` returns an `Arc`; the cron scheduler is `cron.rs` (robfig standard syntax, UTC), one tokio
  task per schedule entry.
- `Store::orphan_finder` replaces Go's `Store.(OrphanFinder)` assertion.
- `Internals` (`Engine::internals_of`) keeps the Go hooks for the e2e tests; `cron_fire` is async.

## Public API

Construction:

```rust
let mut e = Engine::new(graph /* Arc<dyn GraphPort> */, methodologies /* Arc<dyn MethodologyPort> */,
                        store /* Arc<dyn Store> */);
e.executors.insert(KIND_LLM.into(), Arc::new(LLMExecutor { .. }));   // + tool, human, builtin, script
e.scope = Some(Arc::new(AuthzScope { authz: Some(authorizer), hub: Some(tool_port) }));
e.events = Some(publisher); e.llm = Some(client); e.locks = Some(locker); e.signals = Some(signals);
let e = Arc::new(e);
```

Runs: `start(&Arc, ctx, StartRequest) -> Process`, `run(&Arc, ctx, id)`, `answer`, `submit(ctx, id, items)`,
`submit_form(ctx, id, values)`, `approve(ctx, id, approve, comment)`, `cancel(ctx, id, reason)`,
`unblock(ctx, id, UnblockRequest)`, `attach_change`, `resolve_merge`, `resolve_rebase`, `relaunch`, `decide_flow`,
`resolve_board`, `flows_of`, `accompany(ctx, &TriggerEvent)`.

Read models: `progress`, `explain_condition`, `change_state`, `starting_points`, `change_asks`,
`assessment_states`, `may_run_agent`, `with_history`, `step_dependencies`; free function `preview_plan`.

Replicas: `recover_loop(ctx, every)`, `woken`, `interrupted`; `drain()` waits for the background work;
`set_clock`.

Triggers: `TriggerManager::new(engine)` / `with(engine, min_interval, leader)`, `start`, `stop`, `reload`,
`states`, `handle`, `fire`, `watch_processes(ctx, &Broker)`; `declared_events`, `trigger_event_of`.
`EventingGraph { inner, on_event }` wraps an in-process graph.

Traits to implement:

- `Store`: `get`, `put`, `list`, `append_process_log`, `list_process_log`, `orphan_finder` (default None);
  `MemoryStore` is provided.
- `Locker` (`acquire -> (bool, holder)`, `release`, `interrupt`), `OrphanFinder` (`orphans(ctx, before)`),
  `Signals` (`wake`, `interrupt`).
- `Scope`: `as_actor`, `has_tools`, `tools`, `call_tool`, `allowed`, `may_run`, `may_step`; `AuthzScope { authz,
  hub }` is provided.
- `MethodologyPort` (`StaticMethodologies` provided), `Publisher` (`Broker`, `Publishers`), `Tracer` (`NopTracer`).
- `Executor`: `execute(ctx, ActionContext) -> ExecResult` (`Result<ActionResult, ExecError { partial, error }>`).
  Provided: `LLMExecutor`, `ToolExecutor`, `HumanExecutor`, `BuiltinExecutor` + `default_builtins()`,
  `ScriptExecutor` over `Sandboxes` / `InprocSandboxes`; the `call` kind is run by the engine itself.
