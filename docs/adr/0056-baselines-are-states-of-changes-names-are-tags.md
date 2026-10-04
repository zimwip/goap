# ADR 0056 — Baselines are the states changes leave; names are tags

**Status**: accepted, implemented · **Date**: 2026-10 ·
Builds on ADR 0032 (branches as pointers, baselines as deltas), ADR 0049 (mandatory change), ADR 0054 (the guard).
Supersedes the empty initial baseline of ADR 0049 and the naming of baselines of ADR 0032.

## Context

A change is the only way to move the state of the graph (ADR 0049, 0054). Baselines did not follow: the empty initial
baseline of a namespace, its "start" baseline, belonged to no change; `CreateBaseline(nodes)` pinned an arbitrary set of
node versions into a baseline outside any change, and chained an empty one onto a head; a baseline carried its own
free-text name. The first change of a namespace needed a baseline to start from, and the only way to have one was to
write one that no change produced.

## Decision

- **A baseline is the state a change leaves.** Every baseline names the change that produced it (`Baseline.ChangeID`,
  `NOT NULL`, checked by the guard, `guardTx.PutBaseline`). Nothing writes a baseline outside a change: `CreateBaseline`
  (Graph, RPC, web) is gone.
- **The empty state is the empty baseline id.** What precedes the first change of a namespace is the empty state:
  `Change.BaselineID == ""` (`change.baseline_id` is nullable), `Baseline.ParentID == ""` for the first baseline, and
  the head of `main` in a namespace no change landed in is the empty baseline (`BranchHead`: empty id, no nodes,
  `branchHead`). `guardTx.Baseline("")` reads it, nothing stores it. `CreateChange` naming no starting state starts
  from the head of its branch: the state the last change left, or the empty one.
- **Committing and integrating are two steps** (the first collapsed them in `Apply`):
  - `CommitChange` validates the change (reviews, properties, lifecycle and transitions, validators, activity goals:
    every rule of a change) and records the state it leaves on its own branch: a baseline of kind `commit` and one
    `impact.landed` event per applied impact naming it. That depends on the change alone. The change is `committed`
    (the former `merge_pending` is this status: a committed change whose integration waits for a resolution).
  - `IntegrateChange` (`MergeChange` over RPC) integrates a committed change into the branch it was forked from: a
    fast-forward (baseline of kind `fast-forward`, the versions of the branch joined to the target, ADR 0032) or a
    3-way merge (kind `merge`, written by a merge change that records its own `landed` events); `landed` is emitted
    again for the change's impacts on the target; the change is `applied`. Conflicts without a resolution leave it
    `committed`.
  - `Apply` is both, in one transaction. A change is its branch: one that wrote nothing gets its branch when it is
    committed (`ensureOwnBranch`), so a change is never applied straight onto the branch it acts on.
- **The state after a change is derived from what the change did; a stored baseline is a snapshot of it.** Each
  baseline has a `Kind` saying how: `commit` and `merge` from the `landed` events naming the baseline in the log of the
  change that produced it, `fast-forward` from the versions that change joined to the branch (`Tx.BranchJoins`),
  `snapshot` (the bootstrap, test fixtures) not derived and always stored. No extra record is written for it: the
  `landed` events are the impact log of ADR 0029, the joins are what a fast-forward already writes (ADR 0032). The
  replay of every baseline in the test corpus is checked against its stored entries (`checkStates`).
  - Only every `MaterializeEvery` baselines along a chain of derived baselines is **materialised** (its entries stored,
    `Baseline.Gap` 0; default 16, `Graph.MaterializeEvery`, `GOAP_MATERIALIZE_EVERY`, 1 materialises every one); the
    others keep their header (`baseline` row, `Gap` = steps since a snapshot) and their state is the nearest snapshot
    with the steps since applied, cached by baseline (states never change). The graph hides it: `Tx.Baseline`,
    `Baselines`, `NodesIn` and every read through the guard return the nodes whether they are stored or derived.
  - Entries of a materialised baseline are stored as before (a delta over a materialised parent, whole at a checkpoint
    or over a derived parent, ADR 0032); `Graph.Materialize(baseline)` stores one on demand, browsing a derived
    baseline by type and `DeleteChange` (which checks the baselines hold nothing of the change) do it themselves.
  - `Graph.StateAfter(change)` gives the state a change left, derived or stored. A state is identified by its
    baseline: a change leaves two (its commit baseline, then its integration baseline), so a change id alone does not
    name one.
- **A name is a tag.** `domain.Tag` labels the change that produced a state, and the materialised baseline when one is
  kept (`tag` table, both dialects; `Graph.TagChange` / `Tags` / `DeleteTag`, RPC `TagChange` / `ListTags` /
  `DeleteTag`). Tags are **not unique**: a name may label several changes, a change may carry several names; a lookup
  by name returns a list. Only the state of an applied change can be named; the same name is not repeated on one change.
  `Apply(change, name)` / `Commit.BaselineName` tag the change when the name is not the change's own title.
  `Baseline.Name` stays a display label (the change's title).
- Pinning a set of nodes into a baseline no longer exists: a state is what a change left. Test fixtures that arrange
  versions written outside a change's merge use the test helper `pinBaseline` (an applied change of its own).

## Consequences

- An existing database must be recreated (`make devlocal-reset`): the schema file changed in place (ADR 0054
  convention). Rows written earlier with a change-less initial baseline still read, nothing writes one.
- A process started on a namespace with no history now starts from the empty state (the engine's `latestBaseline`
  returns the empty id) instead of being refused; the web's "Start {namespace}" actions are gone, the form creates
  the change directly.
- A replay reads at most `MaterializeEvery - 1` steps per read (their `landed` events or joins), less once cached. A
  baseline written before this ADR has no kind: it is a snapshot, always materialised (the default `gap` is 0).
- The integration baseline of a 3-way merge names the merge change, not the integrated one: landing re-attributes the
  versions the merge wrote to the change they come from (`SetNodeOrigin`), so only the merge change's `landed` events
  tell what it moved.
- Tag names are not unique, so a UI naming "the release" must show every change carrying the name.
