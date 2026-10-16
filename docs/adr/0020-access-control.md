# ADR 0020 — Access control: ABAC with Casbin, policies in the graph

**Status**: accepted, implemented; §2 and §4 amended by ADR 0043 and ADR 0047 (a user holds no role of its own: roles
are granted by Assignments, administration is a platform role), §5 by ADR 0068 (the floor guards every `adminOnly`
type) · **Date**: 2026-09

## Context

Access decisions depend on attributes, not only roles: the organisation of the resource (multi-tenant), its owner
(separation of duties: you do not approve your own change), its namespace, the type of methodology. Rules must be
administrable without redeployment, and, like everything that describes the enterprise, they are graph data changed
through changes (CLAUDE.md, rule 4).

## Decision

1. **Casbin with an ABAC model**: `p = sub_rule, obj_type, act, eft`, matcher
   `(type or *) && (action or *) && eval(sub_rule)`, effect *allow unless deny*. Rules are expressions over `r.sub`
   (principal), `r.obj` (resource: type, name, namespace, organisation, owner, project) and `r.act`, with the functions
   `hasRole`, `hasAnyRole`, `isAnonymous` (since joined by `hasRoleIn`, and by `onProject` and `mayRun` of ADR 0043). Every service decides through `authz.Authorizer`.
2. **Policies and users are nodes.** There is no IAM service. `Policy` (rule, resource, action, effect) and `User`
   (subject, displayName, email, locale) are node types of the `organisation` domain; a user belongs to a unit
   through `member_of`. The `roles` the `User` first held are gone: roles are granted by `organisation@Assignment`
   nodes, on a project or platform-wide (ADR 0043, 0047).
3. `lib/access` reads them from a snapshot of the head of `main` (`Directory`, same pattern as the MCP directory) and
   builds the enforcer (`Authorizer`). Each service decides in process: the graph service over its own graph, the others
   through a graph client, falling back to the compiled-in defaults while the graph has no policy.
4. The principal is completed with its `User` node: the roles its Assignments grant are added to those of the token
   (the platform roles always, the project roles for the project of the resource, ADR 0043, 0047), and its unit is the
   organisation when the token names none. The gateway propagates the completed principal; `GET /api/whoami` returns
   it with what the web derives from it (the session, ADR 0070). Identity headers (`X-Goap-*`) are trustworthy only because services are reachable only through the gateway.
5. **Safety net**: a compiled-in **floor** (administrators may do everything) is evaluated before the stored policies
   and guards changes to `Policy` and `User` nodes (`policy:write`, `graphsvc.Handler.Floor`), since ADR 0068 to every
   node type flagged `adminOnly` (`Graph.AdminOnlyType`). These nodes are written only through changes, like every node
   (ADR 0049). With no policy in the graph (first start) or an unreadable graph, the
   compiled-in default policies (`contracts/authz/casbin.go`) apply; `graphsvc.SeedAccess` creates them as nodes once.
6. **Permissions of actions** (ADR 0004) are decided the same way, for the initiator of the process.

## Consequences

- Policies and users are versioned, reviewable and journaled like the rest; the IDE edits them as nodes.
- A policy change takes effect within a second of the change being applied (head lookup interval), without events.
- New default rules reach an existing graph only through a change (the Access screen): defaults are seeded once.
