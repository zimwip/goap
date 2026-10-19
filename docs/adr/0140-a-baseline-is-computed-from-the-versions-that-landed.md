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
   its state. The fold also carries the change's items (from its `fact.*` entries), so the guard never reads the
   facts of a change again at each operation.
   A merge locks the branch it lands on before it rebases (`mergeIn`): no other merge lands between the rebase and the
   fast-forward, which would make the merge a three-way one (a merge change, its own impacts and landed events). A
   landing rewrites the origin of a node only when it differs.
   A rebase is a header too when what it puts over the head it brings its branch onto is every last version its
   branch landed (no revert, no stored baseline on the branch): the state of a header whose base is on another branch
   is the state of that base with every landing of its branch (`stateAt`), which for a fork is what it was already.
   The event of a baseline outside main carries no diff (the index follows main), so a rebase reads no state for it.
   `LatestOn` reads the greatest version of the node written on or joined to the branch through their indexes (it
   scanned the joins of the branch).
   Resolving the node an impact names (`resolve`: a creation, a checkout by key), the board's check of the impacts'
   pre versions and a merge or split read the reference baseline for those nodes only (`versionsAt`), never whole.
8. **The indexes follow the reads.** Every read of the log names a change (`change_log_change`, a type or a flow
   filters that range); the engine reads by process and by action run, so those two indexes are partial and leave out
   the graph's entries (migration 0008, both dialects). `change_impact` leaves a third of its pages free (fillfactor
   70, PostgreSQL): its updates at checkout, review and landing change no indexed column and stay heap-only.
9. **The branch of a change is ephemeral** (`Branch.Ephemeral`, column `branch.ephemeral`, migration 0009, set on
   every change branch): it lives until the change lands and its state is its fork with what it landed, so its
   headers are never checkpointed; only the lasting branches (main, the named ones) store states.
10. **An answer follows its scope, not the size of the graph.** `ListBaselineNodes` takes a change
   (`change_id`, `NodeQuery.Change`): the nodes its impacts act on, read node by node at their version in the
   baseline; it and `ListBaselineLinks` answer the header of the baseline, not its nodes. `GetBranch` with
   `header_only` answers the head's header (the web and the bench ask it so; the services that read the head's state
   through `graphclient.BranchHead` still get it whole). `ListBaselines` answers headers only.
11. **A history is read a page at a time, and from where it is asked.** `ListNodeVersions` and `ListNodeChanges` take
   `before` and `limit` (default 50, at most 500) and answer `next`, the cursor of the older page (`Tx.VersionRange`,
   `Tx.NodeChangePage`); the web loads older pages on demand. Landing a version no longer reads the history of its
   node: the next number, its parent and whether it is on the branch come from its last version
   (`Tx.VersionRange(id, 0, 1)`), and landing reads no other open change: each starts from the latest version of its
   nodes when it is rebased at its own merge (ADR 0101), and the board reads a planned impact as outdated when its node
   moved on the branch it lands on since (`behindHead`). One way to diff two baselines (`diffBaselines`, used by
   `DiffBaselines` whatever their distance): the lineage walk names the changes between them (`lineageGap`: both
   lineages painted together, newest first by order, the own branch of a change taken in, stopping where everything left
   is in both), the nodes their impacts and branches touched are gathered, only those nodes are read in each baseline
   (`versionsAt`), and the two sets are compared in memory (created, removed, modified). `DiffStacks` uses the same walk
   instead of reading every header of the namespace, the head of every change and the whole state of
   `from`. Migration 0010 indexes `change_impact (node_id, seq)` and `baseline (namespace, branch, seq)`.

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
- Write rate of the same bench (4 writers, 5,000 versions): concurrent merges raced between the rebase and the
  fast-forward, and a third of them became three-way merges. The log went from 8.7 to 6.8 entries per version written
  (44,900 to 33,900), `node_version` is no longer updated after its insert, and every merge is a fast-forward.
- With point 8 and the header rebase, the same bench: change_log indexes 12 MB to 5.5 MB, change_impact updates 67% to
  91% heap-only, rebase entries 5,137 to 150, WAL 79 to 63 MB, MergeChange 200 to 145 ms (p50); `LatestOn` went from
  0.5 ms (and growing with the joins of main) to out of the top statements.
- Point 11, same bench (4 writers, 20,000 versions, before → after): `DiffBaselines` p50 went from 35 ms growing to
  47 ms (69 ms in the last window) to 14 to 18 ms. Its jumps came from reading both states whole, with the checkpoint
  under them stored every 50 headers, plus every header of the namespace and the head of every change for the stack.
  `MergeChange` went from 134 ms growing to 180 ms to a flat 139 to 147 ms. `ListNodeVersions` and `ListNodeChanges`
  stop growing at 50 entries.
