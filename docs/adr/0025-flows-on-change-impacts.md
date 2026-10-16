# ADR 0025 — Flow branches on change impacts

**Status**: accepted, implemented; the post resolution by branch of §1 / §3 and the row rewrites of §5.3 are replaced
by the event log of [ADR 0029](0029-event-sourced-change-impacts.md); the graph branch of a flow and the versions it
writes (§1, §4, §5) by the drafts of [ADR 0079](0079-drafts-and-versions-at-landing.md) (a flow keeps no branch, its
drafts are its own, adopting installs them on the main flow); `FlowEvent.StaleExecutions` is `StaleRuns` since ADR 0067
· **Date**: 2026-09 · Extends ADR 0017 (flow branches) and ADR 0024 (change impacts). Replaces `MaterializeFlow` and the
flows of items.

## Context
ADR 0017 makes a relaunch of a step a **flow branch of the blackboard log**: the items of the relaunched step and of
what followed are *stale*, the replanned run appends *candidate* items, a human *adopts* (stale → superseded,
candidates count) or *discards* (candidates rejected, stale count again). This works because items are inert facts:
nothing is written to the graph before the change is applied, and `MaterializeFlow` replays the proposals on a graph
branch for review.

Change impacts (ADR 0024) then wrote node versions **eagerly** on the change branch. A step that runs again cannot be
"replayed" over versions that already exist, and the graph the flow reads and writes must not leak into the main
flow before adoption. Change impacts had no flow: a process on a flow branch could not use them.

## Decision
A flow was first a **graph branch forked from the change branch** (`flow-<id8>`, created on the first write of the
flow), and adopting it made the change branch look like the flow: a version `reason = adopt` per affected node copying
what the flow wrote, a tombstone for a node only stale runs created. Since ADR 0079 a node has no version during a
change: a flow keeps **no branch**, what it writes are **drafts of its own**, and adopting it installs them on the main
flow. Everything a flow writes stays its own; the main flow keeps what it has until the adoption.

### 1. What a flow owns
- **Change impacts it declares** (`ChangeImpact.Flow`): candidates until adoption, invisible to the main flow, to
  `Apply` and to the other flows.
- **Reviews it makes** (`Review.Flow`).
- **Drafts it writes**: one per node it creates or checks out on the flow (once per change and flow; a checkout on a
  flow copies the draft of the parent flow, `forkDraft`). A change impact has one main `post`; what a flow sees of a
  node is the draft of the innermost flow of its chain that holds one, the main flow last (`seenDrafts`,
  `domain.DraftSeenBy`), without what the stale runs wrote (§2). It was first the version resolved on the flow branch,
  then on its ancestors' branches.

### 2. Provenance, the basis of "stale"
- Every write records the journal execution that made it: the impact events carry `Execution`, so does a draft, and
  the version written from it at landing (`Node.Execution`). Change impacts and reviews carry `Execution`
  (ADR 0024); a review entry gets `Execution` and `Flow`.
- `open` records the **stale runs**: the relaunched step's, the steps after it and the sub-agent runs started
  from it (found through the journal, as ADR 0017 §3), in `FlowEvent.StaleRuns` (first `StaleExecutions`; opaque
  producer ids since ADR 0067). The stale change impacts are the ones declared by those runs; the stale drafts' edits
  and reviews are the ones made by them. There is no closure over `derivedFrom`: a later step is stale as a whole.

### 3. What a process on a flow sees
`View(flow)` of the change impacts (`domain.ImpactsSeenBy`, a fold of the log, ADR 0029):
- the main change impacts, minus the stale ones, plus the ones of the flow and of its ancestor flows;
- for each, the draft resolved as in §1, `review` = the last review that is not stale, in effect on the flow chain;
- the other flows, and what the stale runs wrote, are invisible. Blackboard hydration, CEL `changeImpacts` and the
  DSL `changeImpacts()` use this view, so a relaunched step replans from the state before the step.

### 4. Writing on a flow
- The node operations of ADR 0076 take the flow (`ImpactNodeCreate` / `ImpactNodeCheckout` / `ImpactNodeUpdate`, ...,
  first `WriteNode(change, node, flow, w)` on the flow branch): they edit the flow's draft, never the main flow's.
  A node declared by the flow gets its main `post` at adoption.
- A review on a flow (`ImpactNodeReviewOn`) reviews the change impacts the flow declared or the main ones it sees; the
  review is a candidate (`Review.Flow`) until adoption.
- The lifecycle rules (ADR 0014, 0078) apply to the draft the flow sees; a transition is checked, guarded and acted
  when it is taken (ADR 0079; first at `Apply`, on the resulting change branch).

### 5. Adoption is a reset, not a merge
What the stale runs wrote on the main flow must be **replaced**, not merged with what the flow wrote (a 3-way merge
would report it as a conflict with the flow's own rewrite). `AdoptFlow` (`adoptNodes`), for each change impact:
1. When the flow holds a draft of the node, it becomes the main flow's (a `transitioned` event carrying the `Draft` and
   `{"adopted": flow}`). The flow must have accepted it; a node whose main draft changed since the flow checked it out,
   by a run that is not stale, is a conflict (`ErrConflict`).
