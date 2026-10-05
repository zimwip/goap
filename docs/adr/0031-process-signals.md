# ADR 0031 — Process signals, deferred change binding, trigger leadership, private vars

**Status**: accepted, implemented (engine/MCP layer; the web UI for gap 7 below is not yet
built) · **Date**: 2026-09 ·
Builds on ADR 0011 (execution journal), ADR 0017 (flow branches), ADR 0024 (change impacts),
ADR 0028 (built-in MCPs and connectors, scope), ADR 0029 (event-sourced change impacts),
ADR 0030 (one change log).

## Context
A walkthrough of the engine surfaced several related gaps:

- A sub-agent call (`ctx.runAgent`) could only hand a child process a one-shot `intent` at spawn
  and block until it went terminal; there was no way for a live parent to be woken early, or for
  either side to push a follow-up after the first turn.
- A methodology's triggers could only react to a fixed, hardcoded event vocabulary
  (`change.created`, `process.completed`, …) — an agent had no way to define and emit its own
  named event for another agent's trigger to react to.
- `TriggerManager` ran independently on every engine replica: with N replicas, every event and
  every cron tick fired N times (`triggers.go` carried a "milestone M1" comment acknowledging
  this).
- `Process.Vars`, meant as each process's own scratch space, was aliased rather than cloned at
  sub-agent spawn — harmless only because nothing yet wrote to it after spawn.
- A process could not exist before a Change was identified: `selectTarget` always created or
  reused one synchronously the moment methodology/agent/goal identification finished, so an
  intake-style agent (see `methodologies/sdlc.yaml`'s `intake`) had to eager-create a throwaway
  change and abandon it if a better match turned up later.
- Several concurrent processes can already share one `ChangeID` (every sub-agent does); nothing
  isolates or explicitly attributes their event streams, and it was unclear whether the existing
  `Flow` machinery (ADR 0017) was the right tool for that.
- A process's own execution history (in particular the `StatusClarifying` identification
  dialogue) was embedded as plain fields on the `Process` struct, so every conversational turn
  rewrote the entire `Process` row — cheap when a process was always about to attach immediately,
  expensive once a process can run many turns unbound.

## Decision

### 1. `KindSignal` — one primitive for parent/child messaging and custom trigger events
The first two gaps above are the same gap seen from two callers: both want a named,
payload-carrying fact that something else reacts to. `pkg/domain/change.go` gains
`KindSignal ItemKind = "signal"` and `ChangeItem.Target string` (a process id the signal
addresses; `""` = broadcast) — no storage migration, `ChangeItem` is already the JSON payload of
a `change_log` row (ADR 0030). `Ctx.Signal(name, data, target)` (`pkg/dsl/dsl.go`) and a new
`goap-change/signal` tool (`internal/connectors/builtin/change.go`, `pkg/mcpbuiltin/builtin.go`) both
write it, mirroring the existing `AddArtifact`/`note` paths.

On the read side, `"change.signal"` was added to `methodology.TriggerEvents`
(`pkg/methodology/methodology.go`); `EventingGraph.AddItems` (`pkg/engine/eventing.go`) emits it
whenever a written batch includes a `KindSignal` item, and `TriggerEvent` gained an
`Items []domain.ChangeItem` field so a trigger's CEL `Filter` can match on payload content
(`event.type == "change.signal" && event.items.exists(i, i.type == 'review_needed')`), not just
the event type.

### 2. `WakeOn` — a live parent can be woken early, without a second suspend mechanism
`dsl.Result`, `ActionResult` and `HumanTask` gained `WakeOn []string` (`pkg/dsl/dsl.go`,
`pkg/engine/process.go`). A script suspending on `ctx.RunAgent` can declare which signal names,
if the child emits one addressed to it, should wake it early instead of only at child
termination. When a `KindSignal` item lands targeting a live parent whose `Pending.WakeOn`
matches, the engine reuses the exact same `resumeParent` path already used at termination — the
parent's action replays from the top (unchanged existing behavior) and re-suspends via the
normal `runChild` path if the child is still running; `resumeParent`'s existing
`Pending.Kind != TaskAgent` guard already makes a duplicate/late wake a harmless no-op, so no
separate debounce primitive was added.

