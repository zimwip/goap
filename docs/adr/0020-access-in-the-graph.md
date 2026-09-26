# ADR 0020 — Access control in the graph

## Context

What describes the enterprise is graph data, changed through changes (CLAUDE.md, rule 4). Policies lived in the
`casbin_rule` table of a separate IAM service, and users did not exist at all: roles came from the token only.

## Decision

1. There is no IAM service. `Policy` (rule, resource, action, effect) and `User` (subject, displayName, email, locale,
   roles) are node types of the `organisation` namespace; a user belongs to a unit through `member_of`.
2. `pkg/access` reads them from a snapshot of the head of `main` (`Directory`, same pattern as the MCP directory) and
   builds the Casbin enforcer (`Authorizer`). Each service does so in process; the graph service over its own graph, so
   no service depends on another to decide.
3. The principal is completed with its `User` node: roles are added to those of the token, and the unit is the
   organisation when the token names none. The gateway propagates the completed principal; `GET /api/whoami` returns it.
4. Safety net: a compiled-in **floor** (administrators may do everything) is evaluated before the stored policies, and
   guards changes to `Policy` and `User` nodes (`policy:write`, `graphsvc.Handler.Floor`). Direct writes of these
   nodes are refused; they go through changes. With no policy in the graph (first start) or an unreadable graph, the
   compiled-in default policies apply; `graphsvc.SeedAccess` creates them as nodes once.

## Consequences

- Policies and users are versioned, reviewable and journaled like the rest. The frontend edits them as nodes.
- A policy change takes effect within a second of the change being applied (head lookup interval), without events.
- The `casbin_rule` and `organization` tables are gone with the service; existing local databases keep unused tables.
- Model configuration and the registry follow the same direction (next steps of the plan).
