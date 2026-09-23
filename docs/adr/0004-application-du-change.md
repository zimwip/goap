# ADR 0004 — Applying the change is a plannable action protected by a permission

**Status**: accepted · **Date**: 2026-09

## Context
Actions only write to the change (ADR 0001); the domain is only transformed at
application time (`ApplyChange`). It had to be decided who triggers this application: a manual act outside
the engine, or the engine itself.

## Decision
- Applying is a `builtin: graph.apply` action of the methodology, with preconditions (generally
  `reviewed: true`) and the effect `applied: true` (`change.status == "applied"`).
- An action can declare a `permission` (here `change:apply`). The engine evaluates it for
  the process's **initiator**:
  - permission held → automatic execution;
  - otherwise → an **approval** task; `ApproveAction` by an authorized person executes the action with
    their identity (`approvedBy`); a refusal disables the action for this process.
- Permissions are decided by `authz.Authorizer`: a static role policy today,
  `IamService.CheckPermission` at milestone M2.

## Consequences
- Each methodology decides whether the application is automatic, subject to approval, or absent
  (a goal that stops at review).
- Validation remains traceable in the process (step, approver, refusal comment).
- Application can fail with a conflict if the reference repository has evolved since the reference baseline:
  the step errors out and the action is retried and then disabled like any other action.
