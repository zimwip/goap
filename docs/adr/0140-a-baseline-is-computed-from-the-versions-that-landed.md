# ADR 0140 — A baseline is computed from the versions that landed in it; a commit is one transaction

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
6. **A commit is one transaction.** `Graph.Commit` is built of the graph's own operations (create the change, the
   drafts, the reviews, commit, merge), each of which opened its own transaction: about 60 for a ten-edit commit, so a
   failure left a change to purge, and each paid a sync to disk. `Graph.inOne` puts the transaction in the context and
   the outer layer of the repository (`ambientRepo`) makes every operation called with that context join it. The checks
   of the guard, the events and the checkpoints run once, at its end. A failure rolls everything back, so nothing is
   purged any more. The roots of a fresh graph are bootstrapped before the commit and never rolled back with it.

## Consequences

- A commit writes its node versions, their joins and one baseline header, and no entry. On the measure above, entries
  went from 250 to 0 per commit and WAL from 176 KB to 117 KB.
- A commit is one transaction: a ten-edit update commit went from about 60 transactions and 435 ms to 1 and 306 ms on
  PostgreSQL, a ten-node creation from 276 ms to 194 ms. What it writes is now mostly the change's own record, its log
  (about 60 entries, 58 KB) and the impact projection. The log stays as it is: a change is the activity of a person or
  an agent, never a high-frequency stream.
- Reading the state of a header costs one indexed query over the landings since its base, at most `CheckpointEvery`
  baselines.
- Schema: migration 0007 (both dialects) adds `baseline.seq`, `baseline.base_id`, `node_version.landed` and
  `node_branch.landed`, plus their indexes. It drops the indexes of 0006 that only the compaction used.
