# ADR 0097 — Starting points: the possible steps towards the end point of the process

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0096 (the goal of a change), 0034 and 0035/0050
(processes, steps and methods sequenced by their conditions), 0090 (the assistant runs agents), 0091 (the project of a
change), 0092 (screen tools).

## Context

The goal of a change is the END point of its process (ADR 0096): it is what pulls the scheduler. Nothing told the person,
or the assistant, what can be started now on a change to get closer to it. The rule is the user's: a change proposes
only the steps that are POSSIBLE (all their conditions fulfilled), and a well-made methodology ensures the right
sequencing of its activities by the conditions of its steps. The scheduler does the selection work inside a step.

## Decision

### A starting point is a step, never an action

`Engine.StartingPoints(ctx, changeID, opts)` (`pkg/engine/startingpoints.go`) reads the change (methodology, goal,
status) and its blackboard, evaluates the world (`Conditions.Evaluate`, with the supertypes) and walks the processes of
the methodology that no step nests (a nested process is reached through the step that nests it):

- a step is looked at only if it works towards the goal: its exit criteria meet `Compiled.NeededBy(goal.Pre)`, the
  backward closure of the goal conditions over the actions (declared and generated for steps; a static
  over-approximation that ignores cost and alternatives, valid for any planner including llm ones);
- it is left out when its exit criteria already hold (done or skipped);
- when its ENTRY conditions do not all hold it is not proposed: it is counted as blocked (`blockedCount`, the first
  ones with what they miss, capped), and its sub-steps are not looked at (a container must be possible before what is
  inside it). The step's own "not done yet" guards are ignored, as in `Progress` (`missing` now works over a world);
- a step whose entry holds is proposed AS A WHOLE, the topmost possible one: a container step stands for its sub-steps,
  which the scheduler sequences. The actions behind a step (the generated action, the pool of a method) are never
  listed, whatever their own preconditions: a step whose actions are ready but whose own entry is unmet is not possible;
- a step naming a capability (`method:`) is proposed through the method that applies (priority, then the most specific
  `when`, `Method.Specificity`): a method composing steps proposes its possible steps (kind `method`, `parent` = the
  step naming the capability), a method with a pool only is proposed as one point (kind `method`); with no applicable
  method the step is blocked (`method:<capability>`); a `foreach` step is a plain step (the streams are the engine's);
- `running`: a live (non-terminal) process of the change already works towards the point, runs it for its parent, runs
  a step inside or around it, or runs the whole process (the planner sequences it): the point stays listed, flagged.

A change that is committed, applied or abandoned, or has no methodology or goal, or whose goal is already reached,
returns no point and a `reason` (not an error).

### Starting = handing the selection to the scheduler

Each point carries its `launch` `{methodology, agent, goal, changeId}`, the fields of `StartProcess`:

- a step of a process: the agent generated for the process, the goal is the step path (`Compiled.Goal` resolves
  `<process-or-method>/<step>/...` to a goal whose `Pre` is the exit criteria of the step), so the planner of the agent
  selects and sequences the actions that reach the exit of that step alone, from the current world;
- a step of a method: the agent generated for the method, the goal the step path;
- a method applied to a step naming a capability: the agent and goal of the method (what `process.step` starts).

`Engine.Start` gives such a run the context of the step (`StepContext`: guidance, checklist, roles) when the goal is a
step path. Nothing selects an action before: not the UI, not the assistant, not the RPC.

### API

- RPC `engine.v1.EngineService.ListStartingPoints {change_id, methodology?, project_id?}`: authorized like
  `StartProcess` (the `start` permission on the methodology, on the project = the request's, else the change's, else the
  principal's, ADR 0091), the change is read as the caller (a personal change of another subject is not found).
  Per point `may_run` is the rule `Start` applies to its initiator (`Engine.MayRunAgent` on the launch agent, ADR 0090):
  points the caller may not start are returned with `may_run=false` and `need_roles` (the UI shows them disabled), not
  filtered; the list is capped at 20, the blocked list at 8.
- `goap-scheduler/starting_points {changeId?, methodology?}` (read-only, agent scope): the same answer for the calling
  change.

### The assistant

`list_agents` with a change in context calls the engine once (`assistantsvc.Engine.StartingPoints`) and returns, in the
same call, `possibleSteps` (id, kind, name, description, why, produces, mayRun, running; capped), `waitingSteps` (a
count and what they wait for) and a `stepsNote` when none is possible; agents get `ready` when one of their steps is
possible. On a change `start_agent` takes `step` (the id of a possible step) and nothing else is accepted: an unknown
step, a step that waits, one already carried out, one the caller may not start, or a call without `step` is refused with
the reason and the possible ids. The proposal records `{methodology, agent (the launch agent), goal (the launch goal),
step, changeId, project}`; `ConfirmAction` re-validates (the step must still be possible, `ErrStale`). The prompt says:
propose only the listed possible steps, say why they are possible, and when none is possible say what is awaited. A
round that only proposes still settles (no extra model round).

### Web

`ChangeTab` shows "Possible next steps" (`PossibleSteps.svelte`, `web/src/lib/startingPoints.ts`) on the overview of a
draft or active change with a methodology and a goal: description, why it is possible, what it produces, who can run it,
a Start button (confirmation naming what will start and the goal, then `StartProcess` with the launch) and a collapsed
"N more steps wait for: ..." line for the blocked ones (no buttons). It is read when the change stamp moves, throttled
to once per second, never polled. The screen exposes the points as `step` entities and the write tool `start_step`
(enum of the startable ids; the proposal card is the confirmation, the same function as the button).

### Methodology quality hint

A well-made methodology can start on an empty change. `Compiled.Hints()` (`pkg/methodology/hints.go`) returns a
non-blocking remark tied to the path `goal` when, on an empty blackboard, no step (no action when there is no process)
working towards the main goal has its entry satisfied, with the first conditions it waits for. It is not a compile
issue: nothing is refused, the shipped methodologies and examples give no hint (`TestShippedMethodologiesHaveNoHint`).

## Not done

- The hint is not yet shown by the methodology editor (no warning severity in `def.Issue`; the function is there).
- A declared agent without steps (a plain `goals`/`actions` methodology) has no step to propose; its actions are not
  listed either, so such a methodology shows "no step of the methodology works towards the goal".
- Starting several points at once; ordering points by value.
