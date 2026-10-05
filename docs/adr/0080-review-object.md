# ADR 0080 — The review object: a review built up, then submitted

**Status**: accepted, implemented (graph mechanism, `pkg/review`, services, `goap-change`, PROV-O, web) · **Date**: 2026-10 · Builds on
ADR 0036 / 0065 (change items of a use case), ADR 0029 / 0030 (the impact log), ADR 0075 (`ReviewPolicy`), ADR 0079 (reviews are
explicit, gated on the draft); the naming follows ADR 0077.

## Context

A review was one `ImpactNodeReview(change, impact, accept, comment)` per impact: a reviewer going through ten impacts made ten
independent calls, each committed at once, with a comment each and no way to say "this is my review of the change". Nothing
grouped them in the log, and a reviewer could not leave a decision half made.

## Decision

### 1. The review is an item of the change

A **review** is a change item of the kind `review`, in the manner of `risk`, `action`, `waiver` (ADR 0036, 0065): each item is a
version of the record of its `data.key`, event-logged as `fact.review`. `pkg/review` registers the kind (`review.Register()`,
called by `cmd/graph`, `cmd/engine`, `cmd/goap-dev` and the `TestMain` of the tests that write it) and folds the items
(`review.Reviews(items)`, pure). `pkg/graph` and `pkg/domain` import nothing of it (`pkg/layering`).

Data of an item (the whole record, a version never restates by omission): `key` (`REV-…`), `flow` (the flow the entries are
reviewed on, empty: main), `comment` (the **global comment**), `status`, `entries` `[{impact, comment, outcome}]`, `by` (the
author), `submittedAt`.

- **Status**: `open` while the reviewer builds it, then `submitted` or `discarded`. Both are **final**: the fold ignores a later
  version of the key, and every operation refuses a review that is not open (`ErrConflict`).
- **Entry**: names one change impact, has its own comment and its own outcome `accept` / `reject` (empty while undecided: an open
  review may hold undecided entries; a submitted one has none).
