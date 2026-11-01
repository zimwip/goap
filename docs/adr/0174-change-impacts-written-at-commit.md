# ADR 0174 — Change impacts are written at commit

**Status**: accepted, implemented · **Date**: 2026-10 · **Builds on**: ADR 0029 (event-sourced impacts), ADR 0079
(drafts and versions at commit), ADR 0102 (commit and merge), ADR 0103 (sub-changes), ADR 0140 (histories by page),
ADR 0153 (a change read as a view of its log), ADR 0173 (counters of a change) · **Supersedes**: the `change_impact`
projection of ADR 0029

## Context

The `change_impact` table was the projection of the impact events of a change (ADR 0029): every event (proposed,
created, checked out, updated, transitioned, reviewed, withdrawn, committed...) rewrote the row of its impact in the
transaction that appended it. Nothing in it was not in the log, and the view of the change (`draftState`, ADR 0153),
which every operation reads, is built from the log alone and never read the table. On a change of 10,000 impacts every
operation paid one more row write (an upsert of a wide row with its reviews, items and effect in JSON, a table at fill
factor 70 for its updates, a sequence moved per write) for a projection the hot path never read.

It was read for: the changes having acted on a node (`NodeChanges`), the impacts of a change not folded (the nodes of
a baseline difference, the provenance of an export, an adoption), the keys of a change for the index
(`ChangeImpactKeys`), and the unique index `change_impact_live` (one live impact per node and flow). And no commit
pointed to the event of the log that made it.

A change impact before its commit is a proposal: a draft, reviews, lifecycle moves. It is the business of its change,
held by its log and its view. What the rest of the graph needs is what the change *did*: the versions it wrote, from
where to where, and why.

## Decision

1. **A change impact exists in the store only once committed.** Before its commit an impact lives in the log of its
   change and in the view folded from it, nowhere else: no row for a proposal, a draft, an edit, a review, a move, a
   withdrawal. `Graph.emit` appends the event and folds it into the view; the projection (`project`,
   `Tx.PutChangeImpact` / `DeleteChangeImpact` / `ChangeImpacts` / `ChangeImpactKeys`) is gone, and a store's
   `Tx.Change` answers the header (the guard folds the impacts and items, every store alike).
2. **Writing a version writes the impact that produced it.** `change_impact` holds one row per version a change wrote
   (`graph.CommitImpact`, `Tx.PutCommitImpacts`, `graph/graph/commitimpacts.go`), written in the transaction of the
   version and never updated:

   | column | what |
   |---|---|
   | `node_id`, `version` | the version written (primary key; the arrival point) |
   | `pre_version` | the starting point: the version it was checked out from (`Parents[0]`), NULL for a creation |
   | `merged_with` | the second parent of a rebase or a merge version, NULL otherwise |
   | `change_id`, `impact_id` | the change and its impact (the impact id stays the same across the commits of a change; NULL for a revert of a withdrawn impact) |
   | `key`, `type`, `intent`, `rationale`, `reason` | as declared, `reason` as `node_version.reason` |
   | `review` | the last verdict of the impact, the one that let it commit (ADR 0105) |
   | `effect` | what the version does over `pre_version` (`domain.EffectOf`, ADR 0173) |
   | `log_seq` | the position of the entry of the change log recording the commit (0: none, the bootstrap) |
   | `produced_by`, `execution`, `via`, `created_at` | provenance of the impact |

   The writers: the commit of a change on its branch (`apply.go`: the versions of its impacts, pointing at the
   `committed` entry, and the reverts of the impacts withdrawn since their commit), the three-way merge (`merge.go`: its
   merge versions, rows of the merge change pointing at its `committed` entry; no effect), the bootstrap (no log,
   `log_seq` 0). On PostgreSQL the rows are held with the versions of the commit and sent with them (`heldQuerier`,
   `pipeTx`): no statement of their own. The table stays apart from `node_version`, so the selection of a state (ADR
   0143), which reads versions, does not read the impact's text.
