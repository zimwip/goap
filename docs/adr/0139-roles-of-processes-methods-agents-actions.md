# ADR 0139 — Every process and method has a role; agents and actions take the role of what they run

**Status**: proposed · **Date**: 2026-10 ·
Revises ADR 0035 §2 (RACI roles of steps and methods) and ADR 0050 (the role an agent acts as). Builds on ADR 0043
(roles allowed to run an agent or an action) and ADR 0138 (deny by default).

## Context

A methodology assigns its roles RACI style on steps and methods, but nothing requires it: a process names no role at
all, a method may name none, a step without roles leaves its human tasks to anyone (`stepAllowed` answers yes), and
the role an agent acts as is set only for the agent generated for a method. Under deny by default (ADR 0138) a piece
of work nobody answers for is a hole: who performs a step, and as whom an agent or an action works, must always be
said, without the author repeating it on every element.

## Decision

### 1. A process and a method name their responsible role

`Process.roles` (`Responsibilities`, YAML `roles`, proto `Process.roles`, attribute `roles` of `methodology@Process`)
joins `Method.roles`. Both must name `roles.responsible`: the compiler reports `processes[i].roles.responsible` /
`methods[i].roles.responsible` (`methodology.MissingResponsible`) as an **error for a stored methodology** (the
registry refuses to publish it, `ValidateStored`) and a **warning for a draft** being written (`Validate`, `Compile`,
`CompileLenient`), so that editing never blocks on it.

### 2. Inheritance, field by field

The roles in force for a step (`StepInfo.Effective`, the step context of a run, the view) are its own, what they
leave unsaid taken from the step containing it, then from its process (a step of a method: from its method).
`Responsibilities.Over(parent)` fills an empty responsible or accountable and a nil consulted or informed list. The
method chosen for a capability step lays its roles over the step's the same way (`StepContext.withMethod`). So every
step of a stored methodology has a responsible role, and its human tasks go to it (`step:perform`).

### 3. An agent acts as the role of what it runs, unless it names one

`Agent.role` (proto `Agent.role`) is the role the agent acts as. The agent generated for a process acts as the
process's responsible role, the one generated for a method as the method's (ADR 0050). A declared agent keeps the
role it names; naming none, it acts as the one role its actions act as (none when they differ). `Agent.roles` (who
may start it, ADR 0043) is not touched: the role an agent acts as is not a restriction on who starts it.

### 4. An action acts as the role of the element refining it

`Action.role` (YAML `role`, proto `Action.role`, attribute `role` of `methodology@Action`, linked `assigns` as
`actsAs` like an agent's) is the role the action acts as. Declared, it wins. Otherwise the compiler
(`compileState.propagateRoles`, `engine/methodology/roles.go`) gives:

- an action planned for a step, the responsible role in force for that step;
- a declared action, the one responsible role of the steps it is planned for and of the methods whose pool lists it
  (none when they differ: it depends on where it runs, and the step context says so at run time);
- a **specialization** of an action of the same methodology, the role of the action it specializes: the default
  propagation along `specializes`.

`Action.roles` (who may run it, ADR 0043) is not touched either.

### 5. What the engine does with it

The step context already carries the inherited roles. When a human task has no step responsible (an action run
outside a step), the person submitting it must hold the role the action acts as (`Engine.maySubmit`), and
`GetChangeAsks` addresses it to that role; an llm action with no step role is told the role it acts as.

None of these roles binds the platform administrator: the first default rule (`hasRole(r.sub, "admin")`, a
floor no stored policy removes, ADR 0047) allows every request, so an administrator performs a step reserved to a
role, runs an agent or an action of declared roles and sits on a board alike (`TestPlatformAdministratorPassesTheRolesOfMethodologies`).

## Consequences

- The shipped methodologies name the responsible of every process: `sdlc` 0.11.1 (`software_delivery`:
  business analyst, accountable product owner; `release_train`: release manager), `release-based` 0.1.2,
  `risk-management` 1.2.1, `poc` 0.1.1 (new roles `developer` and `tech_lead`; the confirmations are the tech lead's).
- A human step that inherited no role before is now performed by its process's responsible role: tests and demos
  submit as a holder of it.
- Methodology domain 1.16.0 (`roles` on `Process`, `role` on `Action`). Web: the process tab edits its
  responsibilities, the action and agent editors their *Acts as* role; the agent's `role` now survives the registry
  round trip (it had no proto field).

## Not yet

- Acting as the role at run time beyond human tasks (tool calls and graph writes of an agent authorized as its role
  rather than as its initiator).
- A step's role checked against the roles of the agent that carries it out.
