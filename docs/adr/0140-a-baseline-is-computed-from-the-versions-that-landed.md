# ADR 0140 — A baseline is computed from the versions that landed in it; guardians judge in the transaction they guard

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0032 (versions and branches), 0056 (a baseline is
the state a change leaves), 0137 (a commit reads the nodes it touches) · Supersedes the delta chains of ADR 0032 §5

## Context

A change touches a few nodes, yet a commit still wrote a few hundred `baseline_entry` rows. Measured on PostgreSQL, with a
namespace of 2,000 nodes and commits of ten edits, each commit wrote 250 entries (37 KB) and 176 KB of WAL. The rows came
from two places: a baseline stored every `MaterializeEvery` commits was a whole copy of the state, and the delta chains
of ADR 0032 were compacted in the background. A node version is immutable once it lands, and the version itself already
records the branch it was written on (`node_version.branch`). The versions a branch joined are recorded too
(`node_branch`). So the state of a baseline is already in the database: it is the versions that landed up to it.

## Decision

1. **Landings are ordered.** A baseline gets an order, `baseline.seq`, allocated by `Tx.NextLanding` (greater than any
   order allocated before). A version written by a landing records the order of the baseline it lands in
   (`node_version.landed`), and so does a join (`node_branch.landed`). The guard stamps both (`landingOn`), and the
   baseline written in the same transaction takes the order (`landed`). The guard refuses a landing that no baseline
   takes up.
2. **A derived baseline is a header.** A commit, a merge or a fast-forward stores only its header, with `base_id`: the
   stored baseline it is computed over. Its state is the state of the base, overlaid with the last version per node that
   landed on its branch with an order in `(base.seq, seq]` (`Tx.LandedVersions`, index `(branch, landed)`). The guard
   computes it (`stateAt`), node by node when it is asked for some nodes (ADR 0137), and caches it.
3. **A stored baseline is an overlay.** A baseline whose state does not follow from its own landings (a snapshot, an
   import, a rebase, a revert) stores only the nodes it changes over `base_id`. A removal is stored with `removed`
   (`Tx.StoredVersions` reads the overlays down to a whole one or a header). A chain of 50 overlays, or a baseline over
   nothing, is stored whole. A rebase or a revert writes exactly the nodes it touched.
4. **Checkpoints are taken in the background.** With `Graph.CheckpointEvery` set (the compositions use
   `DefaultCheckpointEvery`, 50), every 50th header of a run on a branch is queued during its commit. One goroutine
   stores its whole state after the transaction commits (`checkpointer`, `Graph.WaitCheckpoints` for tests). A later
   header is computed over the newest checkpoint of its branch (`Tx.LatestStored`), so a read walks at most 50 landings.
   A checkpoint that fails is only logged, because the state is still computed.
5. `MaterializeEvery` (`GOAP_MATERIALIZE_EVERY`) now defaults to 0, so a commit never stores entries. A positive value
   stores every n-th baseline as an overlay, which the test suites use to exercise both paths (1, 3, 100000). The
   compaction of ADR 0032 (`CompactBaselines`, `baselinedelta.go`) is removed.
6. **The guardians judge in the transaction they guard.** A merge (`IntegrateChange`) settles, rebases, asks its
   guardians (`checkMerge`: dependencies, lifecycle, `ChangeLifecycles.Merge`, the outer guardians) and integrates in
   one transaction; a transition asks its guard and its guardians in the transaction of the move; a sub-change is judged
   by `SubChangeValidator` in the transaction that stores it. What the guardian judged is what lands. `Graph.inChange`
   holds the lock of the tree of the change from the start and puts the transaction in the context; the outer layer of
   the repository (`ambientRepo`) makes every operation called with that context join it, the guardian reading the graph
   included. A rebase that changed the branch is kept and the merge refused (`ErrConflict`), as before.
   `Graph.Commit`, the shortcut of the seeds and the registries, keeps one transaction per operation, as a person or an
   agent takes them.
7. **An operation on a change costs what its event touches, not the size of the change.** The fold of the impact log
   (`draftState`, `graph/graph/draftcache.go`) is indexed (impacts by id, drafts by impact and flow, impacts by node) and
   carries the pending conflicts. A transaction folds its events in place in its own copy of it, made once, and
   publishes it to the cache when it commits, so the next transaction folds nothing. Under the lock of the change it
   asks the log once. The guard reads a change's header alone and takes its impacts from the fold (the projection they
   equal, ADR 0029). `emit` writes the one projection row its event changes. A reader of drafts (`draftReader`) finds
   the drafts it is asked for, and gathers them all only to go over every one.
   An operation that only asks of other changes their status, branch or title reads their head (`Tx.ChangeHead`,
   `Tx.ChangeHeads`: no impacts, no items, no log), never the change whole: the landing looking for the open changes
   holding an impact on a node it lands, the lineage of a baseline (`branchOfChange`, one query for every change),
   the stack of changes, the changes of a node, and the filter of `ListChanges`, which reads whole only the changes it
   keeps. The graph service's `CommitChange` answers the header of its baseline (`Graph.CommitChangeHeader`), not
   its state.

## Consequences

- A commit writes its node versions, their joins and one baseline header, and no entry. On the measure above, entries
  went from 250 to 0 per commit and WAL from 176 KB to 117 KB.
- Each operation of a change of ten modified nodes, on PostgreSQL with 2,000 nodes: `CreateChange` 3 ms,
  `ProposeImpact` / `ImpactNodeCheckout` / `ImpactNodeUpdate` 2 to 3 ms each per node, `ImpactNodeReviewBatch` 34 ms,
  `CommitChange` 27 ms, `IntegrateChange` 63 ms (211 ms and 118 KB of WAL in all). What a change writes is mostly its
  own record, its log and the impact projection; the log stays as it is: a change is the activity of a person or an
  agent, never a high-frequency stream.
- Before point 7, a `Graph.Commit` of N nodes grew with N³: 400 nodes took 3 min 23 s, and 1,000 did not finish in
  10 min. Now the cost per node is the same whatever the size of the commit, about 9 ms (1,000 nodes in 9 s, 43 SQL
  statements per node over its four operations).
- On PostgreSQL with 50,000 nodes and 100,000 versions (698 MB), a cold read of the whole head takes 100 ms, a
  `NodeByKey` 0.4 ms, and the per-operation times stay within 1.5 times those at 2,000 nodes. One checkpoint stores the
  whole state: 0.9 s and 21 MB of WAL at 50,000 nodes, so it grows linearly with the size of the state.
- Reading the state of a header costs one indexed query over the landings since its base, at most `CheckpointEvery`
  baselines.
- Schema: migration 0007 (both dialects) adds `baseline.seq`, `baseline.base_id`, `node_version.landed` and
  `node_branch.landed`, plus their indexes. It drops the indexes of 0006 that only the compaction used.
- `goap-bench -mode lifecycle` (4 writers, 50 nodes per change, PostgreSQL): reading other changes whole made
  MergeChange, ListChanges and DiffBaselines grow with the history (1.26 s, 0.63 s and 0.87 s at 8,000 versions).
  Reading their heads keeps them flat: at 28,000 versions MergeChange is 0.22 s, ListChanges 15 ms and DiffBaselines
  45 ms (p50).
