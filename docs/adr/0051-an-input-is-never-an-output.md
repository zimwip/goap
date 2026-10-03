# ADR 0051 — A condition is never both an input and an output of an activity

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0034 (processes and steps), ADR 0050.

## Context

Actions and steps were written with a guard: `pre: {functions_allocated: false}` with `effects: {functions_allocated:
true}`, so that the planner would not run them twice. The guard makes the same condition the input and the output of
the activity. In the flow every block then shows `¬x` on its left and `x` on its right, which says nothing about what
the block changes, and the steps seem to exist to sequence a flow.

## Decision

A step does not build a flow; it is a **transformation** of the common state of the change. Each activity (action or
step) takes the conditions it needs (`pre`) and makes others true or false (`effects`, `done`); the steps all work on
the same state towards the final goal, and the order comes from what each needs and gives. So:

- **The compiler refuses a condition that is both an input and an output of an action or of a step, whatever the
  values** (`actions[i].pre.x`, and for a step `<step>.pre.x` through its action or `<step>.done.x`). The issue is tied
  to the step it is about (`Issue.Activity`, ADR 0050's lenient compile), so the flow marks it.
- The planner does not need the guard: the A* search never repeats an action whose effects already hold (the state does
  not change), and the utility planner skips actions whose effects already hold (`utilityPlan`).
- The flow's level check (`GapNoop`) reports the same overlap on a step, or on the parent of a level, drawn red.

## Consequences

The shipped methodologies (`sdlc`, `risk-management`, `methodology-improvement`, the examples) lost the `x: false`
guards of their actions (63 of them). Conditions that were only ever used as such a guard stay valid as goals.
A condition an activity takes and a later activity changes still works: what it takes and what it gives are different
conditions.
