# ADR 0177 — A rebase takes commits

**Status**: proposed · **Date**: 2026-10 · **Builds on**: ADR 0101 (rebase), ADR 0103 (sub-changes on their own
branch), ADR 0140 (a fork move commits nothing it need not), ADR 0165 (drafts rebased at commit), ADR 0166 (a change
merged stays open), ADR 0175 (everything is a commit), ADR 0176 (a branch reads commits) · **Supersedes**: the fork
moves of ADR 0101 / 0175 (`moveFork`, the `rebase` commit, `refreshReverts`, `RebasedFrom`)

## Context

A change works on its own branch, forked from the head of its target (main, or its parent's branch). When the target
moves, the change is brought onto its head by *moving its fork*: `Branch.ForkCommit` and `Change.ForkCommit` are
rewritten, a `change.updated` entry records the move and, once the branch committed, a `rebase` commit (a header, no
version) says the branch now stands on the new fork; the reverts of the branch are written again over it
(`refreshReverts`). A fast-forward does it before it locks its target and again, at the lock, when the head moved
meanwhile (`reforkFastForward`).

Two things are wrong with it:

- **It updates.** The rule of the graph is that nothing is updated by default (ADR 0174 addendum): a fact is added,
  never rewritten. The fork of a branch and of a change, its `rebased_from`, are rewritten at every rebase.
- **It writes a commit that changes nothing.** A rebase with no conflict changes no node of the branch: what the target
  committed meanwhile is the target's work, not the change's. A commit of the change should exist only when the change
  has something to write: a conflict to settle.

A branch already reads commits (ADR 0176): it holds rows of `branch_commit`, its own commits and the commits it took
from another branch by a merge, and a node is read as the greatest version of the commits it holds, else at its fork.
What the target committed since the fork is a set of commits, each holding its versions. Bringing the change up to date
is taking them, as a merge takes the commits of its source.

## Decision

1. **A rebase takes the commits of the target.** The branch of the change takes, as `branch_commit` rows, the commits
   its target holds that it does not hold yet: the target's rows past the last of them the branch holds (its position
   on the target, point 3), up to the head of the target, at the order of the rebase (`at`, a new order of
   `commit_seq`, no commit header), recorded by the entry of the change's log naming the target and the head taken
   (`log_seq`). Rows are added, nothing else: the fork of the branch and of the change is written once, when the branch
   is opened, and never moves. There is no `rebase` commit, no fork move, no `rebased_from`, no `refreshReverts`. The
   order of the branch is then the order its commits reached it (F, C1, C2, then M1, M2 taken), as in git when the
   target is merged into a feature branch; a commit of the branch keeps the state it had when written (its history is
   not rewritten), the branch as it stands holds all of them.
2. **A conflict is one more commit.** A node the change changed (point 4) that the commits taken changed too is merged
   three ways: base the node at the branch's position on the target before the rebase, ours the branch's version,
   theirs the target's head. The merged nodes become drafts (`Draft.MergedWith`), and a rebase with no conflict left
   commits them in one commit of the branch (kind `merge`, versions with reason `rebase`, written after the rows taken,
   so the greatest version is the merge's); a conflict stops it, as today, until it is resolved. A revert of the change
   is such a node: its value is the base's, the target's wins, written by the merge commit. No overlap: rows only.
3. **The branch's position on its target** is read, never stored: the greatest order, on the target, of the target's
   rows the branch holds, else the order of the fork on the target. *Up to date* is a probe by key: the branch holds
   the head of the target (`branch_commit_pkey`). The target's own commits (its fast-forwards, its merges, its own
   writes) are rows of the target like any other, so a branch fast-forwarded into its target is behind it by one header
   until its next rebase takes it.
4. **What the change did** is what its branch holds that its target does not: the rows of the branch whose commit the
   target does not hold (`BranchNodeIDs`, the commits read for a fast-forward, `uncommitted`). A sub-change merged into
   the change is the change's (the target does not hold it); a commit the change merged into its target before (ADR
   0166) is no longer pending, it is the target's.
5. **The base of a three-way merge** (the rebase of point 2, `planMerge`) is the selection over the rows both the
   branch and its target hold, else the fork: the target as the branch last took it, including what the change merged
   into it before. One probe per node, `latestHeld` with the target's row asked too.
6. **The branch as it stands** is read with every row it holds (`branchVersions`, the views of a change, the checkouts
   of its drafts, the head kept for a change view): the head commit of a change's branch is no longer its state, the
   selection is bounded by the branch's last order (`LastCommit`) instead of the head commit's. `BranchHead` keeps
   answering the last commit of a branch, its history.
7. **A fast-forward** is allowed when the branch holds the head of its target (point 3) and its target took nothing
   since on a node of point 4. Before the lock of the target it rebases (point 1), at the lock it reads the head
   again: a head moved meanwhile on none of the nodes of the change is taken by the branch (rows), the plan kept; a node
   of the change moved is rebased under the lock (point 2). The target then takes the branch's rows (`takeCommits`,
   the ones it holds already left: `ON CONFLICT DO NOTHING`, the rows of point 4 only read) and writes its
   `fast-forward` header. Under the lock: inserts only.
8. **A change merged and kept open** (ADR 0166) does not move its fork (`forkAfterMerge` is gone): its next rebase
   takes the target's fast-forward header and what came after.
9. **A sub-change** forks from the head of its parent's branch and takes, at its creation, the rows its parent holds
   past that head's order (what the parent took by a rebase since its last commit): its fork is a commit, its state the
   parent's branch as it stands. A parent's later rebase is the parent's: the sub-change's branch is not touched, it is
   rebased onto its parent at its merge, like any change.
10. **An attach / detach** (ADR 0106) opens a new branch of the change (`<change>/<n>`, `Change.Branch` names it) forked
    from the head of the new parent, which takes the change's own commits (point 4 of the old branch), then the
    rebase of point 2 settles the nodes the new parent changed. The old branch stays as it was: nothing is deleted,
    `branch.parent` is no longer updated.
11. **The history and the events stay.** `change.rebased` is raised as today, naming the head taken; the log entry
    recording the rows (`log_seq`) is the record of the rebase (ADR 0174 addendum). The change-stack and the commit
    history show the rows a branch took (`VersionJoins`), so a rebase reads as the commits it took.

## Consequences

- No update and no delete on the rebase path: `branch.fork_commit`, `branch.rebased_from`, `change.fork_commit` and
  `branch.parent` are written once. A rebase with no conflict is one insert of rows and one log entry.
- A branch holds more rows: one per commit of its target since its fork. They are inserted in one statement, and the
  reads that must not count them (points 4, 5) ask the target's row by its key.
- The fast-forward under the lock of main inserts only (the rows taken, the target's rows, its header): the section the
  concurrent merges serialize on.
- `RebaseResult`, `GetRebaseState` and the web (`SubChangeSync.svelte`, `rebase.ts`, `commitHistory.ts`) read the
  position of point 3 instead of the fork; `Commit.Fork` of a change's commits is the fork of its branch, fixed.

## Alternatives set aside

- **A marker column on `branch_commit`** (the rows taken by a rebase flagged): a row is taken, whatever took it; what
  the change did is read from what its target holds (point 4), with no flag to keep true.
- **Taken rows beneath the branch's own commits** (an order before its first commit, rewriting its history): the state
  of a commit would change after it was written, and a sub-change forked from it would see its base move under it.
- **Keeping the fork move, without its updates** (the fork read from the head commit's `Fork`): still a commit per
  rebase, and the fork of a branch that committed nothing still updated.
