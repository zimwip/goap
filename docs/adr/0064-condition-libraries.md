# ADR 0064 — Platform conditions become opt-in condition libraries

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0009 §4, 0036, 0058.

## Context

`condition.Platform`, fifteen conditions about decision points, options, risks and actions, was added to every compiled
methodology, whether it used decisions or risks or not (a methodology declaring the same name replaced it). The
engine thus added to the definition of a methodology what it had not asked for, and a methodology that knows nothing
of risks still carried `risks_under_control` and paid for the `risks` and `actions` variables on every evaluation. The
generic default of the subscriptions of a transverse methodology also hardcoded the risk register's event.

## Decision

1. **Libraries.** `pkg/condition` names two built-in libraries (`condition.LibraryNames`, `condition.Library(name)`):
   - `decisions`: `open_questions`, `no_open_questions`, `decision_ready`, `decision_pending`, `no_decision_pending`,
     `ratification_pending`, `decision_escalated`, `options_open`, `options_evaluated`, `option_selected`;
   - `risks`: `open_risks`, `unmitigated_risks`, `risks_under_control`, `open_actions`, `no_open_actions`. Its
     parameter is the score from which a live risk needs an action, `condition.HighRisk` (9), the one place the
     threshold is written (no longer in `pkg/domain`).
2. **`Methodology.Imports`** (`imports: [risks]`, protobuf `imports = 24`, the editor's "Condition libraries" field)
   names the libraries a methodology uses. `Validate` refuses an unknown or repeated name (issue at `imports[i]`).
   Compilation adds the conditions of the imported libraries only; a condition the methodology declares under the same
   name still wins. Without the import a library condition is an unknown condition.
3. **Lazy variables.** The CEL environment stays the same for every methodology and for the lifecycle guards
   (`CheckGuard`, ADR 0058, which read `decisionPoints`, `actions`, `risks` whatever the methodology): the variable
   set is not per methodology. `condition.Activation` binds `options`, `decisionPoints`, `questions`, `risks` and
   `actions` as memoized lazy values (`func() any`, called by CEL when the variable is read), so what no expression
   reads is never computed. An expression of a methodology may read them without importing a library; opt-in applies
   to the injected definitions and to the cost.
4. **Subscriptions.** The generic default of a transverse methodology without `on` is only `process.attached` and
   `step.completed`; the risk register's event (`change.item_added` filtered on risks and actions) is declared by the
   methodology that wants it (`risk-management.yaml` already did). A library does not contribute subscriptions.

No compatibility is kept: the shipped methodologies (`sdlc`, `risk-management` import `risks`, `option-decision`
imports `decisions`) and the test fixtures declare their imports.

## Consequences

A methodology says which vocabulary it uses; the engine adds nothing. New libraries are a name and a list of
definitions in `pkg/condition/library.go`.
