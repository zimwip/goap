# ADR 0144 — A review, a landing and a rebase are one entry each in the log of a change

**Status**: accepted; reviews and landings implemented, the rebase to come · **Date**: 2026-10 · Builds on ADR 0029
(change impacts are event-sourced), 0030 (one log per change), 0080 / 0105 (every verdict is a submitted review), 0101 /
0102 (rebase and merge), 0142 (impact events carry what they change)

## Context

The log of a change is the largest write of the graph. On the bench of ADR 0140 in lifecycle mode (3,000 changes of 50
nodes), `change_log` held 1.0 million entries, 634 MB:

| Type | Entries | MB | Bytes per entry |
|---|---|---|---|
| `fact.review` | 153,385 | 66 | 442 |
| `impact.created` | 45,882 | 48 | 1,062 |
| `impact.landed` | 306,810 | 41 | 138 |
| `impact.proposed` | 107,652 | 32 | 302 |
| `impact.reviewed` | 153,438 | 30 | 199 |
| `impact.updated` | 139,440 | 25 | 185 |
| `impact.checkedOut` | 107,652 | 23 | 223 |

A review of n impacts wrote its record and then n `impact.reviewed` entries, one per verdict, each naming the record as
its cause; read in order, the log showed reviews and verdicts interleaved. A landing wrote one `impact.landed` entry per
impact, twice per change (its commit on its branch, its merge into its target). Each entry also costs its row, its
envelope and its index entries. The bench reviewed impact by impact, so each change wrote 50 review records, and every
verdict read the whole change and decoded all its facts for the review policy: one change cost n² reads, which was
most of the garbage the service collected.

## Decision

1. **One entry may carry several impact events of one operation.** The events of a batched operation
   (`domain.ImpactOp.Batched`: `reviewed`, `landed`) that follow each other with one envelope (change, flow, run,
   author, cause: `domain.SameEntry`) are one log entry: its payload is `{"impacts": [part...]}`, each part the payload
   of an event with its `impactId`, and what every part holds alike (a key, or a key of an object every part holds) is
   written once next to the parts (`domain.ImpactBatchEntry`). Every reader goes through `domain.ImpactEventsOf`
   (the graph's `decodeImpacts`, the phases of a lifecycle, the PROV-O export, the tests) or, in the web,
   `decodeImpactEvents` (the audit trail, the sub-change history); the event ids of an entry carrying several are
   `<entry>#<n>`. The folds, the drafts and the projection see the same events as before.
2. **A review is its record, then one entry applying it.** The submitted record (`fact.review`: the impacts, their
   outcomes and comments) is written first; its verdicts are then applied to the impacts and logged as one
   `impact.reviewed` entry naming the record as its cause. The verdicts of a batch judge each other (an acceptance
   whose origins the same review accepts), so they are staged: applied to the projection and to the fold of the change
   as they are made, logged as one entry when the review is over (`impactStage`, `Graph.stage` / `flush`). On a flow
   they are not staged (what a flow sees is read from the log): one entry per verdict, as before. The review policy
   reads the facts of the change from its fold, not from the log.
3. **A landing is one entry.** A commit logs the versions it wrote as one `impact.landed` entry, and so does the
   integration into the target; a merge of branches logs its creations, its review, its versions written and its
   landing by phase, one entry each.
4. **A rebase is one entry, then the nodes it changes, then a review, then the commit** (to come). The rebase is a
   `change.updated` entry naming the head it brings the branch onto (written first, before the nodes). Each node
   changed on both sides is checked out from the version the change committed (a `checkedOut` naming it, ADR 0142,
   with the version it is merged with) and updated with the difference the three-way merge makes (an `updated` patch:
   what a reader shows as the diff), never a copy of the merged node. The merge is then validated by a review: a clean
   rebase is a submitted review of `graph.merge` accepting the merged nodes, a conflict sends its impact back to
   proposed for a person to settle and review. The commit lands it (one entry).
5. The bench reviews a change in one batch (`ImpactNodeReviewBatch`), as the web submits a review.

## Consequences

- A review of n impacts is two entries instead of n + 1, a change's landings two instead of 2n; the bench's review
  records go from one per impact to one per change.
- A verdict no longer re-reads the facts of its change.
- The database starts from zero: no entry is migrated. A reader of the log written by another tool must read the
  `impacts` parts.
