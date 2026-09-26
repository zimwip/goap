# ADR 0025 — Flow branches on change impacts

**Status**: accepted, implemented (the item forms of flows are gone with the item kinds) · **Date**: 2026-09 · Extends ADR 0017 (flow branches) and ADR 0024 (change impacts). Replaces the
candidate / stale / superseded statuses of items and `MaterializeFlow` once the item kinds are gone.

## Context
ADR 0017 makes a relaunch of a step a **flow branch of the blackboard log**: the items of the relaunched step and of
what followed are *stale*, the replanned run appends *candidate* items, a human *adopts* (stale → superseded,
candidates count) or *discards* (candidates rejected, stale count again). This works because items are inert facts:
nothing is written to the graph before the change is applied, and `MaterializeFlow` replays the proposals on a graph
branch for review.

Change impacts (ADR 0024) write node versions **eagerly** on the change branch. A step that runs again cannot be
"replayed" over versions that already exist, and the graph the flow reads and writes must not leak into the main
flow before adoption. Change impacts have no flow today: a process on a flow branch cannot use them.

## Decision
A flow is a **graph branch forked from the change branch**, and adopting it makes the change branch look like the
flow. Everything a flow writes lives on its branch; the main flow keeps what it has until the adoption.

### 1. What a flow owns
- A **graph branch** `flow-<id8>` (origin `flow:<change>/<flow>`, parent = the branch of the flow it is forked
  from: the change branch, or the branch of a parent flow). It is created **on the first write** of the flow, not on
  `open`; a flow that only reads or declares needs none.
- **Change impacts it declares** (`ChangeImpact.Flow`): candidates until adoption, invisible to the main flow, to
  `Apply` and to the other flows.
