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
- Permissions are decided by `authz.Authorizer` (ABAC, policies in the graph, ADR 0020).

## Consequences
- Each methodology decides whether the application is automatic, subject to approval, or absent
  (a goal that stops at review).
- Validation remains traceable in the process (step, approver, refusal comment).
- `graph.apply` is `Apply`: it commits what is pending, then merges (ADR 0102). If the reference repository has
  evolved since the change forked, the change is rebased first (ADR 0101): rebase conflicts make the process wait on a
  `rebase` task, and conflicts of the residual three-way merge (the target moved in between) on a `merge` task; a
  person settles them (the planner then applies again; settling a `merge` task completes the merge). Any other
  refusal errors the step, and the action is retried and then disabled like any other action.
