# ADR 0126 — Formal execution elements between the methodology and the engine

**Status**: accepted; step 1 implemented (the call of a step, the owner of a step and of a reaction, the descriptions
of the generated conditions), step 2 implemented (`Compiled` the activable form, the planner conversion in the engine),
step 3 implemented (the compiled view, bound by the engine), step 4 implemented (the change screen
draws the view, the engine answers the binding) · **Date**: 2026-10 · Builds on
ADR 0034 (processes), ADR 0035 / 0050 (methods), ADR 0062 (builtins), ADR 0109 (conditions), ADR 0125 (components)

## Context

The engine executes, the methodology describes. The compiler turns a methodology into what the planners plan
(agents, goals, actions with their pre and effects), but several things the engine needs were not stated, only
encoded, and the engine decoded them:

- a step done by a nested process or by a method compiled to a `builtin` action named `process.step` whose
  `Params` map carried `step`, `agent`, `goal`, `methodology`, `capability`, `foreach` and `groupBy`; the builtin
  table of the engine held an entry for it and the compiler a special case to let it through;
- the process or method a step belongs to was the text before the first `/` of its path
  (`strings.Cut(path, "/")` in `steps.go`, `companion.go`, `startingpoints.go`), and a reaction's process the same;
- what a generated condition means was read from its name: `state:`, `step:` and `method:` prefixes parsed by
  `changestate.go`, and a step with no applicable method reported as missing a made-up `method:<capability>`.

A rename in the compiler broke the engine without a compile error, and the engine knew the naming scheme of the
methodology.

## Decision

The methodology states, as typed elements, everything the engine executes; those elements are generic (they say
what to run, not which methodology concept produced it). The engine reads them and decodes no name.

1. **A call is an execution element.** `methodology.Call {Methodology, Agent, Goal, Capability, Foreach, GroupBy}` on
   `Action.Call`, with the kind `call` (`methodology.KindCall`): run a sub-agent on the same change and wait for it,
   the agent being named or chosen at run time among the methods providing the capability. The compiler generates it
   for the steps done by a nested process or a method; a methodology does not declare it (the compiler refuses
   `kind: call`). Calling a sub-agent is a primitive of the engine (`RunCall`), not an executor: `Engine.Executors`
   has no entry for it, and the builtin `process.step` is gone (`contracts/builtins`).
2. **A step knows its owner.** `StepInfo.Owner` is the process or method whose step tree holds it,
   `Reaction.Owner` the process or method a reactive activity is or belongs to; `methodology.StepWithin` says
   whether a step is inside another. The engine reads them instead of cutting paths.
3. **A generated condition carries its meaning.** The compiler gives every condition it generates a description
   (steps, lifecycle states, method contexts, specializations, expectations, the subjects of scoped actions); the
   methodology's own conditions keep theirs; `condition.Set.Description` and `Compiled.ConditionDescription` answer
   for all of them. A step whose capability has no applicable method reports the context conditions of the methods
   providing it (`Compiled.CapabilityGuards`).
4. `platform/layering` `TestEngineDecodesNoConvention` keeps the engine from naming the prefixes of the generated
   conditions, the parameters of the former builtin or `process.step`.

## Step 2: three stages

The methodology is the **description**; `methodology.Compiled` is the **activable** form the compiler builds from it;
the **engine executes** the compiled form, feeding its planners and orchestrator with it and binding the context of
the change.

