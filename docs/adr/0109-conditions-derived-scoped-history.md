# ADR 0109 — Conditions are derived from the context, scoped to a subject, and read the history of the change

**Status**: accepted (rev. 3); steps (a) (the history, `current`, the default `done` of a step), (b) (removal of ADR 0107, of the waiver, `change_bound` an ordinary condition), (c) (scoped conditions and aggregates) and (d) (scoped actions) implemented, (e) (web) to come · **Date**: 2026-10 · Builds on ADR 0002 (conditions in CEL), 0034 /
0035 (processes, steps, `foreach`), 0051 (an input is never an output), 0064 (condition libraries), 0097 (starting
points), 0102 / 0103 (rebase of a change, sub-changes); **supersedes ADR 0107** (effects on impacts and items) and the
`waive` decision of ADR 0036 §3 (`Engine.Unblock`); touches ADR 0011 / 0030 (journal in the change log)

Rev. 2 follows the review of rev. 1: the marks of ADR 0107 are removed, nothing overrides a derived condition (a past
state is a replay of the log), a change sees only its own history, and a step's success is invalidated in cascade by
what happened after it (§5).

## Context

### The principle

An activity (an action, a step, a sub-change, a person) transforms its **context**: the graph as the change sees it
(baseline + drafts) and the change itself (header, impacts, reviews, items, facets), and leaves a trace in its log.
Applying an activity changes the context, and so the conditions. Hence:

- **a condition is always derived from the context**: a pure function of it, never stored, never asserted, never
  overridden;
- **an effect is declared, never derived nor written**: it is what an activity *promises* to make true; the planner
  uses it to simulate, the engine checks it afterwards against the derived world, nothing writes it;
- **the state of the world at any instant is a replay**: the change log is the one order of what happened (ADR 0030);
  the context, and so the world, just before an action is the fold of the log up to that entry. Nothing needs to keep
  a second copy of the world.

For every action to make sense in the context, two things are missing: conditions about **one subject** (an impact, an
item) **aggregated** at the level of the change, and the **history** of what was done, with its outcome, as part of the
context.

### What the code does today

| Point | Today | Where |
|---|---|---|
| Conditions derived | Yes: CEL over the blackboard, re-evaluated at each observation (`Engine.observe` → `Set.Evaluate`), the world `p.World` is that result. | `pkg/condition/condition.go`, `pkg/engine/engine.go` `observe` |
| Effects declared, checked | Yes: the planner simulates with `Action.Effects`; `finishStep` re-observes and sets `Step.EffectsMet`; failures count and disable the action. | `pkg/engine/engine.go` `finishStep`, `recordFailure` |
| Values not derived | `change_bound` is injected by the engine; a **waiver** overrides the world (`risk.Waivers` → `res.State[k] = v`). | `observe`, `pkg/engine/unblock.go` |
| Data written to stand for "it ran" | A step without `done` gets `step:<path>` = "a `step_done` artifact exists", written by the engine when the step ends; by hand, `artifacts.exists(a, a.type == "design")` means "the design action ran". | `pkg/methodology/process.go`, `pkg/engine/steps.go`, `methodologies/sdlc.yaml` |
| Facts set on subjects | ADR 0107: an `effect` item sets named values on an impact or an item; written facts that look like effects. | `domain.KindEffect`, `Change.Effects()` |
| Scope | Only the change: per-subject reasoning is inline `filter(...).all(...)`, repeated (`sdlc`), with the vacuous truth patched by hand (the `NB` of `sdlc.yaml`). Lifecycle guards have their own subject scope (`node`, `impact`, `draft`). | `methodologies/sdlc.yaml`, `pkg/guard` |
| History | The journal (`journal.*` entries: runs, actions with `effectsMet` / `error`, `Reads`) is in the change log but **not on the blackboard**; the `step_done` artifact says neither the outcome nor what the step started from. A rebase records a `rebased` event per impact, which no condition can relate to the steps done before it. | `pkg/journal`, `pkg/graph/changelog.go`, `pkg/graph/rebase.go` |

