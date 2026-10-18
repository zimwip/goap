# ADR 0136 — A commit reads the nodes it touches, never the whole state

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
