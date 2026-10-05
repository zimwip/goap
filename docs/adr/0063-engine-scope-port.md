# ADR 0063 — The engine reaches organisation, project, authorization and MCPs through one port

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0019, 0028, 0035, 0039, 0043, 0062.

## Context

The engine knew the organisation (`Engine.orgOf`), the project (`projectOf`), the authorization model
(`Engine.Authz`, `authz.Resource` built in five places, `authz.With(ctx, actor)` repeated a dozen times) and the MCP
hub (`Engine.Tools`, whose port took an organisation). Design rule 1 says a methodology knows no organisation or
connector beyond one meeting point; the engine, which runs methodologies, had four. "Nil authorizer grants
everything" was tested at each of them. None of it was tested in isolation.

## Decision

1. `engine.Scope` is the one port: `As` (ctx acting as the actor of a process), `HasTools`, `Tools`, `CallTool`,
   `Allowed` (a permission on the process or its change), `MayRun` (roles of an agent or action), `MayStep`
   (perform / approve a step). It takes a `ProcessRef` snapshot (id, methodology, change, initiator, resolved
   organisation and project), never a `*Process`, and plain data (roles, step path), never the methodology.
2. What reads the methodology or the process tree stays in the engine and calls the port: `runRoles`,
   `checkAgentRoles`, the choice of the role in `stepAllowed`, the walk up the initiators in `mayUnblock`.
3. `AuthzScope{Authz, Hub}` is the only implementation: it builds the `authz.Resource`s exactly as the engine did and
   is the one place where a nil `Authz` grants everything and a nil `Hub` binds nothing (a test and `goap-dev`
   convenience). `Engine.Scope` replaces `Engine.Authz` and `Engine.Tools`; unset, it behaves as a zero `AuthzScope`.
4. `Engine.cycle` is split (`cycle.go`): `observeGoal`, `admissible`, `planStep` (with `settleNoPlan`), `newStep`,
   `gateAction` (the role gate and the permission gate share one approval-task builder). `process.started` is
   journalled by one helper, and the four `runChild*` wrappers collapse to `runChild` and `runChildStep` (the
   `anyMethodology` flag was a no-op).

## Consequences

- A different authorization model or tool binding is a new `Scope`, not a change to the engine.
- `pkg/engine` still imports `pkg/authz` for `authz.Principal` (the initiator and approver of a process, the caller
  in a context), `authz.From` / `With` and `ErrForbidden`; it no longer builds a request or a resource. The graph
  request types it imports from `pkg/graph` are a later phase.