## Decision

### 1. The context

The context of a condition is `graph + change + history`, all of the **change itself**:

- the **change**: header, impacts (draft, reviews), items, facets (options, decision points, dependencies): unchanged;
- the **graph** as the change's branch has it: the hydrated nodes of the impacts (unchanged, ADR 0002);
- the **history** (§4): the runs and steps done on the change, with their outcome and their place in the log.

A change sees only its own context: a sub-change does not read its parent's history nor its drafts (as for the graph,
ADR 0103). It is autonomous until it synchronises (a rebase onto its parent) or merges; what the synchronisation
changes then invalidates its conditions (§5).

A condition is a pure function of that context. Consequently:

- `change_bound` becomes an ordinary condition of the methodologies that use it (`change.id != ""`);
- **no override**: the waiver of `Engine.Unblock` is removed (§6), the world is what the context derives;
- no engine code writes data so that a condition becomes true: the `step_done` artifact is replaced by the history;
- the state before an action is **a replay**: `Graph.BlackboardAt(change, seq)` folds the log up to an entry, and the
  journal's `Before` / `After` worlds become a cache for display, recomputable at any time.

### 2. Effects are declarations

An action, a step, a method declares `effects:` (unchanged syntax), used for three things only: **simulation** (the
planners, `StartingPoints`), **verification** (after the run the derived world must satisfy them, `EffectsMet`, else
the run failed) and **explanation**. An effect is never written into the world nor into the change. ADR 0051 holds.

**ADR 0107 is withdrawn.** Its `effect` items were written facts dressed as effects: the item kind
(`domain.KindEffect`, `domain.EffectMark`), the fold (`Change.Effects()`, `domain.EffectIndex`), the `effects` field
of the CEL entries, the brief's `effects=`, `goap-change/effect` and the PROV `goap:qualifies` mapping are removed.
What they were meant to say ("REQ-9 is formulated") is derived instead: from the subject itself (its draft, its
review), or from the history (the step that worked on it succeeded, and nothing changed since: §3.5, §5).

### 3. Scoped conditions

A condition has a **scope**, the kind of subject it is about; `change` is the default and what exists today.