- An impact is in **at most one open review per flow** and an entry takes only an impact of the review's change and flow whose
  review is `proposed` as the flow sees it (`review.Awaiting`: what the web's select box lists). A discarded review frees its
  impacts.

### 2. Operations

Change-level names, no `ImpactNode` prefix (ADR 0077): `ReviewOpen(change, flow, comment)`, `ReviewUpdate(change, key, edit)`,
`ReviewSubmit(change, key)` and `ReviewDiscard(change, key)`. `ReviewUpdate` takes an edit, not a replacement: `comment`, `remove`
then `add` (impacts), then `entries` (`{impact, comment?, outcome?}`; "set" flags on the wire say which field is written, as proto3
has no unset). They are RPCs of `graph.v1`, tools of `goap-change` (`reviews`, `review_open`, `review_update`, `review_submit`,
`review_discard`; impacts named by id or node key) and calls of the web client (`graph.reviewOpen` ...).

The logic is the use case `review.Service` over a small `review.Port` (`Change`, `AddItems`, `ImpactNodeReviewBatch`), implemented
by `graph.Graph` and `graphsvc.Client` and so by every caller of `engine.GraphPort` (which gained those two methods: the
blackboard is the view of one flow, a review needs the whole change). `graphsvc.Handler`, the `goap-change` connector and tests
build a `Service` over their graph; none of them has review logic of its own. The graph does not import `pkg/review`, and
`pkg/review` does not import `pkg/graph` (the graph's tests import it): the refusals are `review.ErrInvalid` / `ErrConflict` /
`ErrNotFound` / `authz.ErrForbidden`, mapped to the Connect codes by `rpcerr` like the graph's own.

**Authorization**: only the author or an administrator updates, submits or discards (`review.Actor{Subject, Admin}`; the handler
asks the floor of the authorizer, as `gateAccess` does, a platform service and a principal holding `admin` also count). There is no
`change:review` action in the ABAC rules today (`ImpactNodeReview` itself has none, ADR 0075 only adds the `ReviewPolicy`), so none
is invented: the author-or-admin rule is the use case's. **The verifier-is-not-producer policy of ADR 0075 applies per impact, exactly
as for a review on its own**, at submit: the submitter is the reviewer of every entry.

### 3. Submit is the ordinary review, in one transaction

`Graph.ImpactNodeReviewBatch(change, domain.ReviewBatch{ID, Flow, Execution, By, Verdicts, Item})` is the mechanism the submit
stands on. `ImpactNodeReviewOn` became a thin transaction around `reviewTx`; the batch runs `reviewTx` for every verdict in **one
transaction**, so each entry gets everything a review on its own gets: the change and flow open, the impact proposed as the flow
sees it, the `ReviewPolicy`, the gate of an accepted draft (validators, required links, origins), the `reviewed` event.

- **All or none**: any refusal returns an error that names the verdict (`verdict 2 (REQ-1): ...`) and rolls everything back; the
  review stays open, nothing was reviewed. (A refused call writes nothing, and the draft cache publishes nothing uncommitted,
  ADR 0079.)
- **Origins first**: an acceptance whose merge / split origins are accepted by the same batch stands whatever the order of the
  verdicts: a verdict refused only for its origins (`errOriginsPending`) is tried again after the others, until a pass makes no
  progress.
- **Comments**: the review of each impact keeps both comments: `entry comment` then, after a blank line, `Review: <global
  comment>` (`review.EffectiveComment`; either alone when the other is empty; a submit with neither for an entry is refused: a review
  needs a comment).
- **The review id**: the batch's `ID` (the review key) is stamped on every review it writes: `domain.Review.ReviewID`, in the
  `reviewed` event, the proto `Review.review_id`, the audit trail and the PROV-O export. The graph treats it as opaque.
- **The item**: `Item` (the submitted version of the record, `submittedAt` set) is written in the same transaction after the
  verdicts, on the resolved flow. The batch refuses the reserved kinds (`flow`, `transition`, `decisionPoint`) and runs the item
  authorizers, as `AddItems` does. The RPC `ImpactNodeReviewBatch` exposes the mechanism (the reviewer is the principal of the
  request), for remote callers of the port.
- An impact edited after the submit follows the existing rule (ADR 0079): its review goes back to `proposed`; the submitted review
  stays as it was, a record of what was decided then.

### 4. Audit and PROV-O

The audit trail shows each version of the record (`review open`, `review submitted`, `review discarded`: subject the key, summary the
global comment and the tally), marks the `reviewed` events of a review (`AuditEntry.reviewId`, "accepted in review REV-1 — ...") and
`reviewGroups` returns the entries of each review. In the PROV-O export the review is a `prov:Activity` (`goap:ReviewObject`) of its
author, started when opened, ended when submitted, about the impacts of its entries; its `reviewed` events are `goap:partOf` it, and
the item versions are `goap:ReviewRecord` entities (`goap:reviewOf`).

### 5. Web

A **Reviews** pane in the change editor (`ReviewPanel.svelte`, scoped like the impacts pane): the reviews of the scope, open first,
`New review`; for an open review of the caller (or an administrator) a global comment, a select box of the impacts awaiting review
(one, or all) with `Add`, a table of the entries (impact linked to the Impacts pane, comment input, accept / reject radios,
`Remove`), `Submit review` (disabled until there is an entry, an outcome on each and a comment, its own or the global one; the title
says what is missing) and `Discard` (confirmed). A submitted or discarded review is read-only, with the comments and what each
impact is now. The per-row Accept / Reject of the impacts stay as the shortcut for one impact. Following the one-stream rule
(ADR 0053) nothing is read again after a write: the answer of the write is put over what the change shows (`overlay`, by version)
until the `change.updated` event brings the new one; the pure rules (`foldReviews`, `awaitingImpacts`, `submitProblems`,
`effectiveComment`) are in `web/src/lib/reviews.ts`.

## Consequences

- A reviewer can decide several impacts with a comment each, think about it, and have it all happen or none; the log says which
  reviews were one submission.
- `ReviewOn` and the batch share one code path: a rule added to a review reaches the review object with no change.
- Greenfield: no migration; the kind is an item like the others, no schema change (SQLite and PostgreSQL untouched).

## Not done

- **Concurrency of two open reviews**: "an impact is in one open review per flow" is checked when an impact is added, reading the
  change, not inside a transaction with the write; two concurrent additions of one impact to two reviews could both succeed. The
  submit still refuses the second (the impact is no longer proposed): all or none, nothing wrong lands. A transactional check would
  need a graph hook the item write does not have.
- Awaiting-review is evaluated by the use case from the change impacts as stored (the reviews of the flow, else the impact's); the
  authoritative view of a flow (`ImpactsSeenBy`) is the graph's, applied at submit.
- No DSL / engine builtin to open or submit a review (an action reviews with `impactNodeReview`); the `goap-change` tools exist.
- No ABAC action for reviews (`change:review`): author-or-administrator only.
- A review of several flows at once: a review is of one flow.