5. `Compiled` holds the definition but no longer embeds it: `Compiled.Definition()` gives it to whoever works on the
   description (the registry, the editors, the assistant's queries, the tests), `WithDefinition` builds a variant in a
   test. The engine reads the accessors of the activable form only: `Name`, `Version`, `Description`, `Namespace`,
   `Administrative`, `Criticality`, `MainGoal`, `AppliesTo`, `Subscriptions`, `Supertypes`, `Processes` /
   `Process` (`ProcessInfo {Name, Description, Nested}`, the nesting computed by the compiler), besides the agents,
   goals, actions, steps, reactions and conditions it already had.
6. The compiler knows no planner: `Compiled.PlannedActions` / `AgentActions` return `methodology.Action`s, and the
   engine turns them into planner operators (`operator`, `operators`, `planningGoal` in `engine/engine/planners.go`).
   `engine/methodology` and `engine/condition` import no `engine/goap` (`condition.Result.State` is a plain map).
7. `platform/layering`: the `forbidden` map keeps `engine/methodology` and `engine/condition` off `engine/goap` and
   `engine/engine`; `TestEngineDecodesNoConvention` refuses `.Definition()` in the engine.

## Step 3: the compiled view, bound by the engine

The read models of a change (its starting points, its current state, the progress of a run) mixed two things: the
shape of what is shown (the step trees, the entry and exit conditions, what a condition means, the actions producing
it, the method a capability resolves to), which follows from the methodology alone and was walked again on every call,
and its binding to a change (the conditions evaluated, the live runs laid over the steps, the rights of the caller).

8. The compiler produces `methodology.View` (`engine/methodology/view.go`, `Compiled.View()`, computed once at
   compilation): the processes and the valid methods (`ActivityView`: kind, agent and goal running it, what its goal
   asks, `Nested`, `Reactive`, a method's capability and roles), their step trees (`StepView`: path, owner, method and
   target, capability, `foreach`, guidance, checklist, references, roles in force, entry, exit, the actions planned for
   it, `Unavailable` the context conditions of the methods of its capability) and `Conditions` (`ConditionView`:
   description, producers, prerequisites) for every condition they name. It holds no value of a change.
9. The engine binds it: `StartingPoints`, `ChangeState` and `Progress` walk the view, evaluate its conditions on the
   blackboard, lay the runs over its steps, choose the applicable method (`MethodsFor`, a binding: it depends on the
   change) and ask the rights of the caller; the facts of a missing condition stay a binding too (its terms evaluated
   on the blackboard). `platform/layering` `TestEngineBindsTheView` refuses `ProcessSteps(`, `MethodSteps(`,
   `CapabilityGuards(`, `ConditionDescription(`, `.Producers(` and `.Prerequisites(` in the engine.
10. The front reads the view as it is: RPC `registry.v1.GetMethodologyView` (a methodology as edited, saved or not,
    drawn as far as it compiles, with its issues; messages `MethodologyView`, `ViewActivity`, `ViewStep`,
    `ViewCondition`), web `registry.methodologyView`, type `MethodologyView` (`MethodologyViewJson`).

## Step 4: one view, drawn by the change screen

11. One compiled shape. The flow of a process or method (`Compiled.ProcessGraph`, the flow view of the editors) is a
    projection of the compiled view (its step tree flattened, the edges its conditions draw, the methods of the
    capabilities it names and their agents), no longer a second walk of the definition. The view carries what the flow
    needs (`StepView.Process`, `GroupBy`, `ActivityView.References`, `Actions`; every method of the definition, a
    method the compiler refused having no goal).
12. A stored version is read by name: `GetMethodologyView`, `GetProcessGraph` and `CheckLevels` take `name` /
    `version` when no methodology is sent (`Handler.compiledFor`: the compiled methodology of the registry, the version
    a change follows; unknown: NotFound, suspended: FailedPrecondition).
13. `engine.v1.GetChangeState` answers the binding only: per process its state, runs, current run and counts, per
    step (`StepState`, by path) its state, missing conditions, needs (`NeedState`: the value wanted, the prerequisites
    not holding yet, the facts), runs, sub-agents, lanes, waiting and chosen method, and the `version` of the
    methodology. Names, guidance, roles, what a condition means and the actions producing it are the view's.
14. The web composes them (`web/src/lib/changeState.ts`: `composeProcess`, `stepOf`, `needOf`, `statesByPath`):
    `ChangeState.svelte` reads the view of the version once, lays the binding over its steps for the list
    (`ProcessProgress`) and shows a process *as a flow* on demand: the methodology's own `ProcessFlowView` with the
    state of each step on its node. The flow has two displays, switched by the reader: **Detailed** (every condition
    as a port, the blocks of what the level is given and cannot reach: the editor's default) and **Simplified** (the
    steps, one link per pair labelled with its conditions, their states: the change's default).

## Next step

`PossibleSteps.svelte` and the assistant still read self-contained starting points (the MCP tool
`goap-scheduler/starting_points` needs them so); `MethodologyPort` over RPC still recompiles the definition it fetched.

## Consequences

- The names of the generated conditions (`step:<path>`, `state:<name>`, `method:<name>`, `subject:` / `pending:`)
  are the compiler's alone; it can change them without touching the engine.
- `Params` stays what a methodology gives a builtin it declares (`graph.propagate`'s `layer`, ...).
- No stored data changes: generated actions are compiled, never stored, and the process keeps the action names.