| Scope | Subjects | `self` is |
|---|---|---|
| `change` | the change | (no `self`; today's variables) |
| `impact` | the impacts of the change | an entry of `changeImpacts` |
| `item` | the items of the change, filtered by `kind` / `type` | an entry of `items` |
| `risk`, `action` | the keyed records of the risk register (ADR 0036) | an entry of `risks` / `actions` |
| `step` | the steps of the history (§4) | an entry of `history.steps` |

```yaml
conditions:
  - name: impact_accepted
    scope: impact
    expr: self.review == "accepted"
  - name: requirement_traced
    scope: impact
    where: '"alm@Requirement" in self.types && self.intent == "created"'   # the subjects it is about
    expr: self.hasPost && self.post.out.exists(l, l.type in ["alm@satisfies", "alm@refines"])

  - name: change_impact_accepted
    all: impact_accepted            # every subject of the scope (within its `where`) satisfies it
  - name: requirements_traced
    all: requirement_traced
    empty: true                     # value on an empty set; default false (no vacuous truth)
```

1. **Evaluation.** A scoped condition is evaluated per subject of its scope satisfying `where` (absent: every
   subject); a subject outside `where` has no value. Result: a table per scope, `subject → {condition → bool}`, plus
   the errors per subject (unknown, as today).
2. **Reading.** Every entry of a scope carries `conditions` (`changeImpacts.filter(i, !i.conditions.impact_accepted)`);
   a scoped condition may read the scoped conditions of its own subject (`self.conditions.x`), the compiler refusing a
   cycle.
3. **Aggregates.** `all:` / `any:` / `none:` over one scoped condition (+ `where:`, `empty:`) declare a change-level
   condition. The compiler records the link, so the explanation, the Progress and the UI list **the subjects that do
   not hold**. On an empty set `all` is `false` unless `empty: true`, `any` is `false`, `none` is `true`.
4. **The planner's world** is the change level only (declared change-scope conditions and aggregates): `pkg/goap` is
   unchanged. The per-subject tables live beside it (`Process.Subjects`).
5. **Scoped actions.** An action (or a step) may declare `scope: impact` (+ `where`): its `pre` and `effects` name
   conditions of that scope, and it runs **once per pending subject** (its `pre` holds, its `effects` do not). The
   compiler lifts it for the planner: lifted pre = "some subject is pending" (generated `pending:<action>`), lifted
   effects = the `all:` aggregates of its effects (to be declared, else a compile issue suggests one). The engine runs
   it per pending subject in parallel lanes, as a `foreach` step (ADR 0035, `vars.item` = the subject), and checks
   the effects per subject. The history records the subject of each run (§4), so a per-subject condition can read
   "the step that formulates it succeeded on it" where ADR 0107 wrote a mark.
6. **Guards** of a node lifecycle (`pkg/guard`) see `impact.conditions`: gates, guardians and conditions share one
   vocabulary.

### 4. The history is part of the context

A pure fold of the change log, `journal.History(entries)`, gives a facet of the blackboard, `domain.FacetHistory`
(filled through `Graph.Facets`, like the dependencies of ADR 0108; `pkg/graph` and `pkg/domain` still name no journal),
read lazily by CEL:

```text
history.runs:  [{process, methodology, agent, goal, parent, status, startedAt, endedAt, seq}]
history.steps: [{path, process, action, subject, outcome: succeeded|failed|skipped|running,
                 effectsMet, error, seq, inputsSeq, current, runs}]     # one entry per step path, subject and run
history.last:  {<step path>: <its latest entry>}                       # the shortcut most conditions want
```

1. **Outcome**, derived from the journal: `succeeded` = the action of the step ended with its effects met (a
   `process.step`: its sub-run `completed`); `failed` = an error or effects not met; `skipped` = its exit held when it
   was reached; `running` = not ended.
2. **`seq`** is the log position of the step's end; **`inputsSeq`** the position of the latest write, before the step
   started, to what it read (the journal's `Reads`, the impacts and items in its scope; for a scoped run, its subject).
   Every impact and item also carries `seq`, the position of its last event (`changeImpacts[i].seq`, `items[i].seq`).
3. **`current`** says the success still stands (§5).
4. **Scope `step`**: `scope: step` conditions are evaluated per entry of `history.steps` and aggregated like the
   others (`any: step_failed_twice`).
5. **Cost**: folded once per observation from the log already read; a methodology that never reads `history` pays
   nothing (lazy binding, ADR 0064 §3).

### 5. Invalidation in cascade

A success is not a permanent fact: it was obtained on a given context. It **stands** (`current`) while two things hold:

1. **its inputs did not move since**: no event touched, after the step started, what it read (an edit, a review sent
   back to `proposed`, a `rebased` event of a synchronisation with the parent, ADR 0103, a merge of a sub-change into
   this one, a cancel or withdraw);
2. **the successes it relied on still stand and are older**: every step whose exit is in its entry (its predecessors,
   which the compiler knows from the conditions) has a current success with a `seq` **before** its own.

Rule 2 is what makes the invalidation cascade with nothing to cancel: when a rebase moves the inputs of `collect`,
`collect` is no longer current (rule 1); `analyse`, which relied on `collect`, is no longer current either (rule 2);
when `collect` runs again its new success comes **after** `analyse`'s, so `analyse` stays not current until it runs
again too, and so on down the chain. Each condition only looks at the one before it.

- **Default `done` of a step** (a step without `done:`) becomes `history.last["<path>"].current`; a step with its own
  `done:` keeps it (derived from the content: if the rebase changed the content, the condition changes by itself).
- **The planner** sees the step's exit false again and re-plans: the steps to redo are exactly those whose success no
  longer stands, in the order of their dependencies.
- **Sub-changes**: a rebase of a sub-change onto its parent (`rebaseCommittedTx`, `RebaseChange`) writes `rebased`
  events in the sub-change's log; the conditions of the sub-change that depended on what moved fall, and only those.
  A clean rebase that moved nothing in a step's inputs invalidates nothing.
- **Explanation**: why a success no longer stands is part of the step's entry (`staleBy`: the log entry that moved an
  input, or the predecessor that ran again), shown by the Progress and `Set.Explain`.

### 6. Unblocking a run

With no override, `Engine.Unblock` keeps `retry` and `abandon`; `waive` is removed (the `waiver` item kind of
`pkg/risk`, `risk.Waivers`). A person who wants a stuck run to go on changes the context: a manual step or action whose
effects are the missing conditions (its success, recorded in the history, is what the conditions read), or an edit of
the change. The record of who unblocked and why is in the log like any other activity.

### 7. Writing conditions

- "The action producing X ran" is the history (`history.last["design"].current`), not `artifacts.exists(type == X)`;
  an artifact condition stays when the content of the artifact matters.
- The repeated `filter(...).all(...)` become a scoped condition and an aggregate.
- The compiler warns (with a hint, `CompositionIssues`): an `all:` without `empty:` where nothing else anchors it, a
  scoped action whose effects have no declared aggregate.

## Consequences

- A condition answers one question: "what does the context say now?". The world before any past action is a replay of
  the log, so nothing keeps a world of its own.
- Per-subject work is first-class (one scoped action, run per pending subject, its aggregate driving the plan).
- A rebase or any later edit re-opens exactly the steps whose inputs moved, and the ones after them, with no rule to
  write and nothing to cancel.
- `sdlc`, `release-based`, `risk-management`, `methodology-improvement` and the examples are rewritten (aggregates,
  history), no compatibility kept (as for ADR 0064).

## Gaps with the code

1. `pkg/condition`: `Definition.Scope / Where / All / Any / None / Empty`; evaluation per scope (`Result.Subjects`);
   `conditions` and `seq` on the scoped entries; `history` (lazy); `Explain` of an aggregate and of a stale success;
   `CheckGuard` binds `impact.conditions`; `effects` removed from the entries.
2. `pkg/methodology`: YAML / proto / `methodology@Condition` attributes; compile checks (scope known, `self` only in a
   scoped condition, no cycle, aggregate of the same scope); scoped actions and steps lifted to `pending:<action>` +
   aggregates; the predecessors of each step (from entry / exit); default `done` from the history; `ArtifactStepDone`
   removed.
3. `pkg/journal`: `History(entries)` (outcomes, `seq`, `inputsSeq`, `current`, `staleBy`), the facet provider; the
   action record carries the subject of a scoped run. `pkg/graph`: `FacetHistory` through `Graph.Facets`,
   `BlackboardAt(change, seq)`.
4. `pkg/engine`: `observe` without `change_bound` nor waivers; `Process.Subjects`; scoped actions per pending subject
   (the `foreach` lanes), `EffectsMet` per subject; `RunStep` / `foreach` stop writing `step_done`; `Unblock` without
   `waive`; `Progress` / `StartingPoints` read the failing subjects and the stale steps.
5. Removal of ADR 0107 (`pkg/domain` effect kind and index, `pkg/brief`, `goap-change/effect`, `pkg/prov`) and of the
   `waiver` kind (`pkg/risk`, web Unblock dialog).
6. Web: conditions editor (scope, where, aggregate), per-subject column on the change's impacts, explanation of an
   aggregate and of a stale step.
7. Methodologies and tests rewritten; `docs/architecture.md` and `CLAUDE.md` updated.

Suggested order: (a) history facet, `current` and the default `done` from it (removes `step_done`); (b) removal of
ADR 0107 and of the waiver; (c) scoped conditions and aggregates; (d) scoped actions; (e) web.

## Step (a), as implemented

- `journal.KindStepEnded` (`journal.step.ended`): `Engine.finishStep` records the run of a step of a process that ended
  without error (`ActivityRef` the step path, `data.execution` its run, `data.children` its sub-agents) **before** it
  observes the change again, so that observation's conditions see it; the action record follows with the effects
  found. A manual step's submission is its run (`Step.Execution` = the record of the submission).
- `journal.Fold(entries, relies)` (`pkg/journal/history.go`, read types `journal.HistoryTypes`) is the history:
  `Runs`, `Steps` (`Outcome` succeeded / failed / running; `skipped`, `waived` and the `subject` of a scoped run come
  with the later steps), `Current`, `StaleBy`, `Last()`. A write that makes a success stale is an impact event that moves
  content (`updated`, `transitioned`, `cancelled`, `withdrawn`, `rebased`) or a new version of a keyed item, on a subject
  born before the run ended, made after it, by anything but the run itself (its execution, its sub-agents) or a step
  relying on it. `relies` comes from `methodology.Compiled.StepDependencies()` (transitive: a step relies on a step
  whose exit gives a condition of its entry).
- The graph gives it with the blackboard: `Graph.LogFacets` (`graph.LogFacet{Types, Fold}`: the entries of those types
  are read in the transaction of `BlackboardIn` and of the merge's blackboard, the fold runs after it, since it may ask
  the registry), plugged by `graphsvc.LogFacets(deps)` as `domain.FacetHistory` (`journal.FoldWith`): `goap-dev` with
  the step dependencies of the registry's compiled methodologies, `cmd/graph` with none (no registry: a success stands
  while nothing it started from moved, rule 2 is not applied there). So the lifecycle guards, the gates, the guardians
  and the merge read it (`history` in the guard environment, `guard.Facts.History`), and the world the registry
  evaluates for them holds the `step:<path>` conditions. The engine folds it itself when the blackboard comes without it
  (a graph reached over RPC carries no facet): `Engine.withHistory` / `Engine.blackboard` (`observe`, the explanation
  of a condition, `StartingPoints`); CEL `history` (`pkg/condition`).