3. **Intermediate commits.** A change commits several times (ADR 0102, 0165): each commit writes, per node it commits,
   a version and its row, whose starting point is the version of the change's previous commit of the node (the draft
   is checked out again from it), else the version the change started from; its effect is the step from there
   (`cpost.step`). What the change did to the node over all its commits is the first `pre_version` to the last
   `version`, which the view folds (the net effect of ADR 0173 and `Change.Stats` are unchanged).
4. **A commit points to its log event.** `log_seq` ties every version a change wrote to the `committed` entry (ADR 0144:
   one entry per commit) that recorded it; `Graph.ChangeAt(change, log_seq)` reads the change as it stood then. A
   baseline names its change and its versions; the rows of those versions give the entries. Nothing is added to the
   baseline.
5. **Merges and joins.** A fast-forward or the integration of a change into its parent branch (a version joining a
   branch, `node_branch`, `SetNodeOrigin`) writes no version and no row. The merge of a sub-change into its parent
   (`recordInParent`) records the parent's impact in the parent's log as before; the versions joined keep the row of the
   sub-change that wrote them.
6. **Readers.**
   - The changes acting on a node (`NodeChanges`, `Tx.NodeChangePage`): since ADR 0180 the log names no node; the
     changes that committed it are read from its versions (the cursor the first entry committing one), the open ones
     from their views (the cursor the first event of an impact naming it), committed or not.
   - The difference of two baselines reads the nodes of the changes it spans from their rows (and their branches).
   - The provenance of an export, an adoption and the keys of a change's index document read the view.
   - The uniqueness of a live impact per node and flow is a rule of the fold, checked by the operations under the
     change's lock (ADR 0127); the store holds no row to index.
7. **Tests.** The graph tests (`checkImpactLogs`, every repository) replay every change's log against its view and
   check the rows against its `committed` events (`checkCommitImpacts`: one row per version, its reason, key and
   starting point those of the version, `log_seq` the entry of its committed event; every version a change committed on
   its own branch has its row). The database starts from zero: migration `0024_impacts_at_commit` drops the projection.

## Consequences

- Until it commits, a change writes its log and nothing else: no row write per operation for a projection.
- A commit writes one more row per version, in the statements that write the versions.
- "What did this change do to this node, from which version to which, and which entry recorded it" is one row.
- A change that commits nothing leaves no impact outside its log: `PurgeChange` deletes the log and nothing else.
- Whatever read an open change's impacts from the table reads the view (folded once, kept by the cache of views).

## Addendum: the version is the record

Once every path writing a version wrote its row with it, the row and the version were one to one: the row repeated the
key of the version, its node, its change, its reason, its execution, its time and its parents, and what it held of its
own (intent, rationale, the review that let it commit, the effect, the producer, `via`, the entry of the log) is in the
log of the change, folded into its view. The only reader of the table in the services listed the nodes a change wrote,
which the versions answer by their change (`node_version_change`).

