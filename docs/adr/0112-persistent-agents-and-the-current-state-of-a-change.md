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

Shipped: `sdlc` 0.7.2, `description_rating` is persistent per change; `methodology-improvement` 1.5.0, the observer
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

## Consequences

- A rater, an observer or any agent declared persistent works as one colleague on its scope: it remembers what it said
  and goes on, and its runs are one process with a history instead of a pile of runs.
- The change tab says where the change is, not how many times something ran; the steps that wait say for what.
- Not done: the memory is not summarized (it is cut to the last exchanges); the web does not show the memory of an
  instance (the exchanges are in the change log, ADR 0059); no persistence on a process itself (its companion run is
  already kept).
