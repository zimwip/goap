# ADR 0116 — Declarative assessment states: the methodology says how a rating reads

**Status**: accepted, implemented · **Date**: 2026-10 · Extends ADR 0110 (assessments, reactive review of the description)

## Context

An assessment (ADR 0110) is a rating of a subject of a change: the description, the title, the intent, an impact, an
item. The web shows it as a bubble by its subject. Until now the web judged the bubble by itself: a score of
`condition.AcceptableScore` or more was "accepted", an impact was "processed" once its review was decided, and the
colours were two (pending, done). The methodology, though, already says when a rating is good enough: the
`description_accepted` condition is the gate of `sdlc`'s analysis. The two could disagree, and a methodology had no way
to say that a refuted impact is not a warning, or that a score of 1 calls for a rewrite.

## Decision

1. A methodology declares how the assessments of its changes read:

   ```yaml
   assessments:
     - subject: description            # a subject key, or a glob: impact:*, item:*, *
       states:                         # the first that holds wins
         - {when: "!assessment.current", tone: warn, label: "changed since its rating"}
         - {conditions: {description_accepted: true}, tone: ok, label: accepted}
         - {when: "assessment.score <= 1", tone: danger, label: "to be rewritten", open: true}
         - {tone: warn, label: "to be improved", open: true}
   ```

   A state holds when each of its `conditions` has the value given and its `when` is true; neither: it always holds.
   A scoped condition (ADR 0109 §3) is read on the subject (an impact by its node key, an item by its id, a risk or an
   action by its key). `when` is CEL over the variables of the conditions plus `assessment` (the entry of
   `assessments`), `subject` (`key`, `kind`, `id`, `entry`: the change, the `changeImpacts` or `items` entry it is
   about, null when there is none; `conditions`: the scoped conditions on it) and `conditions` (the conditions of the
   change). `tone` is one of `ok`, `warn`, `danger`, `info`, `neutral`; `open` makes the bubble open by itself while
   the state holds (a compact badge never opens by itself). By default a bubble opens only on a click: neither the
   defaults nor `sdlc` declare a state `open` (the person asked for it: an open bubble was in the way).
2. The compiler checks the subject pattern, the tone, the conditions named and the CEL (`compileAssessments`,
   `condition.CompileAssessmentViews`); issues are tied to `assessments[i].states[j]...`.
3. The server evaluates; the web renders. `Engine.AssessmentStates` (RPC `engine.v1.GetAssessmentStates`, read like
   the change) evaluates the conditions of the change, then, for each assessed subject, the first view whose pattern
   matches and one of whose states holds; when none does, the platform's defaults decide
   (`condition.DefaultAssessmentViews`, the former judgement of the web: an impact processed once its review is
   decided; any other subject accepted when current, answered and scored `AcceptableScore` or more). A change whose
   methodology declares nothing gets the defaults.
4. The change tab reads the states (again when the change moves) and gives them to its bubbles through a Svelte
   context (`ASSESSMENT_STATES`); `bubbleView` takes the state of the rating shown and falls back on the local
   judgement only while the states load or lag behind the rating.
5. `sdlc` 0.7.6 (0.8.1: no state `open`) declares its description (accepted is `description_accepted`, a score of 1 or less is "to be
   rewritten") and its impacts (processed once reviewed, a refuted one neutral, the others to be reviewed).

## Consequences

- The colour of a bubble means what the methodology means: the gate and the bubble can no longer disagree.
- The definition is graph data like the rest of the methodology (`assessments`, a JSON attribute of
  `methodology@MethodologyVersion`, proto `registry.v1.Methodology.assessments`); the methodology tab edits it as JSON.
- One more engine call per change view with assessments, throttled like the starting points.
