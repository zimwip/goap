# ADR 0146 — The guardian judges commits; the merge and the rebase are mechanical

**Status**: accepted; implemented · **Date**: 2026-10 · Amends ADR 0102 (the guardian at the merge), ADR 0106 (the merge
guard of an integrator), ADR 0066 (`ChangeLifecycles.Merge`); renames "land" to "commit" throughout

## Context

A change is the working directory of its author; a commit puts its work in the graph tree (ADR 0102: versions written
on the branch of the change). The merge then combined that work with what the branch it was forked from holds now,
and it was also where the methodology had its say: `checkMerge` built a blackboard of every impact of the change, its
pre version and its committed version with all their links, then asked `ChangeLifecycles.Merge` (the goal of the
Activity the change is scoped to, else the committable states) and the outer guardians of its chain. The cost of that
blackboard grew with what the change touched and with the links of its nodes, on the one operation that must stay
stable: a merge is the combination of two views of the graph and nothing else.

The vocabulary added to the confusion: a version "landed", a change "landed" on a branch, a state was "landable",
while what happens is a commit.

## Decision

1. **The guardian judges each commit.** `CommitChange` (so `Apply` and the producer path `Graph.Commit`) writes its
   versions, then, in the same transaction (`Graph.inChange`), asks the guardian of the change (`checkCommit`,
   `graph/graph/commitcheck.go`): the say of its own methodology (`ChangeLifecycles.Commit`, the goal of its Activity),
   else the committable states of the domain (ADR 0078), then the outer guardians of its chain (`checkGuardians`,
   ADR 0106). A refusal undoes the commit. A methodology's `guardian.merge` is now `guardian.commit` (proto
   `Guardian.commit`, `Methodology.CommitGuard`), and an exclusive integration requires an attached integrator for each
   commit: in a project where `release-based` is in force, an sdlc change commits once attached to a release that
   integrates (`release-based` 0.1.3).
2. **The merge is mechanical.** `IntegrateChange` checks only what makes the work whole: nothing unsettled, the parent
   holds no uncommitted draft of the nodes it brings, the changes it depends on merged (ADR 0108) and its lifecycle in a
   final state (`checkMerge`, `graph/graph/mergecheck.go`). It asks no methodology and builds no blackboard. A conflict
   stops it before anything is committed (`ErrConflict`), as before.
3. **The rebase is mechanical.** The commit a clean rebase writes for its merges checks nothing of what they mean (no
   lifecycle walk, no validator): each side's work was judged when it was committed.
4. **Links are merged only when both sides changed them.** A version holds its links as a link set (ADR 0141): for a
   node changed on both sides, the rebase reads the sets of the fork's, the change's and the target's versions in one
   batch (`Tx.LinkSetsOf`). A side whose set is the fork's left its links alone: the other side's links stand as they
   are, and the links of the versions not needed are not read. Both changed, the links are merged one by one
   (`domain.MergeDrafts`), a conflict stopping the merge.
5. **"Land" is "commit".** `node_version.committed`, `node_branch.committed`, `change_impact.committed_version`, the
   sequence `commit_seq` (migration 0017, both dialects), the impact event `committed` (`domain.ImpactCommitted`),
   `ChangeImpact.Committed`, `Tx.CommittedVersions` / `BranchCommits` / `NextCommit`, a lifecycle state
   `notCommittable` (`Lifecycle.Committable`). The ADRs before this one keep their words.

## Consequences

- The cost of a merge no longer depends on the links of the nodes a change touched; the cost of the guardian moves to
  the commit, where the author is waiting for a verdict on the work anyway.
- A change can no longer commit work its guardian refuses and fix it before the merge: the commit is the verdict. A
  sub-change's work merged into its parent is judged by the sub-change's guardians; the parent's own guardian judges
  the parent's own commits.
- Databases created before this change must be reset: the stored domain versions keep the YAML key `notLandable`.