The mirror direction — a parent sending a child a follow-up — needed no engine change at all: the
parent emits a signal targeted at the child's process id, and the child, which already re-reads
its blackboard every cycle, sees it on its next tick. This is deliberate: it respects "agents
plan, actions execute" (CLAUDE.md rule 6) — a running action step is never interrupted mid-flight,
only the next planning cycle sees new information. `llm` actions are not given the same
synchronous suspend contract (a poor fit for a model mid tool-loop); the idiom there is
`goap-scheduler/start` plus a `Trigger{type: event, event: "process.completed"}` (or
`"change.signal"`) — reusing §1's mechanism rather than a second suspend path.

### 3. Private `Vars`
`Process.Vars` is now cloned (`maps.Clone`), not aliased, at `Start` and at sub-agent spawn
(`pkg/engine/engine.go`). `Ctx.SetVar(name, v)` (`pkg/dsl/dsl.go`) lets a script set a
process-private variable, buffered like `Signal`/`AddArtifact` and merged into the owning
process's `Vars` (copy-on-write) after the step runs. `Vars` stays engine-owned execution state,
not a graph write — it is not routed through a Change (same category as `Plan`/`World`/`Steps`,
already non-graph `Process` fields). Children keep the **same** `ChangeID` as their parent:
isolation here is about scratch data, not splitting the change, which would break ADR 0024's
"impacts reviewed together." Once `Vars` is private, a `Signal` is the only sanctioned way a
private fact crosses into a parent's or sibling's view — every such crossing is a journaled
change item.

### 4. Trigger leadership across engine replicas
`cmd/engine/main.go` replaced two plain NATS core `events.Subscribe` calls (fan-out — every
replica received every message) with one `events.ConsumeDurable(ctx, log, "trigger-manager", …)`
call: a JetStream durable pull consumer under the same durable name on every replica gives
competing-consumer semantics for event triggers with no new dependency. Cron has no equivalent
per-message semantics, so a small JetStream-KV-backed lease was added
(`internal/platform/lease.go`, `Events.NewLease`/`Lease.Acquire` — create-if-absent,
renew-if-own, steal-if-stale-past-ttl via KV `Create`/`Get`/`Update` with revision-based CAS).
`TriggerManager` gained a `Leader func() bool` (`pkg/engine/triggers.go`); only the `cron.AddFunc`
callback (`cronFire`) is gated by it — `Reload`, `States()` and manual `fire` stay available on
every replica regardless of leadership. `Leader == nil` defaults to always-leader, so every
existing single-process deployment and test is unaffected. This resolves the "milestone M1"
comment for trigger firing specifically — see Consequences for what it does *not* resolve.

### 5. Deferred change binding: `change_bound` as ordinary condition data, `Engine.AttachChange`
**A correction made mid-design, worth recording**: an early draft of this decision ANDed a
synthetic `change_bound` fact onto every goal's precondition, reasoning it would centrally
enforce CLAUDE.md rule 2 ("every modification is a Change"). That was wrong: rule 2 gates *graph
writes*, not a process's existence or goal completion. A process is free to run standalone —
gathering context, asking questions, doing pure reads — for as long as it likes, and its goal can
be satisfied having never attached to any Change, if it never needed to write. The actual
enforcement of rule 2 already existed and is correctly scoped: `Host.permitted`/`Host.CallTool`
(`pkg/engine/host.go`) rejects any `goap-change` write op or change-scoped `goap-graph` call when
`p.ChangeID == ""`, with no duplication needed at the planning layer.

So `change_bound = (p.ChangeID != "")` (`pkg/engine/engine.go`, set on `p.World` inside
`observe`) is ordinary condition data a methodology *can* reference in its own `Pre`/`Effects`,
never a universally-injected requirement. `observe` also skips `Graph.BlackboardIn` (which
hard-errors on an empty id) when unbound, returning `domain.Blackboard{Vars: p.Vars}` instead.

