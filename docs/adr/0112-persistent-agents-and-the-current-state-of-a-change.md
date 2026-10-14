# ADR 0112 — Persistent agents; the current state of a change and what its steps need

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0034 (processes), 0036 §3 (companion runs),
0050 (methods and their agent instances), 0059 (prompts in the log), 0097 (starting points), 0109 (conditions derived
from the context), 0110 (reactive activities)

## Context

1. **Continuity of an agent.** The review of the description of a change (ADR 0110) is run again at every edit and at
   every remark of the requester. The companion run of the reactive step is kept, but the step names a method
   (`description_rating`), and a method is applied by a **fresh** sub-agent at every run of its step (ADR 0050): each
   rating starts from nothing but the brief. The same holds for the continuous improvement of a methodology
   (`methodology-improvement`): its observer is started by a trigger for every completed run, so every run gets its own
   observer, its own change and its own review, while what is wanted is one improvement cycle that takes in the runs
   until it is over.
2. **The change tab shows runs, not the change.** Under the header it drew one progress card per root run of the
   change: a reactive process run five times, or a step relaunched, showed as many cards, each with its steps. What a
   person wants is where the change stands now, whatever the number of round trips.
3. **A step that cannot start says only `Needs: x`** (condition names), with nothing about what they mean.

## Decision

### 1. A persistent agent has one instance per scope

An agent (or a method, for the agent applying it) may declare `persistent`:

```yaml
methods:
  - name: description_rating
    persistent: {scope: change}          # one rater per change
agents:
  - name: observer
    persistent:
      scope: event.process.methodology   # one observer per observed methodology
      until: {drafted: true}             # the cycle is over once the draft is produced
      memory: 6                          # exchanges given back to the model (default 6, at most 20)
```

- `scope` is what one instance is for: `change` (the default) or a path into the event that starts the run
  (`event.<field>...`). A run whose scope resolves to nothing (no change yet, no event) is an ordinary run.
- The **instance** is a process (`Process.Instance` = `<methodology>/<agent>@<scope>`). Every new execution of that
  agent on that scope goes to it (`Engine.Start` → `resumeInstance`, serialized): one that has ended (completed, stuck,
  failed, or waiting for conditions) runs again **in place** with the new variables (the event), goal, parent and step,
  acting for whoever asks now (`Initiator`, its roles checked again); one at work keeps the event in its **inbox**
  (the mechanism of the companion runs, ADR 0036 §3), taken when it completes. A sub-agent call of another process
  while the instance is at work for its own parent gets an ordinary run (no two parents wait on one process).
  `Process.Runs` counts the executions after the first (companion reruns count too).
- `until` closes it: when a run completes with these conditions holding (`closeInstance`), the instance is `Closed`;
  the next execution on its scope starts a new one. Events left in the inbox of a closed instance start that next
  instance at once (`handOver`): on the same change for a change scope, from the latest baseline otherwise (as the
  trigger did). A cancelled instance, or one whose change is applied or abandoned, is closed too. Without `until`,
  an instance lives as long as its change.
- **Memory.** The llm actions of an instance get back its last exchanges with the model (`Process.Memory`, `Turn`:
  run, action, event, the prompt capped at 2000 bytes, the answer at 4000), put before the new prompt, with a note
  in the system text saying that they are its own earlier executions on the same scope and that it goes on from them
  (`memoryMessages`, `memoryNote`; recorded by `settleAction` from `ActionResult.Turn`). The brief still gives the
  change as it is now (ADR 0110 §5): the memory is the continuity of the conversation, not a source of truth. The full
  exchanges stay in the change log (ADR 0059).

`methodology.Persistence` (`Agent.Persistent`, `Method.Persistent`, copied onto the generated agent), checked by the
compiler (`checkPersistence`: the scope, the `until` conditions known, the memory bounds), proto `Agent.persistent` /
`Method.persistent` (`Persistence`), attribute `persistent` of `methodology@Agent` and `methodology@Method`, the
*Persistence* card of the agent and method tabs (`PersistenceEditor.svelte`, `PersistenceForm` in
`methodologyForm.ts`). Engine proto `Process.instance` / `closed` / `runs` / `memory_turns`.

Shipped: `methodology-improvement` 1.5.0, the observer
is persistent per observed methodology until `drafted`. Its conditions are about the run observed now
(`vars.event.process.id`, or `vars.process` for a run fired by hand: the `cost_report` and the `improvement_plan`,
which now carries `process`), the review and the draft about the whole cycle, so a new run handed to the instance is
analyzed and its proposals add up in the same change for one review.

### 2. The current state of a change

`Engine.ChangeState(change)` (RPC `GetChangeState`, read like the change: a personal change is its subject's only)
gives, for each process of the methodology of the change that no step nests:

- its steps **judged against the change as it is** (its conditions and its history, ADR 0109): the step tree of
  `Progress` computed over a run whose world is the change's and whose steps are those of every run of the process
  (so a step is done because its exit holds, not because some run once did it);
- then what the **live** runs (not terminal) are doing **laid over** it (`overlay`): a step not done that a live run is
  at takes its state (active, waiting with the kind of task, blocked), its sub-agents, the method chosen, its lanes;
- the state of the process as a whole (done when its goal holds, else the most advanced state of its steps), whether it
  is reactive, the number of executions, the run at work (else the latest) with its status and error.

The change tab shows it once (`ChangeState.svelte`, `web/src/lib/changeState.ts`: the processes at work first, each a
collapsible card with its progress bar, "N runs", *open run*, and the steps that cannot start above the tree); the
runs stay listed, folded, in the overview ("N runs, M at work"). The run tab keeps the progress of its own run.

### 3. What a step needs

`StepProgress.Needs` (proto `Need {condition, want, description}`) are the missing entry conditions with what each
means: the description the methodology gives its condition, else the library's (`condition.Definition.Description`,
now given for every library condition), else what a generated one stands for (`state:`, `step:`, `method:`). Filled by
`Progress` and `ChangeState`; the web reads them through `needText` / `needsOf` (falling back on the names).

