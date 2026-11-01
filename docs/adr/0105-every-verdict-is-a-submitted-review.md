# ADR 0105 — Every verdict is a submitted review; causation in the log

**Status**: accepted, implemented · **Date**: 2026-10 · Amends ADR 0080 (the review object) and ADR 0030 (one log per
change)

## Context

ADR 0080 introduced the review object: a `review` item a reviewer builds up, then submits, the submission applying its
entries as verdicts in one transaction. It kept the verdict on a single impact (`ImpactNodeReview` /
`ImpactNodeReviewOn`) as an operation of its own, with no review object behind it. Every path that is not a person
submitting a review used it: the actions of the engine (`engine/engine/nodeops.go`), `Graph.Commit` (seeds, `EnsureUser`,
the registry store, `CommitEdits`), the single-verdict RPC behind the Accept / Reject buttons of the web, the DSL
`impactNodeReview`; the merges (`recordInParent`, the three-way merge) wrote `reviewed` events of their own. So the
reviews of automatic changes had no review object, and nothing in the log said which review decided a change impact
(`Review.ReviewID` was empty, and a submitted review was written after the verdicts it made).

The intended model: a review is created, linked to the impacts with its decisions (accepted / rejected); when it is
submitted, the status of each impact changes, pointing at the review, the change log recording that move as caused by
the submission.

## Decision

1. **The review record is built in.** The kind `review` and its record (`domain.KindReview`, `domain.ReviewRecord`,
   `ReviewEntry`, the statuses and outcomes, the validation) move to `contracts/domain`; `ChangeItem.Validate` knows it
   without a registration (`review.Register()` is gone). `contracts/review` keeps the use case (build a review up, submit,
   discard, the fold `Reviews`), its names now aliases of the domain's.
2. **Every verdict is made by a submitted review.** `ImpactNodeReviewBatch` is the one path to a verdict:
   `ImpactNodeReviewOn` is a batch of one verdict. The batch writes the record of the submitted review **first** (the
   use case's record, checked against the verdicts: kind `review`, status `submitted`, one entry per verdict with its
   outcome; else the graph makes one, keyed by the batch's ID or `REV-<uuid>`, by the reviewer, each entry with the
   verdict's comment), then the verdicts, each carrying the key in `Review.ReviewID`. `Graph.Commit` accepts all its
   edits in one review. The merges accept what they land in one review of `graph.merge` (`putReviewRecord`).
   `AddItems` refuses a `submitted` version of a review: it is written with its verdicts or not at all.
3. **Causation in the log.** `LogEntry.Causation` (column `change_log.causation`, both dialects; proto
   `LogEntry.causation`) is the ID of the entry an entry follows from. A decided `impact.reviewed` entry names the
   `fact.review` entry of the submitted review that decided it (`ImpactEvent.Causation`). The record is the
   submission: the `fact.review` entry with status `submitted` is the "review validated" event. A `reviewed` event
   sending an impact back to `proposed` (an edit, a reopening, a lifecycle move, a rebase) has no cause and no review.
4. PROV-O: every entry with a cause carries `goap:causation`; a verdict `prov:wasInformedBy` its review object.

The graph tests check the invariant on every change they leave (`checkVerdictCauses`, run with `checkImpactLogs`): a
decided `reviewed` entry names an earlier `fact.review`, submitted, keyed by the verdict's review, holding the impact
with that outcome.

## Consequences

- The ReviewPanel and the audit trail show the reviews of automatic changes like the others: a producer's commit, an
  action's verdict and a merge each leave a submitted review.
- A change log holds one more entry per verdict made on its own. No API changed shape: callers of
  `ImpactNodeReview(On)` get a review object without asking for one.
- Greenfield: the column is added to `0001_schema.sql` of both dialects, no migration.

## Not done

- Causation is only set on verdicts; other effects (a transition moving drafts, a merge landing versions) do not name
  their cause yet.
- The engine makes one review per verdict of an action; grouping the verdicts of one action run in one review is
  possible (the batch takes several) but not done.

## Addendum: a review is folded from its deltas, its verdicts derived

A review item holds the gesture that wrote it, not the whole record, and a verdict is stored nowhere. Every change is a
fold of its log, the impacts as much as the reviews: what the log says once is never said again.

1. **A delta per gesture.** The data of a `review` item is a `domain.ReviewDelta`: the `key`, and what the gesture
   sets. `status` opens the review (the first delta), submits it or discards it; `comment` replaces the global
   comment; `by` names the author; `remove` drops entries; `entries` add entries, or set their `comment` and
   `outcome`. A field the delta leaves out is kept. `ReviewUpdate` writes only what the edit changes, and
   `ReviewSubmit` writes `{key, status: submitted}`. A review the graph submits on its own is one delta that opens and
   submits it together: a single verdict, `CommitEdits`, the merges, the rebases. `domain.ReviewFold` folds the deltas.
   The first delta of a key opens it, a delta of a final review is ignored, and the time of the delta that set an
   outcome is the time of that verdict (`ReviewEntry.At`). `review.Reviews` and the web's `foldReviews` /
   `ReviewFold` use the same fold.
2. **The submission decides the verdicts.** `ImpactNodeReviewBatch` judges every verdict as before: the policy, the
   gate of an acceptance, the origins settled by the same batch, and an impact no longer proposed is refused. All of
   them stand or none does, and then it writes one entry, the submitting delta. The verdicts are
   `ReviewState.Verdicts`, derived when the view of the change folds that entry (`draftState.review`). There is one
   `reviewed` event per decided entry, at the position of the submission and caused by it, carrying the time of its
   outcome, the comment `ReviewEffectiveComment(global, entry)`, the submitter (or the entry's `by`: a merge carrying
   a sub-change's verdict) and the review's key. The batch names its review by `ID`: an open review of that key is the
   one submitted, and its entries must be the verdicts; else the graph opens and submits one. The batch no longer
   carries an item (`ImpactNodeReviewBatchRequest.item` is gone). A review the graph submits is written once the
   impacts it decides are declared, between the declarations and the commit.
3. **Proposed again is derived too.** An edit of a draft after its verdict sends the review back to `proposed`:
   `domain.ApplyImpactEventAt` does it on an `updated` event, and on a `cancelled` one when the impact remains. This
   covers a property, the owner, a link, the merge a rebase writes into the draft, and a cancel. The edit comes after
   the verdict in the log; no `reviewed` entry is written for it. A rebase that merged cleanly is accepted again by its
   `graph.merge` review; one with conflicts stays proposed. The `proposed` event that identifies a potential impact is
   an action and stays one. So do the explicit reopenings: `ReopenImpacts`, and a lifecycle going back to a state.
4. **Readers.** Whoever reads the log folds the review deltas with it and derives the verdicts. That covers the view of
   the change, the graph tests (`impactEvents`; `checkVerdictCauses` now checks that no decided `reviewed` entry is
   stored), the PROV-O export (`builder.review`), and the web's audit trail and history of sub-changes, which ask for
   `fact.review` with `impact.`.

On the lifecycle bench, this removes one `impact.reviewed` entry per verdict, and a review is no longer copied whole by
each of its versions.
