# ADR 0152 — The asks strip and the decision history of an impact

**Status**: accepted; implemented · **Date**: 2026-10

## Context

The change tab showed what a change waits for from people (ADR 0117) in a column beside every pane. The column took a
third of the tab whatever it held, and when the engine could not answer it held only an error ("unavailable: Bad
Gateway") next to every pane.

The Reviews pane (ADR 0080, ADR 0105) showed every review as the same card: an open draft, a submitted review and
"impacts wait, nobody started" looked alike. A submitted review also showed, on each of its rows, the validation of the
impact *as it is now*: once committed, an impact's post is a version, no longer a draft, and the browser's check
(`notAcceptableWhy`) read it as a candidate that modifies nothing. The rows of a review accepted long ago said
"Validation failed". A column *Impact now* repeated the impact's current review status beside the outcome of the review.

No rule had run on that review. The graph checks an accept on the draft as it stands, in the transaction of the
verdict (`checkAccepted` → `checkDraft`, `checkModifies`, ADR 0079, ADR 0102), and refuses it when a rule fails. The
validation the graph attaches to an impact (`impactValidations`) covers the drafted impacts only.

## Decision

1. **A strip, not a column.** `ChangeAsks.svelte` is one strip above the panes of the change tab: folded, *Waiting on
   people* and the counts (`attentionChips` in `web/src/lib/changeAsks.ts`: the asks for the caller, the impacts to
   review, then the asks for others by addressee); unfolded, the same lists as before, answered in place. Nothing
   waiting: no strip. The asks unreadable: no strip either, the impacts to review still counted. The column, its
   resizer and `layout.asksWidth` are gone.
2. **A submitted review is read as it was decided.** The Reviews pane shows the validation of an impact only on the
   impacts awaiting a review and in an open review, never on a submitted one; a committed impact is not a candidate
   (`changeCommit.committed`). *Impact now* is gone: a row says `now <status>` only when the impact was decided again
   since the review.
3. **The state of a review reads at a glance.** A banner when impacts await a review, with *Start a review* (to
   create); a stepper on each review (*To create* › *Open · draft* › *Submitted*, `REVIEW_STAGES` / `stageOf` in
   `web/src/lib/reviews.ts`); an open review is framed, says whose it is and how many entries are decided; a submitted
   review folds to one line with its verdicts and shows its entries on demand.
4. **The impact carries its decisions.** The review cell of the impacts table (`ChangeLifecycle.svelte`) is
   `ImpactDecisions.svelte`: the status now, the version committed, and, unfolded, every decision the latest first
   (`decisionHistory`): who, when, the comment, the review it was submitted in, the flow, the returns to `proposed` after
   an edit. Nothing new is stored: `ChangeImpact.reviews` already holds every verdict and every return to `proposed`
   (`editedAgain`). An accepted verdict is the proof the rules held on the draft of that moment, since the graph
   refuses an accept that fails them; the history says so ("rules checked on the draft then").

## Consequences

- The panes get the full width back; what waits for the person is one line away on every pane.
- The Reviews pane is the place of the review batch (ADR 0105: every verdict is a submitted review); everything about
  one impact is on the impact.
- Not done: the asks on the step they belong to in the process view of the change (a later step of the same design).
