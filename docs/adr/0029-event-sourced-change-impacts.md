# ADR 0029 — Event-sourced change impacts

**Status**: accepted, implemented · **Date**: 2026-09 · Extends ADR 0017 (flow branches, the blackboard as a log),
ADR 0024 (change impacts), ADR 0025 (flows on change impacts). Replaces the branch lookups of the flow view (ADR 0025
§1, §3) and the row rewrites of adoption (§5.3).

## Context
ADR 0017 makes the blackboard of a change an **append-only log**: the facts (items) are inserted, never rewritten, and
their effective status (in effect, stale, candidate, superseded) is a replay of the log for a flow. ADR 0024 then made
the **change impacts** the core of the blackboard, but stored them as **mutable rows**: declaring, writing, reviewing,
superseding and landing a change impact overwrite the row (`PutChangeImpact` upserts `post`, `review`, `reviews`,
`landed`, `superseded`, `flow`, `recheck`). So:

- the history of a change impact is lost: only its last state is kept, and only the declaration and the version write
  record the action run (`execution`) behind them; who superseded, re-based or landed it is not recorded;
- a change impact has **one stored `post`**, the main flow's. A flow's post is resolved by looking up the flow
  branches, then the change branch minus the stale versions (ADR 0025 §1), and adoption **rewrites** rows (§5.3), with
  an ordering trick so the partial unique index `change_impact_live` holds. Every new flow case (parallel flows, a flow
  inside a flow) has to be added to that resolution code.

## Decision
Every operation on a change impact is an **event** appended to the change's impact log, with its caller. The state of
the change impacts, for the main flow or for any flow, is a **fold** of that log.

### 1. The log
`change_event` (both SQL dialects, and the memory repository): `id`, `change_id`, `seq` (order within the change),
`impact_id` (empty for a change-level event), `op`, `flow`, `execution`, `by`, `payload` (JSON), `created_at`.
Insert-only: nothing updates or deletes an event.

### 2. Events (`domain.ImpactEvent`)
| op | payload | written by |
|---|---|---|
| `declared` | the change impact as declared | `AddNodes`, the split into sub-changes, a merge |
| `written` | `post` (the version written), on `flow` | `WriteNode`, `RealizeNode`, a merge, adoption |
| `reviewed` | the review (status, comment, by, flow, execution) | `ReviewNodeOn`, the split, a merge |
| `discarded` | the rejection of a candidate whose flow was discarded | `DiscardFlow` |
| `adopted` | change level: the flow and its stale executions | `AdoptFlow` |
| `landed` | the version on the target branch | `land` (apply) |
| `rebased` | the new `pre` of a planned change impact, to re-check | `land`, for the other open changes |
| `imported` | the whole change impact | the migration of data recorded before the log (§6) |

Every event carries its **caller**: `by` (the principal, or the component: `graph.merge`, `graph.split_by_owner`),
`execution` (the journal record of the action run, which gives the process, the step and the action: ADR 0011) and
`flow` (the flow branch it was made on, empty for the main flow). Writes on a flow are recorded too, including writes
on change impacts the flow did not declare: the log is complete.

### 3. The fold
`domain.FoldImpacts(events)` gives the stored state (the one ADR 0024 described): a change impact per `declared`
event, in declaration order; `written` sets `post` when made on the main flow or on the flow that declared the change
impact; `reviewed` appends the review and sets `review` when made on the main flow; `discarded` rejects; `adopted`
applies ADR 0025 §5.3 to every change impact (the flow's become the main flow's, the stale ones and their reviews are
superseded, `review` is recomputed); `landed` and `rebased` set `landed` and `pre` + `recheck`.

`domain.FoldImpactsFor(events, flow, chain, stale)` gives **what a flow sees** (ADR 0025 §3): the change impacts
declared on the main flow or on the flow's chain, minus the ones a stale run declared; `post` is the last version
written on the innermost flow of the chain that wrote one, else the last one written on the main flow by a run that is
not stale; `review` is the last review on the chain or the main flow that is neither superseded nor stale. No branch
lookup: the view is a pure function of the log.

### 4. The projection
The `change_impact` table stays, as a **projection** of the log: each event is applied to it in the same transaction
(`Graph.emit`), so reads (`Change.Nodes`, the indexes, `NodeChangeImpacts`) are unchanged. It is never written
directly. A test replays the log of every change after each graph test and compares it with the projection.

### 5. Flows
- A flow's view is the fold of §3, so parallel flows and flows inside flows need no code of their own: each view keeps
  the events of its chain.
- Adoption appends `adopted`, then one `written` per change impact whose node got an `adopt` version (ADR 0025 §5.2,
  unchanged: versions are still written on the change branch). Discarding appends `discarded` per candidate.

- A flow inside a flow may relaunch a step of its parent flow: the versions that step wrote on the parent's branch are
  stale for it. The branch lookup of ADR 0025 read the latest version of a flow branch whatever run wrote it; the fold
  applies the stale runs to the writes of the chain too.

### 6. Migration
Change impacts recorded before the log get one `imported` event with their whole state, the first time the graph
opens the store (`Graph.MigrateImpactEvents`, idempotent: a change that has events is skipped).

### 7. Reading the log
`ListChangeEvents(change)` returns the log; the change tab shows it as a timeline (who, which action run, which flow,
what changed).

## Consequences
- **Storage**: table `change_event` (PostgreSQL `0017`, SQLite `0016`). The projection is unchanged.
- **Code**: `pkg/domain/impactevent.go` (events, folds), `pkg/graph/impactevents.go` (`emit`, migration), the write
  paths of `changeimpact.go`, `changeimpact_apply.go`, `flownodes.go`, `merge.go`, `subchange.go` emit events instead of
  writing rows; the flow view (`flowNodes.nodes`) folds the log. `Graph.Caller` gives the caller of an operation (set by
  the graph service from the principal; `pkg/graph` does not depend on `pkg/authz`).
- **Tests**: the graph tests replay the log of every change after each test, on every repository, and compare it with
  the projection (`checkImpactLogs`); parallel and nested flows on change impacts have a test of their own; the SDLC
  engine test checks that every declaration and write of a run is linked to its action run.
- **Cost**: one event per operation, and a flow view reads the log of the change instead of one branch lookup per
  change impact.
- **Not changed**: the facts (items) keep their own log (ADR 0017); node versions are still written on the change and
  flow branches, and adoption still writes `adopt` versions (the graph must hold the reviewed content). Moving the
  facts into the same log is left for later.