The table goes (migration `0030_no_change_impact`, both dialects; `CommitImpact`, `Tx.PutCommitImpacts` /
`CommitImpacts` with it). A version names its change (`node_version.change_id`) and its impact
(`node_version.change_impact`); what made it is read in the view of its change, by that impact: its reviews, the
`committed` event that names the version and its effect over the impact's pre (ADR 0173). The bootstrap, which logs
nothing, keeps its intent as the comment of its versions. `Tx.ChangeNodeIDs` lists the nodes a change wrote versions
of (the difference of two baselines). The effect of a version over the one it was checked out from, which only the row
kept, is no longer computed at commit (two reads per node committed again). The tests check the view instead
(`checkVersionImpacts`: a version naming an impact names one of its change's log, the version a committed event of the
change names on its own branch names that event's impact). A commit writes the versions and their links only.

A change commits several times (ADR 0102): an impact may write a version at each commit, so the impact alone does not
say which of its commits wrote a version. A version names the entry of its change's log that committed it
(`node_version.log_seq`, migration `0031_node_version_log_seq`, both dialects; `Node.LogSeq`, proto `Node.log_seq`):
the view of the change read at that position says what made it then (intent, rationale, review). The entry is known
before the versions: the commit reserves its position first (`Tx.ReserveLogSeq`: on PostgreSQL the position the lock
of the change read ahead, else one of the sequence; on SQLite and in memory the next one, the writer being alone), writes
the versions naming it, then appends the entry at that position (`Graph.emitAt`, `AppendLog` with `Seq` set): no
`UPDATE` of a version. The guard holds it so (`guardTx.reserved`): one reservation at a time, nothing else appended
until the entry takes it, every version of that change written meanwhile naming it, none naming a position not
reserved, no reservation left at the end of the transaction. The commit of a change, a merge (its versions written
once its impacts are declared and reviewed, before the entry of its commit) and the reverts written again over a moved
fork (`refreshReverts`, the entry of the `rebase` commit) all write so.

No version is rewritten when a branch takes it (by default nothing is updated): `SetNodeOrigin` is gone. A version keeps
the change, the impact and the entry that wrote it, a sub-change's version taken by its parent included; the merge of
a three-way merge keeps the merge change that wrote it. The integration is the row of `branch_commit` (migration
`0032_branch_commit_log_seq`, both dialects): `change_id` the change whose merge made the branch take the commit,
`log_seq` the entry of that change's log recording it (`Tx.JoinCommits(..., logSeq, ...)`; a fast-forward reserves the
position of the committed entry of the change before it takes the commits, the reverts it carries named by it too; a
three-way merge, its merge change's committed entry; a commit of the branch's own, the entry committing its versions;
none for a move of a fork), read with the version by `Tx.VersionJoins` (`domain.VersionJoin.LogSeq`). The guard
accepts a position reserved or appended by the transaction only. A revert
commits for the impact withdrawn (`withdrawnImpacts`: the last withdrawn impact of the node, from the log; written
again, the impact of the revert it replaces): it names it and the `committed` entry of its commit names the revert,
which the view of the change, holding the impact no more, leaves aside. `checkVersionImpacts` checks that every version
of a change names an impact and an entry of its change committing it (the bootstrap's roots aside).

A version is written whole, at once: how it holds its links (ADR 0141: `links`, `links_base`, `links_depth`,
`links_seq`) is planned before it is written (`Graph.planLinks`, reading only the earlier versions of the node) and
given with it (`Tx.PutNode(ctx, n, LinkSet)`; `Graph.putVersion` plans, writes the version, then the rows of its set,
`writeLinks`); there is no `PutLinkSet`, no `UPDATE` of `node_version` on any store. A commit numbers its versions
first, then writes each with its links once those are resolved to the numbers; a merge plans the links of its versions
before writing them. The node keeps no latest version (`node.latest` dropped, migration `0033_no_node_latest`, both
dialects): it was the counter of the numbers of a node's versions across branches, not a head; the greatest version is
read on the key of `node_version` (`Tx.LatestVersions`, `max(version)`), and two writers giving a node the same next
version meet on that key (`ErrConflict`), as they met on the compare-and-set of the counter before. The `node` row is
inserted with the first version and never written again: what it holds (key, type, project, creator) is the node's
for good.

A commit never updates its branch either: the head of a branch is the search of its last commit, by order
(`dialect.branchHead`: the commit of the greatest `seq` whose `branch` is the branch's, over the index `commit_branch`,
else the head the branch was created with, `head_commit`, which no commit writes any more). Its history is linear by a
key, not by a compare-and-set: two commits of one branch never stand on the same parent (unique index `commit_parent`
on `(namespace, branch, parent_id)`, migration `0034_branch_head_searched`, both dialects); `advanceBranch` keeps the
lock of the branch, which orders its commits (`at`), and accepts the head it reads being the commit just written. The
branch row is written again only when what it says changes (status, description, fork, parent): an upsert that would
restate it writes nothing.

The lock of a branch is held to the end of the transaction that takes it, so a merge keeps what it does under it short
(ADR 0127): the plan is read before; under the lock, a head that moved meanwhile on none of the nodes the branch holds
(`reforkFastForward`: the nodes the commits of the target committed since the head planned, a few) keeps the plan, the
fork moved once more onto the head that stands; the headers it writes (branch, commit, a change whose header the
transaction holds) go with the next statement (`heldQuerier.write`) rather than one round trip each.