- The generated `step:<path>` is `"<path>" in history.last && history.last["<path>"].current`; the `step_done` artifact
  (`methodology.ArtifactStepDone`) is gone.

## Step (b), as implemented

- ADR 0107 is removed: no item kind `effect` (`domain.KindEffect`, `EffectMark`, `Change.Effects()` / `EffectIndex`), no
  `effects` on the entries of `changeImpacts`, `items`, `risks`, `actions`, nothing in the brief, no `goap-change/effect`
  tool, no `goap:qualifies` in the PROV-O export. Effect items already in a log are ordinary items of an unknown kind.
- The waiver is removed: no item kind `waiver` (`risk.KindWaiver`, `risk.Waivers`), no override of the world in
  `Engine.observe`, no `waive` decision in `Engine.Unblock` (retry, abandon), no `waived` in the explanation of a
  condition; the proto fields `UnblockProcessRequest.conditions` and `ExplainConditionResponse.waived` / `note` are
  gone (the protos keep no reserved fields: clients and servers ship together). The web's Unblock panel lists what the run waits for and offers *Retry* once the change establishes it.
- `change_bound` is no longer injected: a methodology that defers its bind declares it (`change.id != ""`,
  `methodologies/examples/deferred-bind.yaml`); `StartingPoints` and `Compiled.Hints` evaluate the conditions on an
  existing change and set nothing either.

