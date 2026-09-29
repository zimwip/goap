# ADR 0034 — Processes and steps: how a methodology describes the route to its objective

**Status**: accepted, implemented · **Date**: 2026-09 ·
Builds on ADR 0004 (change application), ADR 0009 §5 (specializations), ADR 0011 (execution journal), ADR 0023
(definitions in the graph). Relates to ADR 0033 (from a request to a shipped change).

## Context

A methodology had conditions, actions, goals and agents. The planner chose freely among an agent's actions, so the
methodology could say *what* must hold at the end, but not *how* the organisation works to get there: the phases, the
steps inside them, their order, and who or what does each one. Real methods are described with uneven precision: some
steps are fully automated, some are "the architect designs it" (an agent that plans), some only say what a person must
check. Carrying out one change often needs several such processes, one inside another (delivery runs a release train,
which runs a deployment procedure).

## Decision

1. **A methodology declares `processes`.** A process has a name, a description, intent examples and a tree of
   **steps**. A step has a name, a description, entry conditions (`pre`), exit criteria (`done`) and **one method**:

   | Method | Field | The step is done by | Default exit criteria |
   |---|---|---|---|
   | sub-steps | `steps` | its sub-steps (in order, or `parallel: true` with `after`) | those of its sub-steps |
   | action | `action` | one action of the methodology (its specializations apply) | the action's effects |
   | alternatives | `actions` | the actions the planner chooses among (the cheapest first, another one when it fails) | the first one's effects |
   | agent | `agent`, `goal` | an agent of the methodology, which plans towards the goal (default: its only goal) | the goal's conditions |
   | nested process | `process` | another process: `<process>` of this methodology or `<methodology>/<process>` | those of the nested process; for another methodology's process, once it has run |
   | by hand | (none) | a person, from `instructions` | once submitted |

   Steps run in declaration order unless their level is `parallel`; `after` then names the earlier siblings a step
   waits for. A step waits for the whole chain before it (entry is transitive), so the order holds even when the
   criteria of an earlier step already hold at the start.

2. **A process compiles to the planner's vocabulary**; nothing new runs at execution. For each process, the
   methodology gets an **agent** and a **goal** of the process name (the name must not collide with a declared agent
   or goal). Each step other than sub-steps becomes a planned **action** named by its path
   (`software_delivery/analysis/scope`, alternatives suffixed `:<action>`): its preconditions are the step's entry
   conditions plus the exit criteria of what precedes it, its effects the step's exit criteria. An action step is a
   copy of the action (same implementation, `Action.Implements` names it so its specializations apply); an agent or
   nested process step is the builtin `process.step`; a manual step is a human action. A step done "once it has run"
   gets a generated condition `step:<path>` over a `step_done` artifact, recorded by `process.step` when its
   sub-agent completes and by the engine when a person submits a manual step. The process agent plans with the step
   actions only; declared agents never see them.

3. **Nesting is sub-agents on the same change.** `process.step` starts the agent (towards its goal) or the agent of
   the nested process as a sub-agent of the running process on the same change, and waits for it
   (`TaskAgent`, resumed when the child ends). A child that ends stuck or failed fails the step. Several processes can
   so take part in one change, each journaled as its own run (ADR 0011), the change remaining the one blackboard.
   A process that nests itself (directly or through others of the methodology) is refused.

4. **A methodology may have processes only**: "at least one goal or process". Without declared agents and goals,
   there is no default agent; the processes' agents are the methodology's agents.

5. **Storage and contract.** A process is an element of a methodology version like the others: a node
   `methodology@Process` (meta-domain 1.1.0) keyed `<version key>/process/<name>`, its steps a property. The registry
   contract carries `Methodology.processes` (`Process`, `Step`); the summary lists each process as an agent (planner
   `process`) and a goal, so identification and the assistant offer them. The IDE edits them in a Process tab (a tree
   of steps; the issue paths are `processes[i].steps[j].steps[k].<field>`).

## Consequences

- A methodology can be as precise as its authors are: a process can start as a list of manual steps and gain
  actions, agents and sub-processes over time without changing how it runs.
- The planner still decides within a step (alternatives, an agent's own plan) and replans when the world changes:
  a step whose criteria stop holding (a new requirement not yet traced) is planned again before the steps after it.
- `methodologies/sdlc.yaml` 0.5.0 describes `software_delivery` (framing by hand, analysis in sub-steps with
  alternatives, design by the architect agent, build by the specialized `build` action, `release_train` nested,
  application) next to its existing agents; `pkg/engine/sdlc_test.go` runs it end to end.
- A step retried after its sub-agent ended appears twice in the run's steps (suspended, then completed), as for any
  action waiting for a sub-agent.
- Not done yet: a step's own permission or addressee (who is expected to do a manual step: ADR 0033 §5), and
  showing a run as the progress of its process (steps done / current / remaining) in the change and the assistant.
