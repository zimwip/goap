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
