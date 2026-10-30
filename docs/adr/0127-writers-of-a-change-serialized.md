# ADR 0127 — The writers of a change are serialized

**Status**: accepted, implemented (step 1 of 4) · **Date**: 2026-10 · Builds on ADR 0029 (impact events), ADR 0079
(drafts folded from the log), ADR 0102 (one commit and merge path), ADR 0103 (sub-changes); complements ADR 0124
(engine replicas), whose locks serialize the work on a process, not the writes of the graph

## Context

Several people, the assistant and the agents of the engine write one change at once: they create and edit its nodes,
review its impacts, move it through its lifecycle, commit and merge it. Nothing in the graph put those writes in order:

- the PostgreSQL transactions run in `READ COMMITTED` and took no lock. `Graph.emit` reads the projection of the
  impacts (`change_impact`), applies the event and writes it back: two transactions on one change both read the same
  projection and the second wrote over the first, so the projection was no longer the fold of the log (ADR 0029);
- the draft cache (ADR 0079) assumed it in so many words ("Writers of one change are assumed serialized"): with
  `bigserial` the transaction holding `seq` 11 may commit after the one holding 12, and a reader that cached
  "everything up to 12" never folded 11;
- an operation checks then writes (a second checkout of a node is a conflict, a decided review is not decided again):
  two transactions both passed the check;
- `fastForward` compared the head of the target with the fork of the branch, then `advanceBranch` wrote the head with
  an unconditional upsert: two changes merged at once into `main` from the same head both fast-forwarded, the second
  head replacing the first, whose work was no longer on the chain of baselines of `main`.

SQLite (one connection, `lib/platform`) and the memory store run one transaction at a time: none of this happened
there, which is why the suites never saw it. `TestConcurrentWritersOfAChange` and `TestConcurrentMergesIntoABranch`
reproduce both on PostgreSQL (`make test-pg`).

## Decision

1. **One lock per tree of changes.** `Tx.LockChange(id)` holds, until the transaction ends, the lock of the root
   change of the tree `id` belongs to (a root change and every sub-change under it): the writers of one tree run one
   after the other, changes of other trees stay parallel. A tree rather than a change because a sub-change's merge
   writes its parent and an abandoned parent writes its sub-changes: one lock for the tree has no lock order to get
   wrong. PostgreSQL: a transaction-level advisory lock (`pg_advisory_xact_lock`) on the root found by a recursive
   query, read again once the lock is held (an attach or a detach that committed meanwhile moved the change to another
   tree, whose lock is taken in turn); nothing to release, nothing left behind by a process that dies. SQLite and the
   memory store do nothing.
2. **Taken before the checks, and at every write.** `changeOpen` (the start of the node operations, `workOn`) and
   `Graph.changeTx` (the other operations on a change: header, items, reviews, decisions, flows and options,
   transitions, commit, rebase, merge, attach / detach, move, upgrade, dependencies, tags, purge) take it before they
   read what they check, so it still holds when they write. The guard takes it too at every write naming a change
   (`AppendLog`, `PutChange` (the parent's tree for a new sub-change), `PutChangeImpact`, `DeleteChangeImpact`,
   `DeleteChange`), so a write path that forgets it is still serialized from its first write. Once per transaction
   (`guardTx.locked`).
3. **A branch head moves only from the baseline built on it.** `Tx.LockBranch` locks a branch the same way;
   `advanceBranch` takes it, reads the head and refuses (`ErrConflict`, "merge again") when it is not the parent of the
   new baseline. The merge takes the lock of its target before it reads the head (`merge`, `IntegrateChange`), so a
   merge landing meanwhile is seen and the later one merges after it (a fast-forward becomes the three-way merge of a
   race, as before), the refusal staying a safety net. Lock order: the tree of the change, then the branch.
4. A deadlock or a serialization failure PostgreSQL reports is an `ErrConflict` (`mapErr`): the caller tries again.

## Consequences

- The projection of the impacts stays the fold of the log and the draft cache's prefix is stable on PostgreSQL; two
  checkouts of one node give one draft; concurrent merges into a branch all land.
- The writes of one tree wait for each other (a transaction, a few milliseconds); reads take no lock. A long
  transaction (a large commit or merge) holds back the other writers of its tree for its duration.
- Not solved here, and next (see `notes/multi-utilisateur-meme-change.md` of the project): two people editing the same
  property still have the last write win, silently. Step 2: an optimistic check per field (the client sends the value
  it read, a conflict names the current value and its author) and a review bound to the content it judged (the
  fingerprint of the draft on each entry). Step 3: presence on a node and an optional short claim. Step 4: one
  sub-change per person for parallel work.

## Addendum: the target locked for the writes only

The merge took the lock of its target before the rebase read the head, and held it through the rebase, the checks
and the reads of the fast-forward: about 15 ms of a close with 4 writers (`goap-bench -mode lifecycle`, PostgreSQL), so
the target was locked more than half the time and every close waited about 4 ms for it, while the rebase found nothing
to merge nearly every time (the changes touch other nodes).

The merge now reads first and locks last:

- the rebase reads the head of the target without its lock (`rebaseIn`) and writes on the change's branch only, which
  the lock of its tree holds;
- the fast-forward reads what it writes against the head it read, and moves the fork of the branch there when the
  target moved on other nodes (`checkFastForward`, `finishFastForward`);
- it then locks the target and reads its head again: unchanged, it only writes (`applyFastForward`: the commits taken,
  the header, the head moved, the impacts committed); moved, it is `errTargetMoved`, and the merge rebases onto the new
  head under the lock it now holds, then merges as before (a fast-forward, or the three-way merge of a race).

Then the window between the plan and the lock, and what is left under the lock:

- with 4 writers, 39 % of the merges found the head moved at the lock and did everything again under it. The plan now
  reads the head again before it locks (`freshOnto`, not as the transaction kept the branch) and is read again while
  the head moves, up to `prepareAttempts` (3); a head moved at the lock all the same (the merge that held it landed)
  has the plan read again under the lock, and only a target moved on a node the branch changed is rebased;
- the commit of the impacts on the target read the versions the target holds once it took the commits (two
  statements); the plan knows them (the versions it moves the nodes to, the tombstones) and reads the nodes before the
  lock (`targetHeads`, `fastForwardPlan.heads` given to `commit`).

A plan read again while the head moved first moved the fork at each read, so a merge could record two or three
`rebase` commits on the change's branch in one transaction, each onto a head no one read the branch at. The reads that
decide are now apart from the move (`checkFastForward` writes nothing, `finishFastForward` moves the fork): the plan is
read again until the head stands, then the fork is moved once, onto that head. A head moved at the lock all the same
moves it a second time, under the lock (one merge in five with 4 writers); a merge records at most two.

Under the lock remain the commits taken, the header, the head moved, the `committed` entry, the header of the change
and the COMMIT. A merge that lost the race reads its plan twice; the result is the one of a merge locked from the start.
A caller already holding the lock of the target (`IntegrateChange` of a committed change) sees no move.

Measured with the same bench, 150 s:

| | target held a close | waited for | versions written |
|---|---|---|---|
| locked before the rebase | 15.5 ms | 3.8 ms | 54 872 |
| plan before the lock | 9 ms | 1.4 ms | 59 552 / 59 406 |
| plan read again, target heads read before | 3.7 ms | 0.5 ms | 61 413 / 62 052 |