Binding stays eager by default — zero migration for every existing methodology. `selectTarget`
routes its default create-or-reuse call through the new
`Engine.AttachChange(ctx, processID string, req AttachRequest) error`
(`AttachRequest{ChangeID, Title, Intent, Namespace, OwnerOrg, BaselineID}`) instead of calling
`CreateChange` directly, so every binding — eager or deferred — goes through one path and is
journaled (`journal.attach`, a new `ExecutionRecord` kind) and eventing (`"process.attached"`,
added to `TriggerEvents`, fired for every bind) uniformly. `selectTarget` skips its default bind
only when the resolved `Agent` structurally declares its own action with
`Effects["change_bound"]` (`agentBindsOwnChange`) — an opt-out signal, not a flag to set; no
existing methodology declares this today, so behavior is provably unchanged for all of them. A
methodology that wants a real orphan phase declares such an action and calls the new
`goap-scheduler attach` op (`{process, change?, title?, intent?, namespace?, baseline?}`, scope
`agent`, proto `AttachChangeRequest`/`AttachChangeResponse` added to `engine.proto`) either with
an existing `change` id (reuse) or none (create). `sdlc.yaml`'s `intake` agent was updated to call
`attach` instead of spawning a second process and abandoning its own change when a better match
is found — narrower than fully deferring its bind (see Consequences).

Two events on attach, kept deliberately separate: the fixed `"process.attached"` (system,
"agent management" — a bind happened) and the custom `"change.signal"` (user, what was decided
and why, if the action chooses to emit one). Neither replaces the other.

### 6. Gap 6: automatic per-process flow tagging was investigated and rejected
An early design for attributing concurrent processes' facts proposed defaulting each process's
`Flow` to its own process id, skipping `Graph.OpenFlow`. This was **investigated and dropped**: a
flow name that was never opened via `Graph.OpenFlow` returns a completely empty view from
`Change.View(flow)` (`pkg/domain/flow.go`) — defaulting a child's `Flow` without opening it would
have made every concurrent child's blackboard read empty, breaking every sub-agent scenario that
reads shared facts. `Flow` in this codebase is a content-branch precedence/visibility axis (ADR
0017), a different thing from "who wrote this." Attribution turns out to already exist and need
no new mechanism: every `journal.*` row already carries `Process = r.ProcessID`
(`pkg/domain/changelog.go`), and `execution` (already a column on journal and fact rows) links
any fact back to the process/action-run that produced it — exactly what the Audit UI's process
grouping already uses.

What was kept: `StartRequest.Flow string` as a plain, never-auto-populated pass-through, so a
caller can explicitly place a process on an *already-opened* flow (the hook a future "solution
branch," below, needs). Impacts (actual proposed graph/node mutations) intentionally do not
follow any per-process isolation — they land on the change's own branch exactly as before,
regardless of which process produced them. A bounded retry (3 attempts) on `graph.ErrConflict`
was added around `applyNodeOps`'s three graph-mutating calls, wrapped individually rather than
retrying the whole function (a partial retry must not re-declare an already-declared change
impact). Caveat: `graph.ErrConflict` is one sentinel shared by roughly ten different conditions
in `pkg/graph`, only some of them transient version races — the retry cannot yet tell them
apart, so a non-transient cause just fails identically three times instead of once (harmless,
mildly wasteful, not incorrect).

Solution branches — a distinct, explicit concept, not automatically one-to-one with a process —
are where real impact isolation and merge-conflict resolution belong, and this decision
intentionally does not design that further: a human or agent can still explicitly open a real
flow (ADR 0017, unchanged) for an alternative proposal that might conflict and need a considered
merge later; several concurrent processes could even share one solution branch. For that path,
today's existing reject-on-divergence behavior (`adoptNodes` → `ErrConflict`) is the interim
outcome. A real per-property merge already exists as working code, but for a different
mechanism — merging two separate Changes' branches (ADR 0024 §5, `pkg/graph/merge.go`'s
`MergeProps`/`Resolution`) — porting it into `AdoptFlow` is left for a later pass.

### 7. `process_log` — a process's own append-only history, independent of a Change
`Process` and its Change/blackboard are two distinct things: a process holds its own local
execution state and is connected, or not, to a blackboard/Change; its own audit trail connects to
the change's audit trail only from the point it actually acts on that change (an `AttachChange`
bind, and every write after). `journal.*` records already carry `process_id` and live fine in
`change_log` regardless of whether the row also has a `change_id`. What didn't have a home
independent of a Change was the `StatusClarifying` identification dialogue
(`Process.Intent.Turns`, `Question`, `Candidates`) — plain fields embedded directly in the
`Process` struct, so every conversational turn rewrote the *entire* `Process` row. Deferred
binding (§5) makes this worse: an unbound process can now run a whole multi-turn `llm`-driven
intake conversation before ever attaching, and every turn paid that cost.

