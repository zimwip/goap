# ADR 0050 — Methods own their actions, roles drive agents, the scheduler specializes

**Status**: accepted, phases 1 and `foreach` implemented (see Phasing) · **Date**: 2026-10 ·
Reshapes ADR 0035 §1 (methods), ADR 0034 (processes and steps) and the agent model of ADR 0009. Builds on ADR 0036 §2
(the brief of a change), ADR 0043 (project-scoped roles).

## Context

ADR 0035 made a method the documentary reference of how a step is done in a context, and made it name an **agent**,
the actor, whose action list gave the tools. Two things did not fit the mental model (who / why / how / what / with
what):

- *how* a thing is done (the method) did not own *with what* (its actions): they lived on an agent that the method
  merely pointed at, and the same agent could serve several methods;
- an agent was a long-lived, hand-declared actor, while the work it does varies with the context at every step.

## Decision

### 1. A method owns the actions that realize it

A method has **actions** (a pool the planner chooses among, any order) and/or its own **steps** (a tree). A method
step inherits the pool of its method (union; the actions of its steps are part of the pool). A method without steps
states what it reaches (`done`). It also carries the `planner`, `model` and `mcps` its agent needs: how to plan
depends on the work, not on the actor. A method no longer names an agent (`agent:` and `goal:` are gone).

### 2. The agent is an instance created for the step, acting as a role

The scheduler creates the **agent instance** for a step from (role, step, method): the agent has the role responsible
for the method (`Agent.Role`, from `roles.responsible`), which gives it its overall objective; the method says how.
Its goal is the method's goal (its steps' exit criteria, or `done`); its action pool is the method's. Today the agent
is generated per method, of the method's name; it is run as a sub-agent on the same change for each step execution
(`process.step`, ADR 0034), so it is a new instance per step.

An instance starts **fresh**: it has no conversation from earlier steps. Its system prompt (`StepContext.section`)
says its role and that what matters is on the change, to be read through the brief (`pkg/brief`,
`goap-change/brief`, `trace`), and that what it produces is recorded on the change as items. Anything only said in
chat is lost, which makes every step restartable (retry, unblock) and auditable (ADR 0011).

### 3. The scheduler picks the most specialized method

A process always starts the scheduling. For a step naming a capability, among the methods providing it whose context
(`when`) holds, the scheduler takes the highest **priority**, then the most **specific** context (the number of
conjuncts of `when`; none is the generic fallback), then declaration order. Priority is an explicit override of
specificity, kept first so that existing methodologies behave the same. It then descends: a method with steps is
walked step by step, with the same rule applied to the capabilities its steps name (a method step may not name a
capability for now, §Not yet).

### 4. `foreach` and `groupBy`: parallel streams

A step naming a capability may declare `foreach`, a CEL list over the blackboard (`vars.components`, `changeImpacts...`).
The step runs once per element, each in its own stream: the element is bound to `vars.item`, the most specific method
is chosen **for that element** (a method's `when` reads `vars.item`, e.g. `vars.item.lang == "java"`), and its own agent
instance runs as a sub-agent on the same change (call key `<step>#step:<element id>`; the id is the element if a string,
else its `key`, `id` or `name`, else its position). Every stream is started at once; the step waits for the first one
still at work and is retried when it ends; it fails if a stream failed and is done (a `step_done` artifact listing the
elements and methods) when all completed. Streams already started stay the streams of the step if the list changes,
and keep their method. The progress read model lists them as `lanes`.

`groupBy` (with `foreach`) is a CEL key evaluated per element (`vars.item.lang`): one stream per group instead of one
per element, whose `vars.item` is `{key, items}`, for what belongs together (all the Java components in one build).

The exit criteria of such a step are not the shared goal of the methods (they cannot be, they differ per element):
the step is done when its streams are, unless it declares `done`. What each stream reaches is written by the method's
`done`, with conditions that read `vars.item` (`artifacts.exists(a, a.type == "build" && a.data.component ==
vars.item.key)`), evaluated in the stream, whose variables hold its element.

### 5. Roles in a step

Only the **responsible** role gets an executing agent instance. The **accountable** role gets an approval task
(`step:approve`, already checked by the engine); consulted and informed roles are notified only.

## Consequences

- The `architect` / `designer` agents of `sdlc.yaml` are gone: their action pools and planner moved to the methods
  `solution_design` / `functional_design`, whose generated agents carry the names of the methods.
- `registry.v1.Method` drops `agent`/`goal` (field numbers reserved), gains `actions`, `done`, `planner`, `model`,
  `mcps`. The methodology editor's method tab has an actions pick list, the exit criteria, the planner and MCPs
  instead of an agent selector.
- A declared agent can still be named by a step (`agent:`) or run by a trigger; only methods stopped naming one.

## Phasing

| Phase | Content | State |
|---|---|---|
| 1 | model (§1), generated agent with role, planner, pool (§2), specificity ranking (§3), fresh-start prompt | done |
| 2 | `foreach` / `groupBy` on a step (§4): one parallel stream per element or group, each with the most specific method for it, joined when all complete; lanes in the run's progress | done (flow view of the *definition* does not draw lanes) |
| 2 | agents declared as role + binding (process or step path, no method), created from the role for steps whose RACI names it, with the extra instructions or planner of the declaration | not yet |
| 2 | A (accountable) as approval task for agent-run steps; a method step naming a capability (specialization below a method) | not yet |
| 3 | a more specific method that becomes valid during a step ends the instance at an action boundary and starts a new one (v1 ignores it: the method is fixed for a run) | not yet |
