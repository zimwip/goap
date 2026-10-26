# ADR 0165 — A commit rebases its drafts onto the head

**Status**: accepted, implemented (Go and Rust graph) · **Date**: 2026-10 · Amends ADR 0102 (the change commits on its
branch, rebased at its merge) and ADR 0101; builds on ADR 0133, 0144, 0146.

## Context

Since ADR 0102 a change commits on its own branch from its fork, whatever moved on its target, and is rebased only at
its merge: the versions it committed are merged three-way with the target's, a second version of each node changed on
both sides is written (reason `rebase`, two parents), and the merge fast-forwards over it. A commit therefore writes
versions that are already stale when the target moved, the merge writes them again, and the checks a commit runs
(lifecycle moves, validators, the guardian) judged versions the rebase later changes mechanically. The commit we want is
the sequence of elementary steps a person reads: check, rebase, commit, merge, each its own operation, and the commit
of a change (the composite) the four in a row.

## Decision

1. **The commit rebases the drafts it writes onto the head** of the branch the change merges into (main, a release
   branch, the branch of its parent change), in its transaction, before it writes (`rebaseDraftsTx`,
   `graph/graph/rebase_drafts.go`). The drafts looked at are the accepted modifications the commit writes (all, or the
   nodes named by a partial commit), each with a base: a node the head holds at another version, which is not an
   ancestor of the draft's base, is merged three-way (base: the common ancestor of the two versions; ours: the draft;
   theirs: the head's version, `domain.MergeDrafts`). A draft whose base is a version the change committed is left to
   the rebase of what it committed (at the merge, ADR 0102).
2. **The rebased draft is checked out again from the head's version** (a `checkedOut` event copying that version, the
   impact's pre moved there), then updated with the difference the merge makes (`DraftDiff`, with `rebased` and
   `conflicts`, ADR 0144). The version the commit writes derives from the head's alone (reason `derive`): no version is
   written twice.
3. **A state is merged by its lifecycle**: the replay of the moves validates the transitions. The update never moves
   the state; `transitioned` events do, from the head's state:
   - the change left the state alone, or both sides reached the same one: no move;
   - only the change moved it: the moves it recorded since its checkout are replayed, each allowed by the lifecycle;
   - both moved it: the one move from the head's state to the change's, if the lifecycle allows it.
   Otherwise the state is a conflict (`state`) and stays the head's. The commit then walks the moves recorded since the
   last checkout (`walkTransitions`) as for any draft.
4. **A clean rebase goes on**: graph.merge reviews the merged drafts (one review, ADR 0105) and the commit writes. **A
   conflict stops the commit** with `ErrConflict` naming the nodes and their conflicts; the commit's transaction rolls
   back and the rebase is made again in its own transaction, kept for the person or the agent to settle
   (`ImpactNodeResolve` or an edit, then a review) and commit again.
5. **A change that committed nothing follows the head**: its fork (or its reference baseline, before it has a branch)
   moves to the head with the rebase, journaled as a rebase is (`baselineId`).
6. **The merge follows the lineage**: a node the target moved since the fork is no reason for a three-way merge when
   the branch's version derives from the target's (`overTarget` in `fastForward`, the same skip in `rebaseOnto`): the
   merge is a fast-forward.

## Consequences

- A change committed after its target moved merges as a fast-forward with no `rebase` version; the lineage of a node is
  the chain of the versions the changes wrote, one per commit.
- The rebase of committed work at the merge (ADR 0102) stays for what was committed before the target moved.
- The next steps of the same model: the merge as an elementary operation that leaves the change open, the close of a
  change (nothing uncommitted, merged, closed), and the producer's `CommitEdits` built in memory.
