# ADR 0133 — A rebase is a merge seen from the change's branch; every version and baseline exposes its change-stack

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0032 (branches as pointers, versions joining
branches), 0056 (a baseline is the state a change leaves), 0079 (versions written at landing), 0101 / 0102 (rebase before
the merge, commit on the change's branch), 0103 (sub-changes on their own branch)

## Context

The graph has two axes of different natures. The domain is a lattice of immutable states (versions, baselines, branches).
A change is a singular act: it borrows a branch of the domain, moves it by one state, and leaves a baseline. Two things
about the junction of the two axes were not said by the code:

1. **What a rebase writes.** When the merge of a change finds that its target moved, it rebases the change's branch
   onto the target's head (ADR 0102). A node both sides changed gets a version with two parents: the version the change
   committed and the target's version. That version kept the reason `derive` or `revise`, as if it had one parent.
   "Two parents" and "reason `merge`" no longer went together, and a reader of a version's history could not tell a
   rebase from an ordinary successor.
2. **Which changes a state is made of.** A node version is not only a content: it is the application of every change that
   led to it. The records were there: each version names the change that wrote it (`node_version.change_id`), each join
   names the change whose merge made the version join a branch (`node_branch.change_id`), and each baseline names the
   change that left it, its parent and the head it merged (`baseline.change_id`, `parent_id`, `merged_from`). But nothing
   composed them. "Which changes is `REQ-12@v4` made of?" and "which changes explain the gap between these two
   baselines?" had no answer short of walking the records by hand.

## Decision

### 1. A rebase is a merge, seen from the branch that receives it

Merge and rebase are one operation, read from the two sides. A merge takes a branch into its target and is written on the
target. A rebase takes the branch a change was forked from (its parent branch) into the change's own branch and is
written there. So a version a rebase writes is a merge version of the change's branch: reason **`rebase`**
(`domain.ReasonRebase`). Its parents are, in this order, the version the change had committed and the parent branch's
version (`Draft.MergedWith`). `writeVersion` sets it whenever a draft carries a `MergedWith` different from its base; a
draft that is such a merge reads as `rebase` too (`draftReason`).

| Reason | Written on | Parents |
|---|---|---|
| `create` | the change's branch | none |
| `revise` | the change's branch, base already on it | the base |
| `derive` | the change's branch, base from another branch (the common case: a base on main) | the base |
| `merge` | the target of a three-way branch merge (`MergeBranch`) | target's version, source's version |
| `rebase` | the change's branch, by its rebase onto its parent branch | the change's committed version, the parent branch's version |

`adopt` is gone. Options never write versions (ADR 0079): they are flows of the change, and an adopted option installs
its drafts on the main flow.

### 2. The change-stack: a lineage composed from what is recorded

Every node version and every baseline exposes its **change-stack**: the changes it is the application of, oldest first,
with the lineage they are read from. Nothing new is stored. The per-step delta of the stack is already a column of every
version and every baseline (the change that wrote it, the change that made it join); the stack is the composition of
those deltas along the lineage.

- **A node version** (`domain.VersionLineage`): the version and its ancestors through `Parents`, so a DAG with two
  parents at every merge and every rebase. Each step names the change that wrote it and the joins with their changes
  (`Tx.VersionJoins`, from `node_branch`). A version a sub-change wrote names the sub-change. The version joined its
  parent's branch by the sub-change's merge and main by the parent's, so both changes appear.
- **A baseline** (`domain.BaselineLineage`, over `Tx.BaselineHeaders`: the headers of a namespace's baselines, without
  their entries): the baselines it follows from (`parent`, `merged_from`). A baseline that took a change in (kind
  `fast-forward` or `merge`) also brings in the baselines of that change's own branch written before it: its commits,
  its rebases, and the fast-forwards of the sub-changes merged into it, recursively. A root change that merged
  sub-changes and committed nothing itself has no baseline of its own, so its branch is found through
  `Change.Branch`, not through the baselines' change ids.
- **The changes** (`domain.StackChangeIDs`): in the order they first appear. Each carries its parent change
  (`Change.ParentID`), so a reader shows a sub-change under its parent, plus its title and status. A purged change keeps
  its id alone. The graph service blanks the title of another subject's personal change (ADR 0037): what it did is in the
  graph for everyone, what it was called is not.
- **The difference of two baselines** (`domain.DiffStacks`, `Graph.DiffStacks`): the changes one stack holds and the
  other does not. This is a causal explanation of a baseline diff, next to the factual one (the nodes that differ).

`Graph.ChangeStack(ChangeStackQuery{Node | Baseline, Limit})`, RPC `GetChangeStack` (`node_id` + `version`, version 0
meaning the latest on main, or `baseline_id`; `limit`, default 500, keeps the most recent steps and sets `truncated`).
`DiffBaselines` answers `changes_added` / `changes_removed` next to its nodes. Web: `ChangeStackView.svelte` (rows from
`web/src/lib/changeStack.ts`, a row opens its change) in the history of a node (the selected version), the neighbours
panel of the Baseline workspace (the version at its centre) and the comparison of baselines (the baseline's stack, and
the changes that explain the difference).

### 3. Computed when read; materialised only when measured

The stack is computed when it is read, in one transaction. For a version that means two queries: the node's versions and
its joins. For a baseline it means one query over the namespace's baseline headers and an in-memory walk. This is linear
in the number of baselines of the namespace, without their entries. When that cost shows (a long-lived namespace,
frequent navigation), the closure gets materialised the way the entries already are: per baseline, the changes it adds
to its parent's stack, whole at the checkpoints (`checkpointEvery`), and a version's stack composed from its parents'.
This is derived data, computed incrementally and off the write path, invalidated by nothing since baselines and versions
are immutable. Until then no table is added.

## Consequences

- A reader of a node's history sees a rebase for what it is: `rebase`, two parents, the second from the parent branch.
  The equivalence "two parents ⇔ merge or rebase" holds. The change-stack relies on `Parents`, never on the reason alone.
- "Which changes is this state made of" and "which changes explain this difference" are one call each, on any version or
  baseline. The composition of an arbitrary set of changes (a dynamic baseline) and the closure of a stack under
  `Change.DependsOn` (ADR 0108) can build on it. They are not part of this decision.
- `Tx` gains two reads (`BaselineHeaders`, `VersionJoins`) in the three repositories, with their SQL written once in
  `sqlbuild.go`. The schema does not change.
