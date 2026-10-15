# ADR 0029 — Event-sourced change impacts

**Status**: accepted, implemented · **Date**: 2026-09 · Extends ADR 0017 (flow branches, the blackboard as a log),
ADR 0024 (change impacts), ADR 0025 (flows on change impacts). Replaces the branch lookups of the flow view (ADR 0025
§1, §3) and the row rewrites of adoption (§5.3). The storage is the one log of the change (ADR 0030); the operations of
§2 were renamed and extended by ADR 0076 / 0077 (checkout, node operations) and ADR 0079 (drafts: the events carry the
working state of a node, no version is written before landing); §7's reading is `ListChangeLog` (ADR 0030 §3).

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
The impact events are entries of the log of the change (`impact.<op>`), with its facts and journal records, in one
order ([ADR 0030](0030-one-change-log.md)); the impact log is that log filtered on the `impact.` stream. Insert-only:
nothing updates or deletes an event.

### 2. Events (`domain.ImpactEvent`)
The first list (`declared`, `written`, `reviewed`, `discarded`, `adopted`, `landed`, `rebased`) recorded a declaration
and each version written on a change or flow branch. Since ADR 0076 / 0077 / 0079 a node has a draft, not a version,
while a change works on it, and the events carry that draft:

| op | payload | written by |
|---|---|---|
| `proposed` | the change impact as proposed (the modification of an existing node) | `ProposeImpact`, `SplitByOwner`, `ImpactNodeMerge` / `ImpactNodeSplit`, a merge, `recordInParent` (ADR 0103) |
| `created` | a new node: the change impact and its initial draft, on `flow` (in the merge change: a version) | `ImpactNodeCreate`, a merge |
| `checkedOut` | the draft of an existing node, on `flow` | `ImpactNodeCheckout` |
| `updated` | a patch of the draft (properties, owner, links) | `ImpactNodeUpdate`, `ImpactLinkCreate` / `Update` / `Delete` |
| `transitioned` | a lifecycle move of the draft; carrying a draft, one an adoption installs on the main flow | `ImpactNodeTransition`, `AdoptFlow`, a merge |
| `cancelled` / `withdrawn` | the draft of a flow dropped / the change impact taken out of the change | `ImpactNodeCancel` / `WithdrawImpact` |
| `reviewed` | the review (status, comment, by, flow, execution) | `ImpactNodeReviewBatch` (every verdict, ADR 0105) |
| `discarded` | the rejection of a candidate whose flow was discarded | `DiscardFlow` |
| `adopted` | change level: the flow and its stale executions | `AdoptFlow` |
| `landed` | the version the change applied, in the baseline it names: on the change's own branch when committed, then again on the branch it is merged into (the last one is the version on the target) | `commitOnBranch` (commit), `land` (integration), `recordInParent` |
| `rebased` | the new `pre` of a planned change impact, to re-check | `land`, for the other open changes; the rebase of a committed change |

`integrated` (ADR 0081) is only read, in old logs: a sub-change merges into its parent's branch since ADR 0103.

Every event carries its **caller**: `by` (the principal, or the component: `graph.merge`, `graph.split_by_owner`),
`execution` (the journal record of the action run, which gives the process, the step and the action: ADR 0011) and
`flow` (the flow branch it was made on, empty for the main flow). Writes on a flow are recorded too, including writes
on change impacts the flow did not declare: the log is complete.

### 3. The fold
`domain.FoldImpacts(events)` gives the stored state (the one ADR 0024 described): a change impact per `proposed` or
`created` event, in declaration order; `created`, `checkedOut` and `transitioned` set `post` (the draft reference, ADR
0079) when made on the main flow or on the flow that declared the change impact; `reviewed` appends the review and sets
`review` when made on the main flow; `discarded` rejects; `adopted` applies ADR 0025 §5.3 to every change impact (the
flow's become the main flow's, the stale ones and their reviews are superseded, `review` is recomputed); `landed` and
`rebased` set `landed` and `pre` + `recheck`.