2. Else, when the main draft was written in part by stale runs, it is reset to what the other runs left, or dropped
   when they left nothing (the node was created by steps that no longer exist).
3. Stale change impacts that the flow did not replace become **superseded** (`ChangeImpact.Superseded`) and lose
   their drafts: they stay in the list, out of `Apply`. The flow's change impacts lose `Flow`, its reviews and
   declarations become the main ones, the stale reviews are marked superseded (one `adopted` event, ADR 0029).
4. Ancestor flows are adopted first (ADR 0017: a branch is adopted when the whole chain is).

The history is kept: the change log holds every edit, the stale ones included.

### 6. Discard and competition
- `DiscardFlow` rejects the flow's change impacts and its descendants' (`discarded` events, comment "flow
  discarded"); what they drafted stays in the log, nothing sees it; nothing on the main flow moved, the stale ones
  count again.
- **Parallel flows** keep ADR 0017's rules. Two open flows *compete* when they replace the same runs or when both
  draft the same node (both derived from what the other replaces): the second cannot be adopted, it is relaunched or
  discarded. Flows that touch different nodes are adopted freely.
- The relaunched **process** is superseded on adoption and untouched on discard, as before.

### 7. Engine
- `applyNodeOps` takes the flow of the process: declarations get `Flow` and `Execution`, writes and reviews go through
  the flow view (§3, §4). The "not available on a flow branch" refusal disappears.
- `Relaunch` passes the stale runs it already computes. `MaterializeFlow` is removed: the flow's drafts **are** the
  preview; the reviewer compares the flows impact by impact (`Graph.DiffFlows`, ADR 0083) before deciding.
- The facts of a flow (decisions, artifacts) keep the item statuses of ADR 0017 (`candidate` / `stale` /
  `superseded`, `StaleClosure`); the change impacts follow this ADR.

### 8. Apply
- `Apply` refuses a change with an open flow (unchanged; the merge's `unsettled` since ADR 0102), ignores superseded
  change impacts, and lands the main flow's accepted drafts as in ADR 0024 / 0079.

## Consequences
- **Storage**: `node_version.execution`; `change_impact.flow`, `change_impact.superseded`; `Review.Flow`,
  `Review.Execution` (inside the JSON); `FlowEvent.StaleRuns`. Both SQL dialects, proto (`ChangeImpact.flow`,
  `.superseded`, `Node.execution`, `Review.flow`, `.execution`, `FlowEvent`), `contracts/gen/`.
- **Code**: `graph/graph/flow.go` (open, adopt as reset, discard, competition), `graph/graph/flownodes.go` (the view,
  `adoptNodes`), `graph/graph/draft.go` (a flow's drafts), `contracts/domain` (view of the change impacts per flow),
  `engine/engine` (`applyNodeOps`, `Relaunch`), the UI flow graph and the change tab (candidates and superseded change
  impacts).
- **Cost**: every write records its execution; a flow read is a fold of the change log (ADR 0029); adoption appends
  one event per affected node.
- **Removed** with the item kinds: `MaterializeFlow`, `MergedOnBranch`, the divergence check on proposals. The
  `candidate` / `stale` / `superseded` statuses stay for the facts of a flow.

## Open questions
1. **Reverting on adoption**: a main draft written only by stale steps is reset or dropped (§5.2). The alternative
   was to leave the stale content and mark it in the history; it would keep stale content in the change. Chosen: reset
   (first a reverting version, now an event).
2. **Sub-agent runs started from a stale step** write with their own executions: they are in the stale set through
   the journal, as for items. A sub-agent that already ran on the *flow* is a sub-flow, not handled here.
3. **Reviews by a human** (an `approval` record, not an action): they carry no execution, so they are never stale.
   A relaunch that invalidates the node they accepted would need the human's review to be repeated; today the
   comment stays and the flow's review adds to it.
4. **Main flow writes during the flow**: other processes of the main flow may write while the flow is open; a write
   of the main flow on a node the flow also drafted is a conflict (§5.1), reported at adoption, not prevented at write
   time.

## Implementation notes
- **Storage**: the uniqueness of a node in a change is per flow among the change impacts that are not superseded
  (partial unique index `change_impact_live`); `Review.Flow` / `Execution` / `Superseded` (in the JSON).
- **View** (`graph/graph/flownodes.go`): the change impacts a flow sees, with the draft and `review` resolved (§3); used
  by `Blackboard`, so CEL, the DSL snapshot and the engine read the flow view. `Graph.Change` returns every stored
  change impact, with `flow` / `superseded`, for audit.
- **Writes**: the node operations and `ProposeImpact` carry `Flow` and `Execution`; `ImpactNodeReviewOn(flow,
  execution, ...)`.
- **Engine**: `Relaunch` sends its stale runs; `applyNodeOps` works on the flow of the process.
- **Not done**: a sub-flow relaunched from a flow that already drafted is covered by the flow chain of the view, but
  has no test of its own; the flow graph of the UI does not draw change impacts.
