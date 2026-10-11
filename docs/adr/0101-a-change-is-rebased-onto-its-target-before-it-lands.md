# ADR 0101 — A change is rebased onto its target branch before it lands; landing is a fast-forward

**Status**: accepted, implemented · **Date**: 2026-10 · Extends ADR 0082 (rebase of a sub-change) to a root change; amends ADR 0056
(integration is a fast-forward, the three-way merge of branches stays only as a safety net); builds on ADR 0079 (drafts,
`Draft.Base`) and ADR 0024 (a change's branch).

## Context

Two changes, test1 and test2, start from the baseline B and are applied one after the other. Today test1 commits on
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

`Graph.RebaseChange(change)` accepts a root change (the sub-change rule of ADR 0082 is unchanged). A root change is **behind**
when the head of the branch it was forked from (`Branch.Parent`) is not its fork baseline. For each draft of its main flow
whose node changed on that head since the version it was checked out from (`LatestOn(node, target)` is not `Draft.Base`;
for a creation: the key now exists there), three states are merged field by field (`domain.MergeDrafts`, as ADR 0082 §1):

- **base**: the stored version the draft was checked out from (`storedDraft(Draft.Base)`);
- **theirs**: the stored version on the head of the target branch;
- **ours**: the draft.

The merged draft replaces the draft by a `transitioned` event carrying it, with `Base` set to the version on the head and
`Patch {"rebased": {"branch", "baseline"}, "conflicts": [...]}`; a change impact with no draft yet has its `pre` moved to
the version on the head (the existing `rebased` event). Conflicts are named as in ADR 0082 (`props.<key>`, `owner`,
`state`, `link:<type>:<target>`, `node`) and settled in the change (an edit, `ImpactNodeResolve`, cancelling the draft).

Then the change moves with its branch: `Change.BaselineID` and the fork and head of its own branch become the head
baseline, which is possible because an uncommitted change has no version on its branch (ADR 0079). The move is one
`change.updated` entry of the change's log (`baselineId` `{from, to}`, as the project move of ADR 0091), so the audit of
test2 shows the rebase and what it did to each impact. A committed change is not rebased: its versions are written and
immutable; its integration stays the three-way merge of §3.

### 2. A clean rebase keeps the reviews

Unlike a sub-change (whose parent's values were never reviewed in it), what a root change takes in has landed through a
change of its own. A rebase with **no conflict** keeps the acceptance of an impact whose draft it merged; the landing
validates every draft again (ADR 0079), so a merge that breaks a rule is refused there. An impact with **conflicts** goes
back to `proposed` (comment "rebased onto <branch>") and cannot be accepted until settled (`checkSettled`, extended to a
root change). Only a contradiction asks for a person: a change whose drafts touch nodes main did not change is rebased
without any effect on its reviews.

### 3. Apply rebases first; landing is a fast-forward

`Apply` of a root change rebases it first, in a transaction of its own (as `rebaseFirst` for a sub-change). Conflicts stop
it with `ErrConflict` naming them. Otherwise it commits and integrates in one transaction, and since the fork is the head
the integration is a fast-forward (`fastForward`): one baseline of kind `fast-forward` on the target, parent = the previous
head, and the versions of the change parented to the versions on main. main's history is linear: T1, T2.

`CommitChange` rebases first too, so a branch never holds versions written from a state the target left. A target that
moves between the rebase and the integration makes the fast-forward impossible: the three-way merge of the branches runs
as before, and, in a run, the `graph.apply` action waits on the merge task (conflicts to settle). It is the residual case,
not the normal path.

### 4. Rebase and merge are visible in the history

The history tab (`BaselineBranchGraph`) must say how each baseline came to be, not only draw the lanes:

- Every baseline has a **kind** shown on its row as a badge: `commit` (on a change's lane), `fast-forward`, `merge`
  (its second parent drawn as an edge from the lane it joined), and the lane head keeps its branch label. `Baseline.Kind`
  already exists server-side; the web type and the RPC expose it.
- A rebase leaves a trace on the commit of the change: `Baseline.RebasedFrom`, the baseline the change was forked from
  before its rebase (the first one, if rebased several times), copied at commit from `Branch.RebasedFrom`, which the rebase
  sets once. The row carries a **"rebased"** badge ("rebased onto T1, was B"), and a dashed edge from the commit to B
  labelled "rebased from" lets the eye see both starting points; the solid edge goes to T1.
- Edges carry their kind as a label (fork, rebase, merge, fast-forward), as a native tooltip and as a text on the row;
  a merge baseline's badge names the change and the branch it merged.
- A lane is never hidden once its change landed; an unlanded change shows only in its own branch's history.

## Consequences

- test2's audit log tells the sequence: rebased onto main (T1), the conflicts named and settled, committed, fast-forwarded.
- main's history is linear for changes applied one after the other; a merge baseline means a real race or a branch merged by
  hand (`MergeBranch`, release branches).
- The history graph shows test2's lane forking from T1, with the rebase (and B it came from) and every merge or
  fast-forward named on the row (§4).
- A change that has to wait for a release branch (ADR 0056, two-step apply) rebases onto that branch's head, which is its
  target, not main.
- Not done: rebasing a committed change (it would drop and rewrite its versions); a run that turns the conflicts of a
  rebase into a human task (the `graph.apply` action fails with the `ErrConflict` message, as for a sub-change today).

## Plan

1. `pkg/graph/rebase.go`: `rebaseTx` for a root change (behind rule, base/theirs from stored versions, fork move, log
   entry); `parentOf` split into the sub-change and the root cases; `checkSettled` and `RebaseState` for a root change.
2. `pkg/graph/apply.go`: `rebaseFirst` for a root change; `CommitChange` rebases first.
3. Tests (`pkg/graph`, SQLite and PostgreSQL, `GOAP_MATERIALIZE_EVERY` 1, 3, 100000): the test1 / test2 scenario (linear
   history, versions parented, audit entry), a clean rebase keeping reviews, a conflict stopping `Apply` then settled, a
   creation colliding on a key, a committed change not rebased, the race falling back to the merge, `RebasedFrom` kept on
   the commit (both dialects: `Branch.rebased_from`, `Baseline.rebased_from` in `migrations/` and `migrations_sqlite/`,
   `TestSchemasAligned`; proto `Baseline.kind` / `rebased_from`, `make generate`).
4. Web: `baselineHistory.ts` / `BaselineBranchGraph.svelte` (kind and rebase badges, dashed "rebased from" edge, edge labels,
   tests on the test1 / test2 case); `SubChangeSync.svelte` generalised (a root change behind its target: Rebase, conflicts, "Keep mine"), audit summary
   of the `change.updated` rebase entry (`auditTrail.ts`), tests.
5. `docs/architecture.md` (rebase row, apply row), this ADR to accepted.

## Implementation notes

Deviations from the text above:

- Behind is decided by comparing the nodes of the head baseline (`head.Nodes`) with the version a draft was checked out from, not by `LatestOn`.
- A creation colliding on a key that now exists on the target is flagged as a `node` conflict.
- An impact with no draft yet moves its `pre` through a `Pre` carried by the `transitioned` event, not by a `rebased` event (which would set `Recheck`).
- A committed or closed root change is not rebased.
- `Branch.RebasedFrom` is set once and only from a non-empty fork; a root change whose own branch already holds a baseline is not rebased.
- Web: the `change.updated` entry carries no branch name, so the audit trail reads "rebased onto <branch> head" from the payload when present, else "the target branch"; the sync pane of the change overview (`SubChangeSync.svelte`) serves root and sub-changes, and a failed Apply reloads the change so it lists the conflicts.
