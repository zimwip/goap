# ADR 0102 — One commit and merge path: a change iterates on its branch, only modifications are reviewed, what holds a merge is a plugin

**Status**: accepted, implemented · **Date**: 2026-10 · Amends ADR 0056 (two-step apply), ADR 0066 (the landing gate
becomes a merge guard), ADR 0076 / 0079 (reviews, withdrawal, a draft equal to its base no longer lands), ADR 0081
(a sub-change does not commit), ADR 0101 (a committed branch is rebased at its merge).

## Context

1. An impact the person had only identified (a candidate, never checked out) could be accepted and the change committed
   with nothing that moved. ADR 0079 called it a *confirmation*; the bulk producer path (`Graph.Commit`) relied on it.
2. A committed change was frozen (`committed`): editing it was refused. A commit is meant to make the work available on
   the change's branch; only closing the change should stop it.
3. The path to the graph had special cases: `Apply` committed and integrated in one transaction with the methodology's
   gate asked at the commit, `CommitChange` asked it too, a sub-change committed (writing nothing) then integrated, the bulk
   path and the test helpers accepted what modified nothing, the merge sources and the items-derived impacts were exempt,
   a withdrawal was refused once a version existed, a rejected impact could not be edited.
4. The web offered Apply next to Commit, and the history of a branch showed the branches forked from it only once they had
   landed.

## Decision

### What is stable, what moves

The graph keeps the **mechanism**, identical for every change and every caller: drafts, the review of a modification,
the commit that writes versions on the change's branch, the rebase, the fast-forward. What **moves** with the use cases is
plugged: who may review (`ReviewPolicy`, ADR 0075) and **what may hold a merge** (`MergeGuards`, below). The web never
recomputes a rule of the graph: it reads `GetCommitState`.

### 1. Impacts: modification, review, rework, withdrawal

- A change impact is a **modification** when its draft differs from the version it was checked out from
  (`domain.DraftChanged`: a property, the owner, the state, an outgoing link by type and target node; target versions are
  not compared), creates a node, or merges another version in (`Draft.MergedWith`, §3).
- **Accept** requires a modification, whoever accepts (`checkModifies`): a candidate or a draft left as it was checked
  out is refused (`ErrInvalid`). There is no exemption: not for the bulk path, not for the source of a merge or a split
  (its impact names the parent's, `via`, which decides it: the origins gate of a successor reads the parent's review).
- **Reject** means "not accepted as it is": the impact is reworked, or withdrawn. An edit of its draft (properties, owner,
  links) or a cancel sends a decided review (accepted or rejected) back to proposed (`editedAgain`). A checkout modifies
  nothing and leaves the review; a lifecycle move is judged by its transition (authorization, guard) when it is taken and
  leaves the review as well (a guard may read it, ADR 0076).
- **Withdraw** is possible at any time while the change is open, whatever the review and whether committed
  (`WithdrawImpact`); the impacts that come with it (`via`) leave with it. A node the change committed is reverted on its
  branch by the next commit.

### 2. Commit: write what is accepted, keep the change open

`CommitChange` (root change) writes, on the change's own branch, every **accepted modification** (and nothing else: the
other impacts stay as they are), and **reverts** the nodes its branch changed that no impact holds any more (the version
of the fork, a commit baseline of kind `revert`, stored whole). It checks the drafts as versions (attributes, validators,
required links, the moves of their lifecycle) and **nothing else**: neither the methodology nor the target branch. A
commit that writes and reverts nothing is refused (`ErrNothingToCommit`, an `ErrInvalid`; carried over RPC by a reason
header, `rpcerr`). The change stays `active` (`ResultBaselineID` = its last commit); it is checked out (from the version
it committed), edited and committed again. A key taken meanwhile by another change is a conflict at the commit.

A sub-change has no branch of its own (ADR 0081): it does not commit (`CommitChange` refuses it).

### 3. Merge: settled, up to date, guarded, fast-forward

`IntegrateChange` (RPC `MergeChange`) of an open change, root or sub (`merge`):

1. **settled** (`unsettled`): no open flow, decision point or sub-change; every impact committed (a sub-change: accepted),
   none rejected (rework or withdraw), no candidate (modify or withdraw), no withdrawal waiting for its commit; something
   to merge (`ErrNothingToCommit` otherwise);
