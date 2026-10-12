# ADR 0102 — Iterate on the branch of a change: commit keeps it open, merge closes it; only modifications are reviewed

**Status**: accepted, implemented · **Date**: 2026-10 · Amends ADR 0056 (two-step apply), ADR 0079 (a draft equal to its
base no longer lands), ADR 0101 (a committed change is rebased at its merge); builds on ADR 0024 (a change's branch).

## Context

Three things went wrong in the use of a change:

1. An impact the person had only identified (a candidate: a node named by the change, never checked out) could be
   accepted, and the change committed with nothing that moved (no post differing from its pre). ADR 0079 called an
   accepted impact without a draft a *confirmation* and landed a draft equal to its base as a version of its own.
2. Once committed, the change was frozen (`committed`): editing an impact was refused with "change is committed". A
   commit is meant to make the work in progress available on the change's branch, not to close it; only closing the
   change should stop its modifications.
3. The change tab offered Apply (commit and merge at once) next to Commit, and the history of a branch showed the
   branches of the changes forked from it only once they had landed: a branch just opened, or committed but not merged,
   did not appear.

## Decision

### 1. Only a modification is reviewed and committed

A change impact is a **modification** when its draft differs from the version it was checked out from
(`domain.DraftChanged`: a property set, changed or unset, the owner, the state, an outgoing link added, removed or with
other properties; the target version of a link is not compared) or creates a node.

- `ImpactNodeReview` (and the review batch) refuses to **accept** an impact that modifies nothing: a candidate (no draft)
  or a draft left as it was checked out (`ErrInvalid`, "modifies nothing"). Rejecting stays possible. The source of a
  merge or a split (`via`, changed by its parent's draft, ADR 0077) is exempt.
- `CommitChange` (and `Apply`) commit only the modifications: candidates and unchanged drafts stay in the change as they
  are (they no longer hold the commit for a review), and a change that names nodes but modifies none is refused
  ("nothing to commit", `ErrInvalid`). A change of items only (facts, decisions) commits as before.
- The bulk producer path (`Graph.Commit`: seeds, `EnsureUser`, the settings dialog through `CommitEdits`) keeps confirming
  what it lists: its context is marked (`withBulk`), it is not a user edit.
- `ImpactValidation.Modified` (proto `ImpactValidation.modified`) tells the web, which disables the acceptance
  (`reviews.invalidReason`) and the Commit button.

### 2. A commit keeps the change open

`CommitChange` of a root change writes the versions of its accepted modifications on its own branch (a `commit`
baseline, `landed` events) and leaves the change **active** with `ResultBaselineID` = its last commit. The change is
edited again: a checkout reads the version the change committed (`LatestOn` its branch; a node it created too), the
review goes back to `proposed`, the next commit's versions descend from the previous ones. The methodology is asked at
the merge, not at the commit: `CommitChange` of a root change checks the drafts (attributes, validators, required links,
transitions) but neither the final state of the change's lifecycle nor the landing gate. A sub-change is unchanged (ADR
0081).

### 3. Merge closes it, rebased first

`IntegrateChange` (RPC `MergeChange`) of an open root change that committed (`mergeRoot`):

- refuses while a modification is not committed ("commit or cancel them first"), and when nothing is committed;
- **rebases** the branch when its target moved since the fork (`rebaseCommittedTx`): a baseline of kind `rebase` on the
  change's branch (the head of the target with the versions the change committed; parent the previous head of the branch,
  `MergedFrom` the head it moved onto, `RebasedFrom` the fork it left), the fork of the branch and the change's reference
  baseline moved to that head (one `change.updated` entry, as ADR 0101). Each node changed on both sides gets a draft
  merging the two three-way (`domain.MergeDrafts`; base the version at the fork, ours the version the change committed,
  theirs the target's), with `Draft.MergedWith` = the target's version, the second parent of the version it lands as.
  Clean merges keep the review and are committed by the merge itself; conflicts send the impact back to `proposed` and
  stop the merge (`ErrConflict`): the person settles them, commits, merges again;
- asks the change's **methodology**: the lifecycle of the change in a final state (`checkFinalState`), the landing gate
  on the blackboard of what the branch holds (`askMergeGate`), else no node in a state that cannot land;
- fast-forwards the target, the change becomes `applied` (closed). A target moving in between still falls back to the
  three-way merge, which leaves the change `committed` (frozen) while it waits for its resolutions, as before.

`Apply` (the engine's `graph.apply`, the bulk path) is unchanged for a change that never committed; for one that did, it
commits what is pending, then merges. `MoveChange` refuses a change that committed on its branch.

### 4. The web

- The change tab has **Commit** (enabled when an accepted modification waits, `changeCommit.ts`) and **Merge** (enabled
  once the change committed and nothing is left uncommitted) in place of Apply; a sub-change keeps "Integrate into parent".
- The history of a branch (`BaselineBranchGraph`, `baselineHistory.ts`) draws a lane for every **open** branch forked from
  it: its commits (the first one forking from the baseline it started on, the next following it, a `rebase` row with an
  edge "rebased onto" its new fork and a dashed one to the old), or, before its first commit, an open head drawn as a
  transparent dot (`LaneEntry.pending`) on the lane leaving its fork point. The view follows the baselines and changes
  stamps.

## Consequences

- An impact identified but not changed is rejected or withdrawn, never accepted; a run's reviewer turns candidates down.
- A change is worked on in several commits; its branch is visible from the branch it was forked from as soon as it
  exists.
- The audit of a change reads: committed, edited, committed again, rebased onto the target (when it moved), merged.

## Not done

- Merging a part of the commits, or keeping the change open after its merge.
- A rebase of a committed change with uncommitted modifications (they are committed or cancelled first).