A new `process_log` table (`internal/enginesvc/migrations_sqlite/0002_process_log.sql`; no
PostgreSQL migration yet, see Consequences) holds it instead: `seq, process_id, type, payload,
at`, indexed on `(process_id, seq)`, append-only, mirroring `change_log`'s shape and spirit (ADR
0030) but scoped to a process regardless of whether it has a Change. `Engine.Store` gained
`AppendProcessLog`/`ListProcessLog` (both the SQLite-backed store and the in-memory `MemoryStore`
used by tests and `goap-dev`). `resolveIntent`/`Answer` (`pkg/engine/engine.go`) now append one
`process_log` entry per turn; the SQLite store's `Put` strips `Intent.Turns` before marshaling
the row body and its `Get`/`List` fold the turns back in from `process_log`
(`fillIntentTurns`) — the `Process.Intent`/`Question`/`Candidates` fields themselves were kept as
an in-memory/serialization convenience (so `ProcessToPB` and the web UI keep working unchanged);
only the *persistence* path moved off the full-row rewrite.

This is also the primitive gap 5's `llm`-action follow-up needed: an `llm` action deferring its
own bind has no `Vars`-equivalent way to persist turn-by-turn state while unbound (item writes
are correctly rejected with no Change). `process_log` is that primitive; a DSL/tool-call
convenience wrapper for it, following the `SetVar`/`goap-change signal` pattern, is not yet
built (Consequences).

`process_log` and `change_log` stay separate stores, cross-referenced only by `process_id`
(already a `change_log` column and index) — nothing copies or merges one into the other on
attach; both are simply readable side by side for the same process id from that point on.

## Consequences
- Agents can coordinate live (wake-on-signal) and methodologies can define and react to their own
  named events, without a new store, bus, or MCP concept — everything still goes through a Change
  when it needs to (rule 2), and stays out of one when it doesn't (§5's correction).
- Trigger firing is now safe with multiple engine replicas. This resolves the M1 comment for
  *trigger* leadership specifically — it is **not** the broader engine-clustering work
  (architecture.md §3.2, still 🟡: per-step work-queue distribution of process execution itself),
  which remains a separate, larger, unimplemented target.
- Sub-agents get real private scratch space; a signal is the only sanctioned way a private fact
  becomes visible to a parent or sibling.
- A process can exist, run, and finish entirely unbound. Binding is eager by default for every
  methodology written before this change; a methodology can opt into a real deferred-bind phase
  by declaring its own binding action.
- A process's own history (starting with its identification dialogue) no longer forces a
  full-row rewrite per turn.

**Follow-ups, explicitly out of scope here**:
- Gap 7's web UI: `RunTab.svelte` rendering a process's trail standalone (via `process_log`, once
  a read API for it is added — not yet built either), a top-level "My processes" view (the
  engine/API side already supports this fully, `Engine.Store.List`/`goap-scheduler list` have no
  `ChangeID` dependency; only the UI entry point is missing), and the bidirectional nav link with
  `ChangeAudit.svelte`.
- Solution-branch impact isolation and a real per-property merge-conflict resolution path for
  concurrent writers who explicitly want one (§6) — `KindMerge` remains a declared but
  never-constructed placeholder item kind.
- A distinguishable error for genuine version-race `ErrConflict`s, separate from the sentinel's
  other, non-transient causes (§6).
- A PostgreSQL `process_log` migration — no PostgreSQL-backed process store exists yet
  (`cmd/engine/main.go` still uses `NewMemoryStore`, itself a "milestone M1" item); `process_log`
  today only ships for the SQLite local-dev store and the in-memory one.
- A `SetVar`/`goap-change signal`-style DSL convenience wrapper over `process_log` for a fully
  deferred `llm`-action's own turn-by-turn state (§7) — the intake agent migration in
  `sdlc.yaml` stopped short of this for exactly that reason and only replaced its
  throwaway-change-and-abandon workaround with a direct `attach` call.