`domain.ImpactsSeenBy(impacts, events, chain, stale)` gives **what a flow sees** (ADR 0025 §3): the change impacts
declared on the main flow or on the flow's chain, minus the ones a stale run declared; `post` is the last post set on
the innermost flow of the chain that set one, else the last one set on the main flow by a run that is not stale;
`review` is the last review on the chain or the main flow that is neither superseded nor stale. No branch lookup: the
view is a pure function of the log. The drafts themselves are folded from the same events (`domain.FoldDrafts`,
`DraftSeenBy`, ADR 0079).

### 4. The projection
The `change_impact` table stays, as a **projection** of the log: each event is applied to it in the same transaction
(`Graph.emit`), so reads (`Change.Nodes`, the indexes, `NodeChangeImpacts`) are unchanged. It is never written
directly. A test replays the log of every change after each graph test and compares it with the projection.

### 5. Flows
- A flow's view is the fold of §3, so parallel flows and flows inside flows need no code of their own: each view keeps
  the events of its chain.
- Adoption appends `adopted`, then one `transitioned` carrying the flow's draft per change impact it installs on the
  main flow (ADR 0079; it first wrote an `adopt` version on the change branch). Discarding appends `discarded` per
  candidate.
- A flow inside a flow may relaunch a step of its parent flow: the drafts that step made on the parent flow are stale
  for it. The branch lookup of ADR 0025 read the latest version of a flow branch whatever run wrote it; the fold
  applies the stale runs to the writes of the chain too.

### 6. No migration
The platform starts from an empty store: there is no data recorded before the log to convert.

### 7. Reading the log
`ListChangeEvents(change)` returns the impact events; the whole log is read with `ListChangeLog` (ADR 0030 §3). The
**Audit** pane of the change tab merges it with the execution journal (processes, plans, actions with their model and
tool calls, human approvals: ADR 0011) and the facts of the blackboard (items, flow events: ADR 0017) into one
chronological trail: when, what, on which flow, by whom and through which action run, each entry expanding to its full
record. The change is drawn as a flow of events: one lane per flow branch, the main flow on the left; a flow forks from
its parent where it is opened (several flows side by side to compare alternatives), and merges back where it is adopted
or ends where it is discarded. The scheduling of the runs is in the trail too (the journal's `schedule` records: why
each run happened, by whom, after what). It filters by source, flow, action run and text, and exports the trail (CSV),
the log entries shown (JSON) and the whole log as PROV-O (ADR 0057). A run scheduled in the background acts for the
principal who started the process, so its events name that principal; the graph client forwards the principal of its
context in distributed mode.

## Consequences
- **Storage**: the log of the change (ADR 0030). The projection is unchanged.
- **Code**: `contracts/domain/impactevent.go` (events, folds), `contracts/domain/draft.go` (drafts, ADR 0079),
  `graph/graph/impactevents.go` (`emit`), the write paths of `changeimpact.go`, `changeimpact_apply.go`, `checkout.go`,
  `flownodes.go`, `merge.go`, `restructure.go`, `rebase_committed.go`, `subchange.go` emit events instead of writing
  rows; the flow view (`flowNodes.nodes`) folds the log. `Graph.Caller` gives the caller of an operation (set by the
  graph service from the principal; `graph/graph` does not depend on `contracts/authz`).
- **Tests**: the graph tests replay the log of every change after each test, on every repository, and compare it with
  the projection (`checkImpactLogs`); parallel and nested flows on change impacts have a test of their own; the SDLC
  engine test checks that every declaration and write of a run is linked to its action run.
- **Cost**: one event per operation, and a flow view reads the log of the change instead of one branch lookup per
  change impact.
- **Not changed** at the time: the facts kept their own log and versions were still written on the change and flow
  branches. Since then the facts, the journal and the impact events are one log (ADR 0030) and a version is written
  only when a change lands, from its draft (ADR 0079).
