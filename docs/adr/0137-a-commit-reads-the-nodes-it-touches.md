# ADR 0137 — A commit reads the nodes it touches, never the whole state

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0032 (baselines as deltas with checkpoints), 0056
(a baseline is the state a change leaves; headers computed from the changes), 0102 (commit on the change's branch, merge)

## Context

A baseline is handled as a whole map `{NodeID → Version}` (`domain.Baseline.Nodes`). The commit and merge paths read it
whole: a commit of ten edits read the full state of the namespace about fifteen times (the reference baseline and the
head of the change's branch to build the next state, the fork and the head again to find the withdrawn nodes, the head
for the merge guardian, both heads for the fast-forward, the parent again to store the new entries and to publish the
baseline event, and once per edit to check that its `pre` is in the reference baseline). The depth of the delta chain
(50) and the header gap (`MaterializeEvery`, 16) bound how many baselines a read walks, not how many entries it returns:
a checkpoint is a whole copy, and the state after a header is the whole state of the snapshot before it with the moves
put in. What a commit cost followed the number of nodes of the namespace, not the size of the commit (the load test
measured `CommitEdits` going from 4 s to 21 s while the namespace grew from 1.3k to 8.5k versions).

The changes already are the deltas: a baseline kept as a header is computed from what its change did (ADR 0056). Nothing
new needs storing.

## Decision

1. **Read a state node by node.** `Tx.BaselineVersions(baseline, ids)` returns the versions of those nodes only. A store
   answers for a materialised baseline: the delta chain up to its checkpoint is walked as for the whole state, but only
   the entries of those nodes are read (the primary key of `baseline_entry`). Through the guard, any baseline: the
   nearest materialised baseline before it (or a cached state, or the empty one) for those nodes, then what each change
   in between did to them (`moves`: its `landed` events or the versions it joined; cached, a baseline never changes).
   `Tx.BaselineHeader` reads a baseline without its state.
2. **Write a header from what the change did.** `putBaselineOver(baseline, over, delta)` writes a baseline whose state is
   the state of `over` with `delta` put in. When it is a derived baseline kept as a header (its parent is `over`), only
   the header is written and the baseline event is the delta: no state is read. A baseline to materialise (every
   `MaterializeEvery` along a chain, a rebase, a revert) is written whole, as before: that is the only read of a whole
   state left on the commit path, once every `MaterializeEvery` commits.
3. **Find what a branch changed from the branch.** A change's own branch differs from its fork only on the nodes it holds
   a version of, written on it or joined to it (`Tx.BranchNodeIDs`, indexes on `node_version.branch` and
   `node_branch.branch`). The withdrawn nodes, the rebase and the fast-forward compare the fork and the heads on those
   nodes only.
4. **The operations of a commit build on a `stateView`**: what they read is read by node from the baseline they build on,
   what they change is kept apart as the delta of the next baseline; only a question about every node (a node validator
   listing a type) reads the whole state.
5. `Graph.Commit` (RPC `CommitEdits`) returns the header of the baseline it leaves, without its state; `CommitChange` and
   `Apply` still return it whole.

## Consequences

- A commit costs what it touches plus a walk of at most `MaterializeEvery` headers, and one whole state every
  `MaterializeEvery` commits.
- No new storage: no delta of a delta, the checkpoints and headers are those of ADR 0032 and 0056.
- Still whole: the materialised baselines (rebase, revert, every 16th), the baseline event of a rebase, `Apply`'s answer
  and the three-way merge. A rebase could become a derived baseline too (the head it was rebased onto with the versions
  the change committed); left for later.

## Addendum: a commit reads its nodes in batches

Measured on PostgreSQL (`TestOperationCost`, 20 000 nodes, 50 per change), a commit or a merge was bound by its round
trips, not by the data: one statement per node, several times over (974 statements for a commit of 50 nodes, 645 for
its merge). What a version is never changes once written (ADR 0079, 0141), so:

1. **The versions an operation starts from are read in one batch.** `guardTx.prefetch` (`graph/graph/versioncache.go`)
   reads the versions, their link sets (`Tx.LinkSetsOf`) and their links (`Tx.LinksFrom`) in three statements: the commit
   (the drafts' bases and the impacts' `Pre`), the review batch (`prefetchBases`) and the merge blackboard
   (`draftReader.prefetch`, which also reads the links entering them, `Tx.LinksInto`, and their latest version on main,
   `Tx.LatestVersionsOn`). A version read by a transaction is kept for that transaction (`guardTx.nodes`: its origin may
   still be set, `SetNodeOrigin`); its links and link set are kept by the graph (`versionCache`, bounded by
   `Graph.Caches.Versions`, a `lib/cache`, ADR 0156): only a version the transaction did not write, an empty set only for a version known to
   exist, emptied by a purge (the only delete of versions) before and after its transaction.
2. **The commit reads the latest versions of its nodes in two statements** (`Tx.LatestVersions`, `Tx.LatestVersionsOn`
   on the change's branch): the number of each version, the conflict check and `revise` / `derive` come from them; the
   guard stamps the owner and project from the first parent (kept); the landed versions are checked with the links the
   commit wrote; `land` reads the heads of the target in two statements.
3. **An event folds into the transaction's state** (`emit`): an operation writing N events on a change reads its log
   once, not N times.

A commit of 50 nodes is 277 statements (80 ms), its merge 152 (45 ms), the review batch 161: what remains is per-node
writes (the version, the latest, the link set, the log entry, the projection of the impact).

### The version of a node in a baseline is a selection

Every version is complete (ADR 0079), so the version of a node in a baseline of a branch is the version of that node with
the greatest order of landing up to the baseline's (`node_version.landed`, `node_branch.landed`): a selection, not a
replay. `guardTx.stateAt` answers a read of some nodes in a derived baseline (`domain.DerivedKind`) with one statement
for all of them (`Tx.LandedVersions`, on PostgreSQL `sqlFoldVersions`: one lookup per node of the indexes
`node_version_fold` / `node_branch_fold` on `(node_id, branch, landed)`), down to the last baseline of the branch that
does not follow from the landings (`Tx.LatestUnderived`, index `baseline_underived`: the bootstrap snapshot) else to the
baseline the branch was forked from (`guardTx.foldStop`). Checkpoints and `baseline_entry` are no longer read for some
nodes; they still serve the reads of a whole state (browsing, export, the access snapshots), whose removal is to come.
A transaction keeps what it read of a baseline it did not write (`guardTx.baselineReads`). Baseline differences walk at
most `MaxDiffChanges` (1000) changes between the two baselines. On the lifecycle bench (18 minutes, 3057 changes, 46k
nodes), the merge stays at a p50 of 75 to 80 ms from start to end (it rose from 64 to 171 ms reading checkpoints).
