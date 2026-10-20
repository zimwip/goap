# ADR 0143 — The state of a node is a selection; baselines keep no entries, tags name versions

**Status**: accepted; §1 to §3 implemented (no `baseline_entry`, no checkpoints, the bootstrap a landing), §4 to §8 to come · **Date**: 2026-10 · Builds on ADR 0032 (versions and branches), 0056 (baselines and tags), 0137
(a commit reads the nodes it touches, and its addendum *The version of a node in a baseline is a selection*), 0140 (a
baseline is computed from the versions that landed) · Supersedes the checkpoints of ADR 0140 and the change tags of
ADR 0056

## Context

Every node version is complete: its properties, its owner, its state and its link set (ADR 0141). A version lands on a
branch at a point of one global landing order (`node_version.landed`, `node_branch.landed`, `landing_seq`), and a
baseline is a point of that order on a branch (`baseline.seq`). So the version of a node at a baseline is not folded
from anything: it is **the version of that node with the highest landing order up to the baseline's on its branch**,
one lookup in the indexes `node_version_fold` / `node_branch_fold` `(node_id, branch, landed)`. The addendum of
ADR 0137 made the commit and the merge read their nodes this way, and the bench of 18 minutes in lifecycle mode went
from a merge p50 climbing from 80 to 370 ms to one flat at 75 to 79 ms.

What is left of the stored states (`baseline_entry`) then serves three readers only:

- **The checkpoints** of ADR 0140 (`CheckpointEvery`, the background `checkpointer`, `GOAP_CHECKPOINT_EVERY`,
  `GOAP_MATERIALIZE_EVERY`), written so that reading a state would not replay a long branch. The selection does not
  replay anything: the depth of a branch no longer costs a read, and keeping a checkpoint chain warm became the cost
  itself (the latency growth of the bench came from re-reading it).
- **The reads of a whole namespace**: the snapshot export, the Baseline explorer (`ListBaselineNodes`,
  `BaselineGraph`), the access and hub snapshots, the test replay `checkStates`. None needs a stored copy: they need the
  list of the nodes of the namespace and, for each, its selected version.
- **The bootstrap**, the one snapshot of main (`domain.BaselineSnapshot`), whose state is written as entries instead of
  as a landing.

A tag (ADR 0056) names the state a change leaves, that is a whole namespace. What people tag is a node: a release, a
configuration, a document set, whose version links to the exact versions of what it is made of.

## Decision

1. **The version of a node at a baseline is a selection** by (node, branch, landing order), never a fold: the last
   landing of the node on the baseline's branch at or below its order, else, past the branch's fork, the selection on
   the branch it forked from at the fork's order (a sub-change: its ancestors up to `Branch.ForkSeq`). One statement
   reads it for a batch of nodes (`Tx.LandedVersions` with ids, the lateral query over `unnest` on PostgreSQL). Rebase
   and revert write real versions on their branch (reasons `rebase` and `revert`), so their baselines select like any
   other.
2. **No baseline stores entries.** `baseline_entry` is dropped with the checkpoints: `CheckpointEvery`,
   `DefaultCheckpointEvery`, the `checkpointer`, `Graph.WaitCheckpoints`, `GOAP_CHECKPOINT_EVERY`,
   `GOAP_MATERIALIZE_EVERY`, `Tx.StoredVersions` / `StoredSizes` / `LatestStored` / `MaterializeBaseline`, the
   `stored` kinds and `base_id` / `depth`. A baseline is a header: id, namespace, branch, order, kind, the change that
   left it, its parent and name. `Tx.LatestUnderived` and the partial index `baseline_underived` go too: there is
   nothing left to stop at but the fork.
3. **The bootstrap is an ordinary landing.** `Graph.Bootstrap` writes the roots as versions landed by its change on main
   (as any commit does), and its baseline is a header like the others; the empty state before it stays `""`.
4. **A whole namespace is read as its node list, then the selection.** The nodes of a namespace are `node` rows
   (namespace, type, key), filtered there first (type, key, text through the node index of ADR 0026, owner, project),
   then each kept node's version is selected in batches. The current head of every (node, branch) is a search view of
   the index: the snapshot export, the explorer's pages, the access and hub snapshots read it rather than a baseline;
   reading a past baseline whole stays possible (the same selection with its order) and pages like the explorer does.
   `checkStates` replays the landings and compares with the selection.
5. **A diff walks the changes between two baselines.** `DiffBaselines` and the change-stack follow the landings that
   lead from one to the other and compare the nodes they touched, selected at both ends: cheap between close baselines,
   dearer as they move apart, refused past `MaxDiffChanges` (1000) with a request to compare closer baselines (done
   with the addendum of ADR 0137).
6. **A tag names one node version.** `domain.Tag` becomes `{name, node, version, by, at}`: it references one exact
   version, nothing more. What that version stands for is read from it: its outgoing links name exact versions
   (ADR 0079: a link resolves to a version at landing), so a reader follows them as far as it needs; the tag stores no
   closure. `Graph.TagChange` and the change tags go, and nothing names a whole namespace at a point: a change keeps its
   title, its baseline its name, and the point is its (branch, order).
7. **A smart tag is a shared selection of node versions.** A smart tag `{name, description, owner, versions [(node,
   version)], by, at}` selects several exact versions, of any nodes and namespaces, to give people a context (the
   versions a review, a release or an investigation is about). Its owner shares it with other users (read or edit, the
   way other shared things are granted); the selection is data of its own, never a state of the graph, and adding or
   removing a version is an edit of the smart tag, not a change of the nodes. Reading it is the versions it lists and,
   from each, its links. Smart tags are kept in a store of their own, like the preferences (ADR 0038): a table with its
   migrations in both dialects and its RPCs, no change, no journal; the owner shares a smart tag with users by subject,
   to read or to edit.
8. RPCs, `goap-change` and the web (the tags of the Baseline explorer) move to `TagVersion` / `Tags(node)` and the
   smart tag calls (create, edit the selection, share, list the ones shared with the caller).

## Consequences

- Writing a baseline is one header row whatever the size of the namespace; nothing is written in the background, and
  the cost of a read is the number of nodes it reads, not the length of the branch.
- A read of a whole past state costs one indexed lookup per node of the namespace: an export or the explorer pays it,
  page by page; no hot path does.
- `MaxDiffChanges` makes a diff of distant baselines a refusal rather than a long read; the change-stack and the
  explorer say so.
- Tags no longer label a namespace: a release that must be reproducible is a node whose version links to what it
  releases, or a smart tag selecting the versions. The demo data and the docs that tag changes move to these.
- The database starts from zero (no migration of `baseline_entry` or of the stored tags); the suites drop their runs
  with `GOAP_MATERIALIZE_EVERY`.
