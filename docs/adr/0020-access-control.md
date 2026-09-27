# ADR 0020 — Access control: ABAC with Casbin, policies in the graph

**Status**: accepted, implemented · **Date**: 2026-09

## Context

Access decisions depend on attributes, not only roles: the organisation of the resource (multi-tenant), its owner
(separation of duties: you do not approve your own change), its namespace, the type of methodology. Rules must be
administrable without redeployment, and, like everything that describes the enterprise, they are graph data changed
through changes (CLAUDE.md, rule 4).

## Decision

1. **Casbin with an ABAC model**: `p = sub_rule, obj_type, act, eft`, matcher
   `(type or *) && (action or *) && eval(sub_rule)`, effect *allow unless deny*. Rules are expressions over `r.sub`
   (principal), `r.obj` (resource: type, name, namespace, organisation, owner) and `r.act`, with the functions
   `hasRole`, `hasAnyRole`, `isAnonymous`. Every service decides through `authz.Authorizer`.
2. **Policies and users are nodes.** There is no IAM service. `Policy` (rule, resource, action, effect) and `User`
   (subject, displayName, email, locale, roles) are node types of the `organisation` domain; a user belongs to a unit
   through `member_of`.
3. `pkg/access` reads them from a snapshot of the head of `main` (`Directory`, same pattern as the MCP directory) and
   builds the enforcer (`Authorizer`). Each service does so in process (the graph service over its own graph), so no
   service depends on another to decide.
4. The principal is completed with its `User` node: roles are added to those of the token, and the unit is the
   organisation when the token names none. The gateway propagates the completed principal; `GET /api/whoami` returns
   it. Identity headers (`X-Goap-*`) are trustworthy only because services are reachable only through the gateway.
5. **Safety net**: a compiled-in **floor** (administrators may do everything) is evaluated before the stored policies
   and guards changes to `Policy` and `User` nodes (`policy:write`, `graphsvc.Handler.Floor`). Direct writes of these
   nodes are refused; they go through changes. With no policy in the graph (first start) or an unreadable graph, the
   compiled-in default policies (`pkg/authz/casbin.go`) apply; `graphsvc.SeedAccess` creates them as nodes once.
6. **Permissions of actions** (ADR 0004) are decided the same way, for the initiator of the process.

## Consequences

- Policies and users are versioned, reviewable and journaled like the rest; the IDE edits them as nodes.
- A policy change takes effect within a second of the change being applied (head lookup interval), without events.
- New default rules reach an existing graph only through a change (the Access screen): defaults are seeded once.
