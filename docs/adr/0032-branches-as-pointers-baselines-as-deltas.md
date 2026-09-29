# ADR 0032 — Versions on several branches, baselines stored as deltas

**Status**: accepted, implemented (see §6) · **Date**: 2026-09 · Extends ADR 0003 (version-to-version
links), ADR 0009 (branches, options, merge), ADR 0024 (change impacts), ADR 0029 (event-sourced change impacts).

## Context
A node version carried exactly one branch label (`node_version.branch`), and "the latest version on a branch" was read
from that label. To change what a branch sees, the graph had to write:

- a **fast-forward** (a change landing on a branch that did not move since its fork) relabelled the versions of the
  change branch as versions of the target (`MoveVersion`), rewriting history: the node events already published
  said `change-…`, the index kept that branch facet;
- a **branch merge** wrote a new `merge` version for **every** node the source branch changed, including the nodes the
  target never touched (`fast_forward` candidates) and the nodes only the source created (`added`): pure copies with
  a second version number, their links copied and retargeted.

And every baseline stored its whole `{node → version}` map (`baseline_entry`): with N nodes in a namespace and C
applied changes, N × C rows, although a change touches a handful of nodes (100 000 nodes and 10 000 changes: a
billion rows).

## Decision

### 1. A version is written on one branch and may be part of several
`node_version.branch` is the branch the version was **written on**; it never changes. A version **joins** other
branches (`node_branch(node_id, version, branch)`, `Tx.JoinBranch`) when a merge lands it there as is. The latest
version of a node on a branch is the highest version written there or joined (`LatestOn`, `LatestNodes`, `Node`,
`NodeByKey`). A descendant always has a higher number than its ancestors (versions are numbered per node across
branches), and a version only joins a branch where its ancestor was, so "highest" is "latest".
`Versions` reports the joined branches of each version (`Node.Joined`); `Node.On(branch)` tests the membership.

### 2. A merge without conflict writes no version
- A **fast-forward** of a change branch joins its versions to the target: what the head of the branch changed since
  its fork, and the tombstones of the nodes it retired. No relabelling (`MoveVersion` is gone).
- In a **branch merge** (`MergeBranch`, the merge path of `Apply`), an `added` or `fast_forward` candidate without a
  resolution joins the target as is. Only a node **changed on both sides** gets a merge version (two parents), and
  only it has its links merged and retargeted.
- The merge change still records one change impact per merged node (ADR 0024); for a joined node its `post` and
  `landed` are the source version, whose origin (change, change impact, comment) is unchanged.
- **Links** follow ADR 0003: a joined version keeps its outgoing links. When one of them targets a node that got a
  merge version, the link points to the version the source branch saw, so it is **suspect** in the result. This is
  the model's own impact signal: the target now also carries the changes of the other side, which the source never
  saw. No version is written to hide it.
- `land` recognises what a change landed as its own post version on the target (joined) or a version of the merge
  change, and the `landed` event names the baseline it landed in (`ImpactEvent.Baseline`).

### 3. A baseline is stored as a delta
- `baseline.depth` is the distance to the nearest checkpoint of the parent chain. At depth 0 (a **checkpoint**) the
  entries are the whole content; above 0 they are the entries that differ from the parent (`removed`: the node left
  the baseline, `version` is the one it had).
- A new baseline is a checkpoint when it has no parent or when its parent is `checkpointEvery` (50) deltas away from
  its own checkpoint; otherwise a delta. The store computes the delta from the parent at `PutBaseline`
  (`pkg/graph/baselinedelta.go`); callers keep passing the whole map, nothing else in the graph changes.
- A baseline is read from its checkpoint, then the deltas down to it: one query (recursive CTE over `parent_id` + the
  nearest entry per node), the same for `Baseline` and `NodesIn`, in PostgreSQL and SQLite. The memory store keeps
  whole maps.
- The existing baselines are checkpoints (`depth` defaults to 0): no data migration.

With 100 000 nodes, 10 000 changes of ~20 nodes: ~2 M checkpoint rows + ~200 000 delta rows instead of a billion.
Reading a baseline costs its checkpoint plus at most 49 deltas, about the cost of reading a whole copy.

### 4. The landed events and the deltas
The `landed` events of the change impacts are the meaning of a delta: approved, and this exact version is now on the
target branch. They are not its only source, so the delta is stored with the baseline, as a projection:
- some baselines are not landings of change impacts: seeds and imports (`CreateBaseline`), the result of a change on
  its own branch at `Apply` (the accepted level, §5), a fast-forward carrying what sub-changes merged into the change
  branch (their events name the change branch baselines);