## Step (c), as implemented

- `condition.Definition` gains `Scope`, `Where`, `All` / `Any` / `None` and `Empty` (`pkg/condition/scope.go`; scopes
  `change` (default), `impact`, `item`, `risk`, `action`, `step`, `condition.Scopes`). `CheckDefinition` checks one
  definition alone (known scope, `self` only in a scoped condition or a where, an aggregate names one condition and no
  expression, a plain condition no where); `Set.Compile` orders the scoped conditions so that one comes after those it
  reads on its subject (`self.conditions.x`, `SelfReads`), refusing a cycle, a read of another scope and an aggregate
  of an unknown or unscoped condition.
- Evaluation (`Set.Evaluate`): the scoped conditions first, per subject of their scope within their where
  (`Result.Subjects`: `condition.Subject{ID, Values, Errors}`, the id is the key of an impact, the id of an item, the
  key of a risk or action record, `<path>#<runs>` of a step); every entry of a scope then carries `conditions`, so the
  conditions of the change read them (`changeImpacts.filter(i, !i.conditions.x)`); then the conditions of the change,
  then the aggregates, unknown when a counted subject has an error. `all` on an empty set is `empty` (default false),
  `any` false, `none` true. `Explain` of an aggregate lists the subjects counted and the failing ones, of a scoped
  condition its value per subject.
