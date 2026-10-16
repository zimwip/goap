# ADR 0130 — Reviewing steps: a review declared in the process it belongs to, started by itself

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0034 (processes and steps), 0035 / 0050
(methods), 0097 (starting points), 0109 (conditions derived from the context), 0110 (reactive activities, the rating of
the description), 0129 (a person reviews model output)

## Context

`sdlc` rates the description of a change (ADR 0110) in a process of its own, `description_review`, whose single step
`rate` names the method `description_rating` and subscribes to the events of the description. The framing of a change,
where that rating belongs, is a manual step of `software_delivery` that says nothing of it; the analysis then waits for
`description_accepted`, a condition the delivery run does not establish, coming from a process the reader has to know
about. Three things are wrong with it:

1. **The review is not where it belongs.** Framing a change is reviewing its description with its requester; the
   methodology splits it in two processes, and the delivery process, read alone, does not show that its framing is
   reviewed at all.
2. **Nothing says it is a review.** A reviewing step is an ordinary reactive step with three hand-written
   subscriptions (`change.created`, `change.edited` filtered on the title and the intent with `supersede`, a `comment`
   about the description). What it reviews, and that its verdict advises rather than decides, is in the comments.
3. **Moving it into the framing as is would rate twice.** A reactive step is still a step of its process (ADR 0110 §2):
   the delivery run, started while the reaction rates, would plan the step too and rate the description a second time.

## Decision

### 1. A step declares that it reviews: `review: {subject, automatic}`

```yaml
processes:
  - name: software_delivery
    steps:
      - name: framing
        steps:
          - name: review
            method: description_rating            # the reviewing method: an llm rating, a person as the fallback
            review: {subject: description, automatic: true}
          - name: confirmation                    # a person confirms the framing, in the light of the review
            pre: {description_rated: true}
            instructions: ...
      - name: analysis
        pre: {"step:software_delivery/framing/confirmation": true, description_accepted: true}
```

`Step.Review` (`methodology.Review`, `engine/methodology/review.go`; proto `Step.review`, message `StepReview`; attribute
`review` of `methodology@Step` and `methodology@MethodStep`, built-in meta-domain 1.13.0) names the **subject** the step
rates, one of the subjects of the assessments (ADR 0110 §6): `description`, `title`, `intent`, `impact:<node key>`,
`item:<record key or id>`. The compiler refuses another subject. The step is done the way any step is (an action, a
method, sub-steps); its verdict is an `assessment` item of that subject, and the conditions derive from it
(`description_rated`, `description_accepted`).

A review is part of the process it sits in: the delivery process now reads "frame the change: its description is
reviewed, then a person confirms the framing", and the flow, the current state of the change and the starting points
show it there. The view, the flow and the levels carry it (`StepView.Review`, `GraphStep.Review`, `LevelNode.Review`):
the web draws a *reviews description · automatic* chip on the step (`StepNode.svelte`) and edits it in the *Review*
section of the step editor (`StepEditor.svelte`, `reviewSubject` / `reviewAutomatic` of `StepForm`).

### 2. An automatic review starts by itself

`automatic: true` makes the step start without anyone launching it: it reacts to the events of its subject, as a
reactive step does (ADR 0110 §2, a companion run `companion:<methodology>/<step path>` with the agent of its process
towards the step), under the system principal acting for the end user of the event (ADR 0114), so it never waits for a
person to start it or to hold a role. For a subject of the header the subscriptions are generated
(`Review.Subscriptions`, `Step.ReactsTo`):

| Event | Filter | Supersede |
|---|---|---|
| `change.created` | | |
| `change.edited` | the fields of the subject (`title`, `intent`; both for `description`) | yes: the rating in flight is about a text that is gone |
| `change.item_added` | a `comment` about the subject | no: the remark waits for the rating in flight |

A step that declares its own `on:` reacts to those instead; a review of an impact or an item has no default events and
must declare them (the compiler says so). A manual step cannot be an automatic review: it would wait for a person.

### 3. The other runs of the process wait for it

An automatic review is carried out by its reaction, or by a run whose goal is the step itself or a step within it (a
person launching it from a starting point, `methodology.Carries`). Every other run of its process leaves the actions the
compiler generated for the step alone (`Compiled.AutomaticReview`, `carried` in `engine/engine/cycle.go`, applied to the
admissible actions and to the actions `awaited` counts): its exit is then established outside the run, so the run
**waits** for it (`TaskCondition` on `description_rated`) instead of rating a second time, and is tried again when the
change moves (the review's run completing, its assessment added). The delivery run of `sdlc` started on a new change
thus waits for the review, then goes to the person's confirmation.

### 4. A review advises; a person decides (ADR 0129)

The floor of ADR 0129 is unchanged and holds for every review: the reviewing actions write an assessment, an item of
the change, never a verdict on a change impact, so `checkOversight` has nothing to refuse and nothing to let through. An
automatic review runs a model without a person launching it, so the methodology puts a person after it: in `sdlc` the
framing is confirmed by a person (`framing/confirmation`, a manual step entered once the description is rated), who
reads the rating, its findings and its proposed wording (which only the person applies, ADR 0110 §4). The rating gates
the analysis (`description_accepted`) as before: a gate on the clarity of a text, not the acceptance of a model's
content; what the analysis produces is still accepted by people (`validate_impact_analysis`, the reviews of the
impacts). An edit of the description after the confirmation rates it again by itself and, the confirmation relying on
the review (ADR 0109 §5), asks the person to confirm again.

### 5. `sdlc` 0.9.0

The process `description_review` is gone; `software_delivery/framing` is made of `review` (the method
`description_rating`, automatic review of `description`) and `confirmation` (the instructions, guidance, checklist and
reference of the former manual framing). The analysis waits for `step:software_delivery/framing/confirmation` and
`description_accepted`. The method, its actions, the `assessments` library and the declared assessment states are
unchanged.

## Consequences

- A methodology says where it reviews, what, and whether the review starts by itself; nothing is hidden in a separate
  process, and the review appears in the flow and the progress of the process it belongs to.
- The pattern carries to other reviews (a design rated as it is written, an impact qualified when it is proposed) with
  one declaration and, for the subjects outside the header, the events that wake it.
- A starting point of the framing launched by a person waits for the automatic review too; to rate again on demand, a
  person launches the review step itself (its goal is the step) or answers in the bubble.
- Changes created under an earlier `sdlc` keep their version (ADR 0113) and its `description_review` process; an
  upgrade to 0.9.0 moves them to the framing review.
