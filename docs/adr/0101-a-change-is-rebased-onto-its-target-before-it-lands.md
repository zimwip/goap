# ADR 0101 — A change is rebased onto its target branch before it lands; landing is a fast-forward

**Status**: accepted, implemented; §1 and §3 amended by ADR 0102 (the rebase moved to the merge and applies to what a
change committed: the rebase of the drafts of an uncommitted change and the rebase at commit are gone) · **Date**:
2026-10 · Extends ADR 0082 (rebase of a sub-change; superseded by ADR 0103, a sub-change is now rebased onto its
parent's branch the same way) to a root change; amends ADR 0056 (integration is a fast-forward, the three-way merge of
branches stays only as a safety net); builds on ADR 0079 (drafts, `Draft.Base`) and ADR 0024 (a change's branch).

## Context

Two changes, test1 and test2, start from the baseline B and are applied one after the other. test1 commits on
`change-test1` and fast-forwards main to T1. test2 commits on `change-test2` with the drafts it took at B, finds main at
T1, cannot fast-forward and falls back to a three-way merge of the branch heads: a merge baseline on main, a merge
version for each node both sides changed, a resolution asked at integration time, nothing in test2's own log that says it
moved.

ADR 0082 settled this for a sub-change: the work is brought up to date with what it builds on, under its own review, and
what it lands is a fast-forward. A root change has the same need with its target branch as the parent: test2 is the
work of its owner, which is the one to take test1 in and to settle a contradiction, and main's history should read as the
sequence of the changes, not as a merge for each change applied late.

## Decision

### 1. Rebase

`Graph.RebaseChange(change)` accepts a root change. A root change is **behind** when the head of the branch it was
forked from (`Branch.Parent`) is not its fork baseline. Each node changed on that head since the change took it is merged
three-way, field by field (`domain.MergeDrafts`): **base** the version the change started from, **theirs** the version
on the head of the target, **ours** the change's. Conflicts are named `props.<key>`, `owner`, `state`,
`link:<type>:<target>`, `node` (a creation colliding on a key now taken on the target) and settled in the change (an
edit, `ImpactNodeResolve`, cancelling the draft). The change then moves with its branch: its fork and
`Change.BaselineID` become the head, journaled as one `change.updated` entry of its log (`baselineId` `{from, to}`, as
the project move of ADR 0091), so the audit of test2 shows the rebase and what it did to each impact.

As first decided, the rebase worked on the **drafts of an uncommitted change** (base: the stored version of
`Draft.Base`; the merged draft installed by a `transitioned` event with `Base` moved to the head and `Patch {"rebased",
"conflicts"}`), and a committed change was not rebased. **Since ADR 0102** it is the reverse: a change that committed
nothing has nothing to rebase (`RebaseChange` / `GetRebaseState` answer nothing for it), and what a change committed on
its branch is rebased at its merge (`rebaseCommittedTx`, `graph/graph/rebase_committed.go`: a baseline of kind `rebase` on
its branch, the nodes changed on both sides merged into drafts whose `Base` is the committed version and `MergedWith`
the target's). A sub-change is rebased onto its parent's branch the same way (ADR 0103).

### 2. A clean rebase keeps the reviews

Unlike a sub-change under ADR 0082 (whose parent's values were never reviewed in it), what a root change takes in has
landed through a change of its own. A rebase with **no conflict** keeps the acceptance of an impact whose node it merged;
the landing validates every draft again (ADR 0079), so a merge that breaks a rule is refused there. An impact with
**conflicts** goes back to `proposed` (comment "rebased onto <branch>") and cannot be accepted until settled
(`checkSettled`). Only a contradiction asks for a person: a change whose nodes main did not change is rebased without
any effect on its reviews.

### 3. Rebase first; landing is a fast-forward

The change is rebased before it lands. Conflicts stop the landing with `ErrConflict` naming them. Otherwise, since the
fork is the head, the integration is a fast-forward (`fastForward`): one baseline of kind `fast-forward` on the target,
parent = the previous head, and the versions of the change parented to the versions on main. main's history is linear:
T1, T2. A target that moves between the rebase and the integration makes the fast-forward impossible: the three-way
merge of the branches runs, and, in a run, the `graph.apply` action waits on the merge task (conflicts to settle). It is
the residual case, not the normal path.

As first decided, `Apply` rebased the drafts in a transaction of its own (`rebaseFirst`) then committed and integrated in
one transaction, and `CommitChange` rebased first too. **Since ADR 0102** the commit never rebases; the merge
(`IntegrateChange`, so `Apply`) does, after the change is settled and before its guardian is asked; a clean rebase
commits its merges.

### 4. Rebase and merge are visible in the history

The history tab (`BaselineBranchGraph`, `baselineHistory.ts`) says how each baseline came to be, not only draws the
lanes:

- Every baseline has a **kind** shown on its row as a badge: `commit` (on a change's lane), `fast-forward`, `merge`
  (its second parent drawn as an edge from the lane it joined) and, since ADR 0102, `rebase` and `revert`; the lane
  head keeps its branch label. The web type and the RPC expose `Baseline.kind`.
- A rebase leaves a trace: `Baseline.RebasedFrom`, the baseline the change was forked from before its rebase, and
  `Branch.RebasedFrom` (the first fork, set once). First copied onto the commit of the change, it is since ADR 0102
  carried by the `rebase` baseline (with `MergedFrom` the head it moved onto). The row carries a **"rebased"** badge
  ("rebased onto T1, was B"), and a dashed edge labelled "rebased from" goes to B; the solid edge to T1.
- Edges carry their kind as a label (fork, rebase, merge, fast-forward), as a native tooltip and as a text on the row;
  a merge baseline's badge names the change and the branch it merged.
- A lane is never hidden once its change landed; since ADR 0102 an open branch has its lane before its first commit too.

## Consequences

- test2's audit log tells the sequence: committed, rebased onto main (T1), the conflicts named and settled, merged as a
  fast-forward.
- main's history is linear for changes applied one after the other; a merge baseline means a real race or a branch
  merged by hand (`MergeBranch`, release branches).
- The history graph shows test2's lane with the rebase (and B it came from) and every merge or fast-forward named on the
  row (§4).
- A change that has to wait for a release branch (ADR 0056, two-step apply) rebases onto that branch's head, which is its
  target, not main.

## The `rebase` task of a run

When the `graph.apply` action stops because the rebase left conflicts (`ErrConflict` and `Graph.RebaseState` lists
conflicts; root changes and sub-changes alike), the process does not fail: it waits on a human task of kind `rebase`
(`engine.RebaseTask`: the branch or parent change it was rebased onto and, per impact, its key and the conflict names).
Any other `ErrConflict` (impacts awaiting their review) stays an ordinary error. A person settles the conflicts and
reviews the impacts again in the change tab (the task panel embeds the sync pane), then `Engine.ResolveRebase` (RPC
`ResolveRebase`) continues the run: it refuses while a conflict or an impact awaiting its review remains, and the
planner applies again (which rebases again, and may raise a new task if the target moved meanwhile).

## Implementation notes

- Behind is decided by comparing the head of the target with the fork of the change's branch.
- `Branch.RebasedFrom` is set once, from a non-empty fork.
- Web: the `change.updated` entry carries no branch name, so the audit trail (`auditTrail.ts`) reads "rebased onto
  <branch> head" from the payload when present, else "the target branch"; the sync pane of the change overview
  (`SubChangeSync.svelte`) serves root and sub-changes, and a failed Apply reloads the change so it lists the conflicts.
- Tests: `graph/graph/rebase_root_test.go`, on both dialects and with `GOAP_MATERIALIZE_EVERY` 1, 3 and 100000.