- Methodologies: `methodology.Condition` carries the same fields (YAML, proto `Condition` fields 4 to 9, attributes of
  `methodology@Condition`); the compiler refuses a scoped condition in a goal, in a step's entry or exit (name an
  aggregate of it) and in an unscoped action. The planner's world is the change level only: `pkg/goap` is unchanged.
- Not done: `impact.conditions` in the guard environment (§3.6), the failing subjects in `Progress` / `StartingPoints`.

## Step (d), as implemented

- An action declares `scope` (+ `where`, proto `Action` fields 26 / 27, attributes of `methodology@Action`); its `pre`
  and `effects` may name conditions of that scope (and of the change). `compileState.compileScope` lifts it: the
  generated scoped condition `subject:<action>` (its scoped pre holds, its scoped effects do not, within its where) and
  the condition of the change `pending:<action>` (`any:` of it); lifted pre = its pre of the change plus
  `pending:<action>`; lifted effects = its effects of the change plus every `all:` (`none:` for an effect false)
  aggregate of each scoped effect over its subjects or a part of them (the aggregate's where is the action's, or that
  where narrowed by `&& ...`), else over every subject; none: a compile issue suggesting one
  (`Action.ScopedPre` / `ScopedEffects` / `Lifted`, compiled only).
- A step may narrow the subjects of the scoped action it runs (`Step.where`, proto `Step.where`, attribute of
  `methodology@Step` / `MethodStep`): its own where is the action's `&&` the step's, its generated conditions are named
  after the step (`Action.Narrowed`), it lifts to the aggregates over its subjects, and it states its exit criteria.
  So several steps run one action, each over its part: the passes of an analysis, layer by layer.
- The engine (`pkg/engine/scoped.go`, `cycle.go`) runs a scoped action for one pending subject per step, the first
  in the order of its scope, giving it `vars.item` (the entry) and `vars.subject` (its id); the step records `Subject`
  and `SubjectRef` (the impact id or `<kind>:<key>`, also on the action record of the journal and on
  `journal.step.ended`). Its effects are met when its scoped effects hold on the subject (a subject gone, an impact
  withdrawn, has nothing left to do) and its effects of the change hold; the step of a process is done only once the
  lifted effects hold. A subject failing `maxFailures` times is given up (the others go on); with no pending subject
  left the action is disabled for the run. The step limit of a process counts the runs of a scoped action once.
- The history (`journal.Fold`) keeps the runs of a scoped step per subject (a run for another subject does not
  replace it) and the staleness of such a run is about its subject only.
- The runs are sequential, one subject per step: the parallel lanes of `foreach` are not used.
- `sdlc` 0.7.0 is the first user: the impact analysis method in four passes (requirements, functions, applications,
  components), each one `identify_<layer>` (llm), `propagate_<layer>` (`graph.propagate` with a `layer`) and the one
  scoped action `qualify_impact` (one LLM call per suspected impact: an assessment `impact:<key>`, verdict `confirmed`
  or `refuted`; a refuted impact is rejected with its reason, then withdrawn) narrowed per layer, then
  `validate_impact_analysis` (human).

## The inputs of a step

Rule 1 of §5 needs the inputs of a step. The journal's `Reads` (the node versions it read) is precise but misses what
an LLM step read through a tool, so: the inputs of a step without scope are every impact and item of the change that
existed when it started, the subject alone for a scoped step; and a write by a step that **relies on it** (a
successor, transitively) does not count, since it builds on it (else the design editing a requirement would re-open
the specification, in a loop). What counts is everything else: a rebase, a merge of a sub-change, a person's edit, a
step that is not after it.
