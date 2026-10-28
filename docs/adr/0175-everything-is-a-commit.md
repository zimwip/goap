# ADR 0175 — Everything is a commit

**Status**: accepted, not implemented · **Date**: 2026-10 · **Builds on**: ADR 0056 (baselines are states of changes),
ADR 0101 (rebase), ADR 0102 (commit and merge), ADR 0103 (sub-changes on their own branch), ADR 0140 (a baseline is
computed from the versions that landed), ADR 0143 (the state of a node is a selection), ADR 0174 (impact written at
commit) · **Supersedes**: ADR 0140 §9 (a change branch holds no baseline), the name *baseline* everywhere

## Context

A baseline began as a stored state: the map of every node to its version, then checkpoints and deltas. ADR 0140 and
ADR 0143 took the state out of it. What is left in the `baseline` table is a header: an id, the change that wrote it,
the branch and namespace, its parent, a second parent for a merge (`merged_from`), the fork before a rebase
(`rebased_from`), a kind and an order (`seq`). The state is selected at read: each node's last version committed on the
branch up to that order, past the fork on the branch it was forked from. That is a commit, in the sense every
developer knows from git, and the word *baseline* now says something the table no longer is.

The rename alone would not be enough, because *commit* already names something else here. `CommitChange` writes the
versions of a change on the change's branch, takes an order from `commit_seq`, stamps it on the versions
(`node_version.committed`), and writes **no** row: a change branch holds no baseline (ADR 0140 §9). Its state is
answered with no id (`Graph.BranchHead`), every other reader of a head refuses it (`errEphemeral`), and a sub-change
forked from it records an order instead of a fork (`Branch.ForkSeq`), walked by a second reading path
(`chainVersions`, `branchstate.go`). Renaming without changing that would give commits that are not commits.

ADR 0140 §9 kept the change branch without a row so a commit cost only its own versions, at a time when a baseline
header was read and written with checkpoints around it. A header is one row now; the reason is gone.

## Decision

1. **A commit is the one record of a state.** The table `baseline` becomes `commit`, `domain.Baseline` becomes
   `domain.Commit`, `BaselineID` becomes `CommitID`. Its columns keep their meaning: `parent_id`, `merged_from` (the
   second parent), `rebased_from`, `change_id`, `namespace`, `branch`, `kind`, `seq`, `created_at`, plus ADR 0174's
   `log_seq` (the position of the change's log the commit was written at). `name` stays a label; a name a person gives
   to a state is a tag (ADR 0056, ADR 0143 §6).
2. **Every commit is a row, on any branch.** `CommitChange` writes a commit on the change's branch, its parent the
   previous commit of that branch, else the commit the branch was forked from; its `seq` is the order it already takes
   from `commit_seq`. A rebase, a revert and the merge of a sub-change into its parent's branch write theirs too. A
   change branch is a branch like any other: `Graph.BranchHead` answers its head commit, `errEphemeral` goes.
   `Branch.Ephemeral` stays as what it says (the branch lives as long as its change), it no longer changes how its
   state is read.
3. **One way to read a state.** The state of a commit is the selection of ADR 0143 for every branch: each node's last
   commit on the commit's branch up to its `seq`, else, past the branch's fork, on the branch it was forked from. A
   sub-change forks from a commit of its parent's branch (`Branch.ForkCommit`, the parent's head commit, else the
   parent's fork), so `Branch.ForkSeq` and the second path (`branchVersions` / `forkVersions` / `chainVersions`) go;
   `Tx.BranchCommits` becomes a case of `Tx.CommittedVersions`.
4. **Names.** Everywhere a baseline is named, a commit is: `Branch.ForkBaseline` → `ForkCommit`, `Branch.Head` stays
   (a commit id), `Change.BaselineID` → `Change.ForkCommit`, `Change.ResultBaselineID` → `Change.LandedCommit` (the
   commit that landed the change on a lasting branch, as today), the kinds `BaselineCommit`... → `CommitKindCommit`,
   `CommitMerge`, `CommitFastForward`, `CommitRebase`, `CommitRevert`, `CommitSnapshot`; `BaselineEvent` →
   `CommitEvent`, `BaselineStep` → `CommitStep`, `BaselineLineage` → `CommitLineage`. The RPCs: `ListBaselines` →
   `ListCommits`, `GetBaselineGraph` → `GetCommitGraph`, `GetBaselineDelta` → `GetCommitDelta`, `ListBaselineNodes` /
   `Links` → `ListCommitNodes` / `Links`, `DiffBaselines` → `DiffCommits`; proto message `Baseline` → `Commit` and the
   fields that carry ids. `graphapi.Commit` (the request of `CommitEdits`) becomes `graphapi.Edits`, its
   `CommitResult.Baseline` → `CommitResult.Commit`. The web's Baseline explorer becomes the History explorer, its tab
   `baseline` the tab `commit`. The operation `CommitChange` keeps its name: it writes a commit.
5. **No compatibility layer.** Clients and services ship together and the database starts from zero (the rule of the
   protos): the migration renames the table, its indexes and columns in both dialects, and fills the commits of the
   open change branches from what they committed (one row per distinct `committed` order on a change branch). No alias
   keeps the old names, in Go, in the protos or in the web.
6. **Older ADRs keep their words.** They are history; a note at the top of ADR 0056, 0140 and 0143 points here.

## Consequences

- A change branch's state has an id: a diff, a change-stack, an export, a tag or a query can name the state of a change
  as it committed, and a sub-change's fork is a commit like any other fork.
- A commit on a change branch costs one more row, the header; nothing reads the state whole on a commit (ADR 0137).
- The rename touches every module, the protos, the web and the docs (about 4 000 occurrences): it is done in steps on
  one branch, after ADR 0174 lands, so the two do not rewrite the same lines twice.

## Steps

1. Schema and Go names in `contracts` and `graph` (table, columns, indexes, types, `Tx` methods, `sqlbuild` and its
   golden tests), every module building.
2. Rows for the change branches' commits, one state reader, `ForkSeq` and the ephemeral path gone (`graph/graph`
   tests on memory, SQLite and PostgreSQL).
3. Protos, `graphclient`, services, the engine, the gateway, `goap-bench`, Grafana.
4. The web (types, API, Baseline explorer → History explorer, lanes, change-stack).
5. CLAUDE.md, `docs/architecture.md`, `docs/architecture-overview.html`, the notes on the older ADRs.