- a merge resolves conflicts: replaying events would mean replaying merges.

The two must agree, and the tests check it after every test (`checkLandings`): every `landed` event names a baseline
that holds the landed version (a tombstone: the node is not in it) and changed it from its parent.

### 5. Three levels of the same change impacts
What a branch sees is its fork plus the change impacts on it, filtered by how far they went:

| level | the change impacts that count | what for |
|---|---|---|
| `written` | every written version not rejected (proposed or accepted) | what the change, a flow or an option would look like: analysis, propagation, the agent's reads |
| `accepted` | the accepted versions only | what would land: comparison, the decision |
| `landed` | what landed | the baselines: deltas, checkpoints, history |

`ChangeView(change, flow, level)` computes the first two from the change impact projection (what a flow sees is its
fold, ADR 0029 §3) over the head of the change branch, without storing anything; `landed` is the result baseline of
the change, or its starting point before it is applied. A rejected or superseded version drops out by itself.

### 6. Options (ADR 0009 §3–5)
Two versions of a node coexist, each on the branch of its option: `REQ-1@v2` on option A, `REQ-1@v3` on option B, both
children of v1.

- An **option is a flow** (ADR 0025) opened as a hypothesis (`OpenOption`: a flow event `open` carrying
  `option: {name, hypothesis}`), forked from the main flow and invalidating nothing. What it declares, writes and
  reviews is a candidate of its flow until it is decided; its versions live on its flow branch.
- **The active pointer**: `activate` / `deactivate` flow events name the option the change works on
  (`Change.ActiveOption`, `ActivateOption`; "" or `main` goes back to the main flow). Every graph call that names no
  flow resolves to it (`Change.ResolveFlow`): declaring, writing and reviewing change impacts, adding items, the
  blackboard, the board check, the views. `main` names the main flow explicitly. So a process running on the main
  flow, the IDE and the `goap-change` tools all work on the active option without knowing it, and switching the
  option (as many times as the process needs) is one event. The graph a process reads (prompt `.Baseline`, the
  script `Host`, `ChangeGraph`) is the active option's view at `written`; the pre version of a new change impact is
  still the released one (the reference baseline).
- **Status**: `exploring` → `evaluated` (`EvaluateOption`, an `evaluate` flow event with its comment) →
  `selected` (the flow adopted) or `rejected` (the flow discarded). A decided option is not active any more.
- **Comparison** (`CompareOptions`, at `written` or `accepted`): the nodes any open option changed from the main
  flow, with the version and the properties on each side (both sides fork from the same graph).
- **Selection** (`SelectOption`) is a decision: the other open options are rejected, then the flow of the selected
  one is adopted. A flow that invalidated nothing (an option) and wrote every node it changes on top of the head of
  the change branch is adopted **without copies**: its versions join the change branch (§1). A flow that relaunched
  steps still writes `adopt` versions (resetting a node to an older version needs a new one). The change is then
  applied as usual; `Apply` refuses a change with an open option.
- **Surfaces**: the graph service (`OpenOption`, `ActivateOption`, `EvaluateOption`, `SelectOption`,
  `RejectOption`, `ListOptions`, `CompareOptions`, `GetChangeView`, `GetChangeGraph`; `ListNodeVersions` reports
  `joined`), the engine port (`ChangeGraph`, `ChangeView`, the options except selecting and rejecting), the
  `goap-change` tools `options`, `option`, `activate`, `evaluate`, `compare` (selecting and rejecting stay a
  decision of the service, not a tool), the Options pane of the change editor and the joined branches in the node
  history.
- The baselines written whole before this ADR are compacted to deltas at start (`CompactBaselines`, in the
  background: a baseline's content never changes, so it can run while the graph is in use).

**Implemented**: all of the above. The decision points that choose among the options, their loops and the CEL
conditions on options are ADR 0009 §4 (implemented: a decided point selects its option, and selecting an option by
hand settles the pending points that chose among the options).

## Consequences
- History is no longer rewritten by a merge; `reason = merge` marks only versions with two parents.
- Fewer versions and links: a merge writes what is new, nothing else.
- `node_version.branch` means "written on"; code that asks "is this version on branch X" uses `Node.On` / `LatestOn`,
  not the label. The index keeps the branch a version was written on as its facet; the `main` facet follows the
  baselines of main, as before.
- A merged link can now be suspect where a copy used to hide it.
- Baseline storage grows with the changes, not with the changes × the size of the graph.