2. **up to date**: a root change whose target moved since its fork is **rebased** (`rebaseCommittedTx`): a baseline of
   kind `rebase` on its branch (the target's head with what the change committed; parent the previous head, `MergedFrom`
   the head it moved onto, `RebasedFrom` the fork it left), the fork and the reference baseline moved there (a
   `change.updated` entry), each node changed on both sides merged three-way into a draft (`domain.MergeDrafts`; `Base`
   the committed version, `MergedWith` the target's, the second parent of the version it lands as), the impacts that
   committed nothing moved to the target's version. A rebase with no conflict keeps the reviews and **commits** its
   merges; conflicts send their impacts back to proposed and stop the merge (`ErrConflict`): settled, accepted,
   committed, merged again. A sub-change is rebased onto its parent (ADR 0082);
3. **guarded**: the merge guards are asked (§4);
4. **integrated**: a fast-forward of the target (a sub-change: its drafts installed in its parent, ADR 0081); the change
   is `applied`, closed. Only a target moving in between falls back to the three-way merge, which leaves the change
   `committed` while it waits for its resolutions — the only meaning of `committed` now.

`Apply` (the engine's `graph.apply`, the bulk path) is both: it commits what is pending, then merges. `Graph.Commit`
follows the same rules: its state edits are moves, each impact that modifies its node is accepted, one that restates what
the node holds is withdrawn, and a commit with nothing left is `ErrNothingToCommit` and leaves no change behind (purged);
the syncing callers (seeds, the registry's store) take that as "the graph holds it already". `MoveChange` refuses a change
that committed.

### 4. Merge guards (plugins)

`Graph.MergeGuards` is an ordered chain of `MergeGuard` (`CheckMerge(ctx, MergeCheck) (decided bool, err error)`), asked
outside any transaction on a `MergeCheck` (the change, its lifecycle, the blackboard of what it lands: pre versions and the
version committed on the branch, a sub-change's drafts, and those landed nodes). The first that decides, decides: an error
refuses, `decided` allows and ends the chain, otherwise the next is asked. Built-ins (`pkg/graph/mergeguard.go`):
`FinalStateGuard` (the lifecycle of the change in a final state, ADR 0058), `LandableStatesGuard` (no node left in a state
that cannot land, ADR 0078), and `LandingGate(fn)`, the adapter of a gate deciding on the blackboard (ADR 0066: the goal of
the Activity, `registrysvc.Service.LandingGate`), which, when it decides, replaces the guards after it. Unset:
`DefaultMergeGuards()` (final state, landable states). `goap-dev` plugs final state, the activity gate, landable states;
`Graph.LandingGate` and `checkFinalState` are gone.

### 5. The web

- `GetCommitState` (`Graph.CommitState`: what the next commit writes and reverts, what holds the merge, `canCommit`,
  `canMerge`) drives the **Commit** and **Merge** buttons of the change tab (`changeCommit.ts` only words it); a sub-change
  has "Integrate into parent", from the same state. Accept is disabled for an impact that modifies nothing
  (`ImpactValidation.modified`, `reviews.invalidReason`). "Remove from change" works at any time.
- The settings dialog (`pending.svelte.ts`) withdraws the staged edits put back as they were before it saves, and drops
  its change when nothing is left.
- The history of a branch (`baselineHistory.ts`, `BaselineBranchGraph`) draws a lane for every open branch forked from
  it: its commits (the first forking from the baseline it started on, the next following it, a `rebase` row with an edge
  "rebased onto" and a dashed one to the old fork), or, before its first commit, an open head drawn as a transparent dot
  (`LaneEntry.pending`).

## Consequences

- A candidate is modified or withdrawn, never accepted; a run's reviewer withdraws the candidates it did not modify
  (DSL `remove`).
- A change is worked on in several commits; its branch is visible from the branch it was forked from as soon as it exists.
- A change of items only (facts, decisions) has nothing to commit and is not applied.
- What a merge needs from the methodology is a list of guards a composition plugs.

## Not done

- Merging part of the commits, or keeping the change open after its merge.
- Rebasing a committed branch while it holds uncommitted modifications (commit or cancel them first).
- A node created then withdrawn after its commit keeps its key reserved (its version exists on the change's branch).
- The legacy `ChangeImpact.Items` bridge (ADR 0024) is still filtered out where it was; nothing sets it any more.
