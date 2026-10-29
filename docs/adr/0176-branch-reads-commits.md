# ADR 0176 — A branch reads commits, not versions

**Status**: accepted · **Date**: 2026-10 · **Builds on**: ADR 0032 (versions and branches), ADR 0137 (the commit path
never reads a state whole), ADR 0140 (reverts), ADR 0143 (the state of a node is a selection), ADR 0166 (a change
merged stays open), ADR 0175 (everything is a commit)

## Context

A version is written on one branch and keeps it (`node_version.branch`, ADR 0032). When a merge brought it to another
branch as is (the fast-forward of a change into the branch it forked from, the one-side nodes of a three-way merge),
the version *joined* that branch: one `node_branch` row per version, with the order of the merge. Every change lands
through a fast-forward, so every version that reached main had its row: `node_branch` grew as `node_version` did.
Measured on PostgreSQL with one change of 10 000 impacts: 10 060 joins, 2 MB (`node_version`: 9.5 MB), 111 ms of the
2 s of `CloseChange`.

Since ADR 0175 every commit is a row with its parents, as in git. A state is the sum of the modifications applied to
it, at the scale of the graph: what a merge brings is a set of commits, each holding the versions it wrote; the history
is read back from the commits, their branches and their parents.

## Decision

1. **A branch holds commits.** `node_branch` is replaced by `branch_commit(branch, seq, at, change_id)`, primary key
   `(branch, seq)`: the branch holds the versions of the commit of order `seq` from the order `at`. Every commit is
   marked on every branch it is part of: on its own branch from its own order (`at` = `seq`, marked when the
   transaction allocates the order, `guardTx.commitOn`, so the versions it writes are on the branch at once), on a
   branch that took it from the order of the merge that took it. The table grows as the number of commits, not as the
   number of nodes they wrote: the change of 10 000 impacts above is a few rows.
2. **A merge takes the commits of its source** (`takeCommits`, `graph/graph/branchcommits.go`): the commits the source
   holds, its own and the ones it took itself (the sub-changes merged into a change, the branches merged into a
   branch), each once (`ON CONFLICT DO NOTHING`: a commit taken before keeps its order). A fast-forward writes nothing
   else; a three-way merge writes its versions (the nodes changed on both sides) at the order it takes the commits.
