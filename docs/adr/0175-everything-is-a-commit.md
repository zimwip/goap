# ADR 0175 — Everything is a commit

**Status**: accepted, implemented · **Date**: 2026-10 · **Builds on**: ADR 0056 (baselines are states of changes),
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

1. **A commit is the one record of a state.** The table `baseline` becomes `commits` (`commit` is a reserved word of
   SQL), `domain.Baseline` becomes `domain.Commit`, `BaselineID` becomes `CommitID`. Its columns keep their meaning:
   `parent_id`, `merged_from` (the second parent), `rebased_from`, `change_id`, `namespace`, `branch`, `kind`, `seq`,
   `created_at` (a commit finds the entries of its change's log through its versions, ADR 0174: `change_impact` keyed
   by node and version), and it gains `fork_commit` (`Commit.Fork`): the commit its branch stood on when it was
   written, none on main. `name` stays a label; a name a person gives
   to a state is a tag (ADR 0056, ADR 0143 §6).
2. **Every commit is a row, on any branch.** `CommitChange` writes a commit on the change's branch (`commitOwn`), its
   parent the previous commit of that branch, else the commit the branch was forked from; its `seq` is the order it
   already takes from `commit_seq`. A revert writes its own the same way; a move of the fork of a branch that committed
   (a rebase, the fork after a merge of ADR 0166, a new parent of ADR 0106) writes a commit of kind `rebase` naming the
   new fork as its second parent (`forkMoved`); the merge of a sub-change into its parent's branch writes a
   `fast-forward` on the parent's branch, named after the sub-change. A
   change branch is a branch like any other: `Graph.BranchHead` answers its head commit, `errEphemeral` goes.
   `Branch.Ephemeral` stays as what it says (the branch lives as long as its change), it no longer changes how its
   state is read.
3. **One way to read a state.** The state of a commit is the selection of ADR 0143 for every branch (`selectState`):
   each node's last commit on the commit's branch up to its `seq` (a revert leaving the node to the fork), else the
   state of the commit it stands on (`Commit.Fork`), and so on; a rebase moves the branch, never the commits written
   before it, which keep their own fork. A sub-change forks from a commit of its parent's branch (`Branch.ForkCommit`,
   the parent's head commit, else the parent's fork: `Branch.HeadOrFork`), so `Branch.ForkSeq`, `endCommit`,
   `chainVersions` and `errEphemeral` go; `branchVersions` stays as the read of a branch as it stands (its fork, then
   every commit of it), the merges' starting point.
4. **Names.** Everywhere a baseline is named, a commit is: `Branch.ForkBaseline` → `ForkCommit`, `Branch.Head` stays
   (a commit id), `Change.BaselineID` → `Change.ForkCommit`, `Change.ResultBaselineID` → `Change.LandedCommit` (the
   commit that landed the change on a lasting branch, as today: a commit on the change's own branch never sets it), the
   kinds `BaselineCommit`... → `CommitKindCommit`, `CommitKindMerge`, `CommitKindFastForward`, `CommitKindRebase`,
   `CommitKindRevert`, `CommitKindSnapshot`; `BaselineEvent` →
   `CommitEvent`, `BaselineStep` → `CommitStep`, `BaselineLineage` → `CommitLineage`. The RPCs: `ListBaselines` →
   `ListCommits`, `GetBaselineGraph` → `GetCommitGraph`, `GetBaselineDelta` → `GetCommitDelta`, `ListBaselineNodes` /
   `Links` → `ListCommitNodes` / `Links`, `DiffBaselines` → `DiffCommits`; proto message `Baseline` → `Commit` and the
   fields that carry ids. `graphapi.Commit` (the request of the producer path) becomes `graphapi.Edits`, its
   `CommitResult.Baseline` → `CommitResult.Commit`, and `Graph.Commit` (that path) becomes `Graph.CommitEdits`, so
   `Graph.Commit` reads a commit. The web's Baseline explorer becomes the History explorer, its tab
   `baseline` the tab `commit`. The operation `CommitChange` keeps its name: it writes a commit.
5. **No compatibility layer.** Clients and services ship together and the database starts from zero (the rule of the
   protos): the migration (`0025_commits.sql`) renames the table, its indexes and columns in both dialects and adds
   `fork_commit`; nothing fills the commits of change branches written before it. No alias
   keeps the old names, in Go, in the protos or in the web.
6. **Older ADRs keep their words.** They are history; a note at the top of ADR 0056, 0140 and 0143 points here.

## Consequences

- A change branch's state has an id: a diff, a change-stack, an export, a tag or a query can name the state of a change
  as it committed, and a sub-change's fork is a commit like any other fork.
- A commit on a change branch costs one more row, the header; nothing reads the state whole on a commit (ADR 0137).
- The events of a change name the commit of its branch (`committed` events, `ImpactEvent.Commit`); `CommitEvent`
  carries the fork. A change-stack names the sub-changes a merge brings in from that merge only (`lineageGap`).
- The History explorer lays every commit of a change's branch on its lane.
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