- **Reviews it makes** (`Review.Flow`).
- **Versions it writes** on its branch. A change impact has one stored `post` (the main flow's). The post a flow
  sees is **resolved by branch**: the latest version of the node on the flow branch, else on its ancestors' branches,
  minus the stale versions (§2). No per-flow post is stored.

### 2. Provenance, the basis of "stale"
- A node version records the journal execution that wrote it (`Node.Execution`, set by `WriteNode` from the action
  that ran; empty for versions not written by an action). Change impacts and reviews already carry `Execution`
  (ADR 0024); a review entry gets `Execution` and `Flow`.
- `open` records the **stale executions**: the relaunched step's, the steps after it and the sub-agent runs started
  from it (found through the journal, as ADR 0017 §3), in `FlowEvent.StaleExecutions`. The stale change impacts are the
  ones declared by those executions; the stale versions and reviews are the ones written by them. There is no closure
  over `derivedFrom`: a later step is stale as a whole.

### 3. What a process on a flow sees
`View(flow)` of the change impacts:
- the main change impacts, minus the stale ones, plus the ones of the flow and of its ancestor flows;
- for each, `post` resolved as in §1, `review` = the last review that is not stale, in effect on the flow chain;
- the other flows, and the stale versions, are invisible. Blackboard hydration, CEL `changeImpacts` and the DSL
  `changeImpacts()` use this view, so a relaunched step replans from the state before the step.

### 4. Writing on a flow
- `WriteNode(change, node, flow, w)` writes on the flow branch. The base is the latest version on the flow chain, else
  the last non-stale version on the change branch (a *derive*: `parents = [that version]`). It never changes
  `ChangeImpact.Post`, which belongs to the main flow. A node declared by the flow gets its `post` at adoption.
- `ReviewNode` on a flow reviews the change impacts the flow declared or the main ones it sees; the review is a
  candidate (`Review.Flow`) until adoption.
- The lifecycle rules (ADR 0014) apply to the resolved base: editable state, valid transition. The transition,
  guard and action checks stay at `Apply`, on the resulting change branch.

### 5. Adoption is a reset, not a merge
The stale versions of the main flow must be **replaced**, not merged with what the flow wrote (a 3-way merge would
report them as a conflict with the flow's own rewrite). `AdoptFlow` makes the change branch equal to the flow view for
every **affected node** = nodes with a stale version on the change branch ∪ nodes written on the flow branch:
1. `desired` = the flow's latest version, else the last non-stale version on the change branch, else none (created
   by a stale step and not by the flow).
2. When the head of the node on the change branch is not `desired`: write a new version on the change branch that
   copies `desired` (properties, state, outgoing links retargeted to the versions the change produced),
   `reason = adopt`, `parents = [head, desired]`, and the flow's `Execution`. When `desired` is none: a tombstone
   (`Retire`, ADR 0024) — the node was created by steps that no longer exist.
3. Stale change impacts that the flow did not replace become **superseded** (`ChangeImpact.Superseded`): they stay
   in the list, out of `Apply`. The flow's change impacts lose `Flow`, their `post` is set to the version written in
   2, its reviews and declarations become the main ones, the stale reviews are marked superseded.
4. The flow branch is marked merged. Ancestor flows are adopted first (ADR 0017: a branch is adopted when the whole
   chain is).

The history is kept: stale versions stay in the node history on the change branch, followed by the `adopt` version.

### 6. Discard and competition
- `DiscardFlow` abandons the flow branch (and the branches of its child flows), rejects the flow's change impacts
  (`Review = rejected`, comment "flow discarded"); nothing on the change branch moved, the stale ones count again.
- **Parallel flows** keep ADR 0017's rules. Two open flows *compete* when they replace the same executions or when a
  node is written on both flow branches (same node, both derived from a version the other replaces): the second
  cannot be adopted, it is relaunched or discarded. Flows that touch different nodes are adopted freely.
- The relaunched **process** is superseded on adoption and untouched on discard, as today.

### 7. Engine
- `applyNodeOps` takes the flow of the process: declarations get `Flow` and `Execution`, writes and reviews go through
  the flow view (§3, §4). The "not available on a flow branch" refusal disappears.
- `Relaunch` passes the stale executions it already computes. `MaterializeFlow` (the preview of a flow on a graph
  branch) has no more use for change impacts: the flow branch **is** the preview; the reviewer reads it with
  `PlanMerge(flow branch, change branch)` before deciding. It stays for items until they are removed.
- A flow may hold items (ADR 0017) or change impacts, not both, until the item kinds are removed.

### 8. Apply
- `Apply` refuses a change with an open flow (unchanged), ignores superseded change impacts, and merges the change
  branch as in ADR 0024. Flow branches are closed by then (adopted → merged, discarded → abandoned).

## Consequences
- **Storage**: `node_version.execution`; `change_impact.flow`, `change_impact.superseded`; `Review.Flow`,
  `Review.Execution` (inside the JSON); `FlowEvent.StaleExecutions`. Both SQL dialects, proto (`ChangeImpact.flow`,
  `.superseded`, `Node.execution`, `Review.flow`, `.execution`, `FlowEvent`), `gen/`.
- **Code**: `pkg/graph/flow.go` (open, adopt as reset, discard, competition), `pkg/graph/changeimpact.go` (write and
  review on a flow, view), `pkg/domain` (view of the change impacts per flow), `pkg/engine` (`applyNodeOps`,
  `Relaunch`), the UI flow graph and the change tab (candidates and superseded change impacts).
- **Cost**: every write records its execution; a flow read resolves posts through branch lookups (one per change
  node); adoption writes one version per affected node.
- **Migration**: none for data (new columns default empty). Flows opened before keep working on items.
- **Removes** (with the item kinds, later): the `candidate` / `stale` / `superseded` item statuses,
  `MaterializeFlow`, `MergedOnBranch`, the divergence check on proposals.

## Open questions
1. **Reverting on adoption**: nodes written only by stale steps are reverted by a new version (§5.2), which is
   noisy in the node history when the flow does not touch them. The alternative is to leave the stale version and mark
   it in the history; it would keep stale content in the change. Chosen: revert.
2. **Sub-agent runs started from a stale step** write with their own executions: they are in the stale set through
   the journal, as for items. A sub-agent that already ran on the *flow* is a sub-flow, not handled here.
3. **Reviews by a human** (an `approval` record, not an action): they carry no execution, so they are never stale.
   A relaunch that invalidates the node they accepted would need the human's review to be repeated; today the
   comment stays and the flow's review adds to it.
4. **Merge of flow and change branch during the flow**: the change branch is frozen for the nodes of the flow while
   it is open (other processes of the main flow may write elsewhere); a write of the main flow on a node the flow
   also wrote is a competition (§6), reported at adoption, not prevented at write time.

## Implementation notes
- **Storage**: `node_version.execution`; `change_impact.flow` / `superseded`; the uniqueness of a node in a change is
  per flow among the change impacts that are not superseded (partial unique index `change_impact_live`); `Review.Flow` /
  `Execution` / `Superseded` (in the JSON); `FlowEvent.StaleExecutions`. Migrations `0013` (PostgreSQL) / `0012`
  (SQLite).
- **View** (`pkg/graph/flownodes.go`): the change impacts a flow sees, with `post` and `review` resolved (§3); used by
  `Blackboard`, so CEL, the DSL snapshot and the engine read the flow view. `Graph.Change` and `ListChangeImpacts`
  return every stored change impact, with `flow` / `superseded`, for audit.
- **Writes**: `NodeWrite.Flow` / `Execution`, `AddNodes` (a change impact carries its `Flow` and `Execution`),
  `ReviewNodeOn(flow, execution, ...)`. The branch of a flow is `flow-<id8>`, created on its first write, forked
  from the parent flow's branch or the change branch.
- **Adopt** (`adoptNodes`): a version `reason = adopt` per affected node, carrying the `execution` of the version
  it copies (so a later flow that invalidates those runs still finds them); a tombstone for a node only stale runs
  created; a change impact of the flow becomes the change's (`post` set to the adopt version); the stale ones are
  superseded, their reviews too. A node that moved on the change branch by a run that is not stale since the flow
  forked is a conflict (`ErrConflict`, §6, question 4). **Discard** abandons the branches (descendants included) and
  rejects the flow's change impacts.
- **Engine**: `Relaunch` sends its stale executions; `applyNodeOps` works on the flow of the process.
- **Not done**: a sub-flow relaunched from a flow that already wrote on its own branch is covered by the branch
  chain in the view and by the copied `execution`, but has no test of its own; the flow graph of the UI does not
  draw change impacts.