3. **The version of a node on a branch** up to an order is the greatest version of the commits the branch holds up to
   that order, whichever way it holds them: one query reads any branch (`sqlCommittedVersions`). A fast-forward is over
   the head of its target and a three-way merge writes the nodes both sides changed (a version greater than both), so
   the greatest is the last. For some nodes it is one probe per node (`latestHeld`): its versions committed between
   the first and the last order the branch holds (two reads of `branch_commit_pkey`), from the last backwards
   (`node_version_node_committed`, `(node_id, committed)`), each asked of `branch_commit_pkey` through a fenced
   `EXISTS` (`OFFSET 0`). The bounds matter on a change's branch: holding a few recent commits, it reads the few
   versions committed since its first, where a walk from the last version read the whole history other changes wrote
   (a node changed by 470 changes: 2159 blocks for a branch that does not hold it, 19 with the bounds). The probe
   reads a commit by its key alone, the order it was taken at compared after (a bound on it in the probe let the
   planner walk main by that order, `branch_commit_at`, for each version: 5 500 blocks for 9 nodes, 35 by the key).
   The nodes a branch holds (`BranchNodeIDs`) are the versions of each commit it holds, read by their order
   (`node_version_commit`, an order being one commit's), each commit a fence on PostgreSQL (`LATERAL`, `OFFSET 0`):
   joined, the plan a young table or the weight of main gave read `node_version` whole at each call. So the
   plan never depends on the statistics of `branch_commit` (joined, an old estimate of a branch read every commit of
   the branch for each node, under the lock of the branch being merged). "Latest on a branch" (`LatestOn`,
   `LatestVersionsOn`) is the same probe with no bound.
4. **A revert is a version like any other**: a copy of the version it puts back, a tombstone for a node the fork does
   not hold (a creation reverted). A branch that took its commit reads it as it is. When the fork of the change moves
   (a rebase, the fork after a merge, an attach), its reverts of the nodes the fork moved are written again over the
   new fork (`refreshReverts`, in the commit recording the move), so a revert stays the copy of what the branch stands
   on; a fast-forward moves the target to them (same content, a new version). A branch reading its own revert still
   reads its fork's version (`CommittedVersion.Revert`): its work on that node is none. A three-way merge keeps what
   the target holds of a node a source reverted when the revert is no copy of it.
5. **A three-way merge keeps by writing.** A node the merge leaves as the target holds it (a resolution `skip`, a node
   deleted on the target, a stale revert) gets a version of the merge copying the target's (or a tombstone), written at
   the order it takes the commits, so the commits taken never override it.
6. **The readers follow**: the selection (`Tx.CommittedVersions` / `BranchCommits`), `LatestOn`, `CommittedAtKey`,
   `BranchNodeIDs`, `LastCommit` (the greatest `at`),
   `VersionJoins` / `Node.Joined` (the branches that took the commit of a version: a version superseded on its own
   branch is in the history of the branches that took that commit too), the purge (`branch_commit` rows of the change),
   the kept heads (`took` for the nodes a merge moves), the statistics (`joins` counts the commits taken from another
   branch) and the memory store. `node_version.branch` stays what it was: the branch a version was written on, for its
   history and the revert rule of point 4, never how a branch is read.

## Alternatives set aside

- **One row per version joined** (`node_branch`, the state before): its size follows the nodes, not the work merged.
- **Segments** (a reader holding the versions of a source branch in a range of orders): fewer rows still, but a range
  must be cut against the ranges already read and a revert decides per segment; the commit set is simpler and says what
  happened.
- **A tree per version** (git without content addressing: every commit pointing at all its nodes): a commit would
  point at every node at each version, the cost ADR 0140 / 0143 removed. Pointing at commits is the compromise: a state
  is the sum of the modifications applied, the history is rebuilt from the commits with their branches and parents.

## Next

- The fork of a change stays as it is: a change works in the virtual area of its log until it commits, a fork of a
  change opens a new path of its log. The commits are how changes show each other their work, through a merge.
- A notice on the impacts whose node moved since their baseline (the version they started from) would let a person
  start a rebase from the change.

## Measures

One change of 10 000 impacts (`goap-bench -mode lifecycle`, PostgreSQL):

| | before (`node_branch`) | taken commits only | every commit marked |
|---|---|---|---|
| rows / size | 10 060 / 2 MB | 8 / 80 kB | 25 / 80 kB |
| CommitChange | 2.18 s | 2.07 s | 1.65 s |
| CloseChange | 2.00 s (111 ms of join inserts) | 1.96 s (one insert) | 1.99 s |
| selection fold | 755 ms, 26 calls (both probes) | 507 ms own + 215 ms taken, 27 calls | one probe |

Many small changes (`goap-bench`, the defaults: 4 writers, 50 edits a change, one `CommitEdits` each, PostgreSQL, one
machine): 1 417 changes in 90 s before, 838 with the taken commits read apart (the join of the probe planned against
old statistics of `branch_commit` read every commit of main for each node, under the lock of main: 13.5 ms held on
average instead of 3.4 ms), 1 437 with every commit marked and the fenced probe; over 5 minutes, 4 138 before and
4 046 after (the selection 0.11 ms a call, 0.13 ms before).

Many small changes over the change API (`goap-bench -mode lifecycle -edits 10`, 4 writers, 10 minutes, PostgreSQL, one
machine of 4 cores): 251 406 versions before, drifting +2 % per doubling; 243 572 with every commit marked, drifting
-11 % (CommitChange 23 ms at the end against 19, ImpactNodeCheckout 16 ms against 12: the latest version on the branch
of a change walked the history of the node, 1 ms a call); 238 717 with the bounds, the probe by key and the fenced
node list, drifting +1 % (CommitChange 18.7 ms, ImpactNodeCheckout 11.4 ms; one writer: 23 003 versions in 150 s
against 23 603 before, CloseChange 8.2 ms against 8.9). The runs of one build vary by about 5 %.

The git import of the first 200 commits of this repository (`TestImportRepository`, SQLite, two runs each): 33.8 s and
32.2 s before, 30.2 s and 29.7 s after. The versions of the commits taken are read for some nodes one probe per node,
from its last version backwards (`node_version_pkey`, `branch_commit_pkey`), whatever the number of commits taken.

## Verification

Every test of `graph/graph` (memory, SQLite, SQLite readers, PostgreSQL, PostgreSQL readers) dumped the state of every
commit of every namespace, by key, version and content, on main and on this change: 979 of 989 runs are identical; the
others differ only by the order of concurrent merges or of a map, and by the new test `TestRevertOverAMovedFork`, where
main holds the same content through a revert written again over the moved fork (a new version) instead of a three-way
merge commit. Marking every commit on its own branch too changes none of them: 983 of 989 runs are identical to the
reading of own and taken commits apart, the others differing by the order of concurrent merges or of a map.
