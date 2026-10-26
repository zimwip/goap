# ADR 0166 — A change stays open until it is closed

**Status**: accepted, implemented (Go and Rust graph, web) · **Date**: 2026-10 · Amends ADR 0102 (the merge settles
and applies the change) and ADR 0108 (a dependency is satisfied once merged); builds on ADR 0140, 0146, 0165.

## Context

Since ADR 0102 the merge of a change was its end: it needed everything settled (`unsettled`: nothing left uncommitted,
rejected or a candidate, no open flow, decision point or sub-change, no pending revert) and the final state of its
lifecycle, then it fast-forwarded the target and applied the change. A partial commit (a node list, the commit dialog)
could therefore not reach main before the rest of the change was done: what was committed waited on the branch of the
change. The elementary actions we want are check, rebase, commit and merge, each its own step, with the end of the work
a step of its own.

## Decision

1. **The merge merges what the change committed and leaves it open** (`IntegrateChange` / RPC `MergeChange`, Rust
   `merge_change`): rebased onto the head of what it was forked from (ADR 0102, 0165), checked for its dependencies
   (`checkMerge`, the lifecycle's final state left to the close), then fast-forwarded. Nothing has to be settled: the
   drafts in progress stay drafts. The change stays `active`, its branch open; the fork of its branch and its reference
   baseline move to what it merged (`forkAfterMerge`, Rust `fork_after_merge`: a `change.updated` entry with a
   `baselineId` `{from, to}` field, as a rebase), so that its next merge compares what it commits next with the target
   as it left it. The merge raises `change.merged`, never `change.applied`. A merge with nothing new to merge is
   refused with `ErrNothingToCommit`; a target moving between the rebase and the fast-forward is a conflict (merge
   again): only the close falls back to the three-way merge.
2. **Each merge records what it moves**: the impacts whose node it moves get their `committed` event on the target
   (`commit(..., only)`, Rust `commit_impacts(..., only)`); a sub-change's work recorded in its parent skips the nodes
   the parent already holds as committed (`recordInParent`, Rust `record_in_parent`).
3. **The close is its own operation** (`Graph.CloseChange`, `//goap:operation change.close doer`, RPC `CloseChange`,
   Rust `close_change`, `graphapi.Port.CloseChange`, `graphclient`): everything settled (`unsettled`), the lifecycle in a
   final state (`checkMerge` with `closing`), what is left merged as above, then the change `applied` and its branch
   `merged`, `change.applied` raised; a change whose work is all merged already only closes (`closeMerged`: applied at
   the baseline of its last merge, no baseline written). A committed change whose three-way merge waits for its
   resolutions (ADR 0102) is integrated and closed with them.
4. **The commit state** (`GetCommitState`) answers `unmerged` (the nodes the branch holds a version of that its target
   does not), `can_merge` (`unmerged > 0`) and `can_close` (settled and something committed). The web shows Commit,
   Merge (or *Merge into parent*) and Close (`changeCommit.ts` `canClose` / `closeWhy`, `graph.closeChange`, assistant
   screen tool `close_change`).
5. **The producers close**: `Graph.Commit` (`apply`, asking `change.close`), `CommitEdits` in Rust, the builtin
   `graph.apply` (`engine.ApplyChange`), the settings dialog (`SaveSettings`), the git import (`land`), the bench, the
   alias proposals of the web and `graphtest` close the change they commit.
6. **A dependency is satisfied once the change it names is closed** into the branch the dependent merges into (ADR
   0108, `dependencyOf`: "not closed yet" otherwise): its work is whole only then.
7. **A change merged more than once lands on its target more than once**: the walk of two lineages
   (`lineageGap`, Rust `lineage_gap`) follows the earlier landings of a change on the same branch
   (`Tx.ChangeBaselineHeaders`), so a later landing does not count the change as new.

## Consequences

- A person commits the nodes that are ready, merges them, and goes on with the rest in the same change; the change
  ends only when it is closed.
- Every caller that meant "commit and finish" now closes; a caller that only merges leaves the change open, which is
  the point.
- Conditions observing `change.status == "applied"` see it at the close, as before at the merge.