### 4. A rating is an independent run; the discussion of a topic joins the runs

`sdlc` 0.7.2 made `description_rating` persistent per change. It froze the rating: given its earlier exchanges back,
the rater copied the fingerprint of the first description into its next ratings (`rated`), which were then never
`current`, so `description_reviewed` never held, the action failed and the screen kept the first rating. A rating
depends only on the subject as it is: it needs no memory, and memory made it wrong. So:

- `sdlc` 0.7.3: `description_rating` is no longer persistent; every edit gets an independent run.
- **What was rated is the engine's, not the model's**: the assessments a model answers are stamped with the
  fingerprint of their subject as the action read it (`stampRated`, `pkg/engine/executors.go`), whatever the model
  wrote; the prompt no longer asks for it.
- **The continuity is the discussion of a topic**, not a process: the subject of a rating (`description`,
  `impact:<key>`, ...) has a thread made of its ratings and the persons' comments, however many unrelated runs wrote
  them. The brief gives it to every run (`THREAD <topic>`, `pkg/brief`), now as soon as a topic was rated twice or
  commented, so the next rater goes on from what was said.

Persistence stays for work that is one piece over several runs (the improvement cycle, until its draft).

### 5. Derived conditions: what a condition cannot hold without

A derived condition may be a refinement of another: `description_accepted` (a current rating scoring 3 or more) cannot
hold without `description_rated` (a current rating), which the rating action produces through `description_reviewed`.
Reaching the second is a prerequisite of the first, whatever action states it. `condition.Set.Implied` reads it from
the expressions (`pkg/condition/implies.go`, `Implies`): an expression is a conjunction of atoms, and B implies A when
every atom of A is implied by one of B (the same term, or `R.exists(v, P)` implied by `R.exists(w, Q)` over the same
range when Q has every conjunct of P, the variables renamed; `==`, `&&`, `||` read as commutative). It is structural
and conservative: what it finds holds, what it misses is not shown. Two equivalent conditions are not each other's
prerequisite.

- `Compiled.Prerequisites(c)` and `Compiled.Producers(c)` (the actions with an effect that is c or implies it).
- `NeededBy` (starting points, ADR 0097) takes them in: a needed condition needs its prerequisites, and an action
  whose effect implies a needed condition works towards it.
- `Need.ProducedBy` and `Need.Requires` (the prerequisites not holding yet, each with its producers; proto
  `Need.produced_by` / `requires`): a step waiting for an accepted description says "first: the description has
  been rated (by rate_description)" (`needDetail`, `web/src/lib/changeState.ts`).

**The facts of a condition.** A condition is a CEL expression over the variables of the context, so what it really
asks of the change is known from it, and so is what fails. `condition.Set.Facts(name, bb)` (`pkg/condition/facts.go`)
reads it as its `&&` terms, each evaluated on the blackboard; a term `R.exists(v, P1 && ... && Pn)` is read part by
part, part i holding when an element of R meets P1 to Pi. `condition.Missing` is the first part (or term) that does not
hold, and `Set.Establishes(effect, fact)` says whether an effect condition asks for the same facts (an exists over the
same list with every part up to it). So, for `description_accepted`: before any rating, `a.key == "description"` is
missing and `rate_description` establishes it; once rated 2, `a.score >= 3` is missing and no action guarantees it.
`Need.Facts` (`Fact {text, holds, error, over, parts, missing, producedBy}`, proto `Fact`), filled from the blackboard
by `ChangeState` and `Progress`; the web shows each term with whether it holds and names the missing one
(`factLines`, `missingFact`, `needDetail`).

### 6. An action that missed its effects is not run again on its own

The engine ran an action that had not reached its effects a second time before disabling it (`MaxFailures` 2). For the
rater it meant a second model call nobody asked for, with the same brief, whenever a rating missed
`description_reviewed` (before §4, every rating whose fingerprint was copied wrong). The default is now one attempt
(`Engine.MaxFailures` 1, `maxFailures`): an action that ran and missed its effects is disabled for the run, and the
planner takes another way (the rating falls to `rate_description_by_hand`), else the run waits or is stuck, and a
person decides (`Unblock`: retry). A run started again because the change moved clears its disabled actions, as
before: it works on the change as it is then. A scoped action gives up a subject after one failure the same way.

## Consequences

- An observer or any agent declared persistent works as one colleague on its scope: it remembers what it said
  and goes on, and its runs are one process with a history instead of a pile of runs.
- The change tab says where the change is, not how many times something ran; the steps that wait say for what.
- Not done: the memory is not summarized (it is cut to the last exchanges); the web does not show the memory of an
  instance (the exchanges are in the change log, ADR 0059); no persistence on a process itself (its companion run is
  already kept).
