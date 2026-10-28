# ADR 0176 — A branch reads commits, not versions

**Status**: proposed · **Date**: 2026-10 · **Builds on**: ADR 0032 (versions and branches), ADR 0137 (the commit path
never reads a state whole), ADR 0143 (the state of a node is a selection), ADR 0166 (a change merged stays open),
ADR 0174 (impact written at commit), ADR 0175 (everything is a commit)

## Context

A version is written on one branch and keeps it (`node_version.branch`, ADR 0032). When a merge brings it to another
branch as is (the fast-forward of a change into the branch it forked from, the one-side nodes of a three-way merge),
the version *joins* that branch: one `node_branch` row per version, with the order of the merge (`committed`). The
selection of ADR 0143 then reads, for each node, its last version committed on the branch, written there or joined:
two index probes (`node_version_fold`, `node_branch_fold`) and a `UNION` in every reader of a branch (`latestOn`,
`sqlCommittedVersions`, `sqlFoldVersions`, `CommittedAtKey`, `sqlBranchNodeIDs`, `sqlLastCommit`).

Every change lands through a fast-forward, so every version that reaches main has its join row: `node_branch` grows as
`node_version` does. Measured on PostgreSQL with one change of 10 000 impacts: 10 060 joins, 2 MB (`node_version`:
9.5 MB), 111 ms of the 2 s of `CloseChange`.

Since ADR 0175 every commit is a row with its parents, as in git. In git a branch is a pointer to a commit and a commit
is *on* a branch when the branch reaches it through the parents; nothing is written per file. A fast-forward moves the
pointer. GOAP keeps no tree per commit (ADR 0140, 0143: the state is selected at read), so reachability cannot stay
implicit, but it need not be written per version either: what a merge brings is a range of the commits of one branch.

## Decision (proposed)

1. **A branch reads segments.** `node_branch` becomes the reading header of a branch: one row per (reader branch,
   source branch, range of orders), `(branch, source, after, up_to, at, change_id)`: the reader holds the versions
   committed on `source` with an order in `(after, up_to]`, as of its own order `at` (the order of the merge). A
   branch reads itself implicitly (its own versions, `node_version.branch`), and its fork as today (`Commit.Fork`).
2. **A fast-forward writes one segment.** Merging the branch of a change (`changebranch.go`, `merge`) writes
   `(target, change branch, last range merged, head of the change branch, merge order)`, then copies the segments the
   change branch reads (its sub-changes merged into it), each cut to what the target does not read yet: for a source
   the target already reads up to `u`, only `(u, up_to]` is added, and a segment whose source is the target itself is
   dropped. The ranges a reader holds of one source are disjoint and grow with `at`, so an older version can never be
   ranked above a newer one by a later copy.
3. **A three-way merge writes one segment too.** The source's commits since the last range merged, as of the merge;
   a node changed on both sides gets its merge version on the target at the merge's order, which wins a tie with the
   segment (own versions first).
4. **The selection reads segments.** The version of node X on branch B up to order S is the greatest, by order, of:
   its last own version on B with `committed <= S`, and for each segment of B with `at <= S` the last version of X on
   `source` in `(after, up_to]`, ranked at `at`; a segment whose last version of X is a revert of a change branch
   contributes nothing (the fork decides, as today). A fast-forward is over a rebased branch and a three-way merge
   writes the both-sides nodes, so for one node the order of the commits and the order of the merges agree.
5. **The readers follow.** `LatestOn`, `CommittedVersions` / the fold, `CommittedAtKey`, `BranchNodeIDs`,
   `LastCommit`, `VersionJoins` (the change-stack: the readers whose segments hold the version), `Node.On`, the commit
   delta (a segment with `at` in the range brings every node of its range), the purge (`deleteChangeUsed`), the kept
   heads (`headMove` for the nodes of the segment, already listed by the merge), the memory store.
6. **No per-version join.** `Tx.JoinBranch` goes; `Tx.ReadBranch(reader, source, after, upTo, change)` replaces it.

## Risks to settle in the prototype

- **Heavily versioned nodes.** The repository node of a git import has one version per commit, each landed by its own
  segment. The fold of one node must stay one index step: the candidate versions of X are read by
  `(node_id, branch, committed)` per source the reader holds; a reader holding thousands of segments of thousands of
  sources must not probe each. The prototype measures `goap-gitimport` on a real repository against main.
- **Reverts.** The last version of a segment, not of the node, decides whether the segment contributes.
- **Deltas.** `CommitDelta` between two commits of main lists the nodes of the segments merged in between: one read
  of the source's versions in range per segment.

## Consequences

- A merge writes a row per source branch instead of a row per version: 10 000 rows become one for the bench above;
  the `UNION` of every branch reader becomes a join with the reading header.
- The change-stack and `Node.On` answer from the ranges, not from a row per version.
- No compatibility layer: the database starts from zero (the rule of the protos); the migration drops `node_branch`'s
  per-version rows and creates the segment table in both dialects.

## Measures (main, PostgreSQL, one change of 10 000 impacts)

| | |
|---|---|
| `node_branch` rows / size | 10 060 / 2 MB (`node_version`: 9.5 MB) |
| join inserts at the merge | 111 ms (CloseChange ≈ 2.0 s) |
| branch node list (`UNION` with `node_branch`) | 40 ms, 3 calls |
| selection fold (both probes) | 755 ms, 26 calls |
