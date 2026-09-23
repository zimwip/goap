# ADR 0005 — ABAC access control with Casbin

**Status**: accepted · **Date**: 2026-09 · Replaces the static role policy introduced in ADR 0004.

## Context
Access decisions depend on attributes, not just roles: the resource's organization
(multi-tenant), the owner (separation of duties: you don't approve your own change), methodology
type… Rules must be administrable without redeployment.

## Decision
- **Casbin** with an ABAC model: `p = sub_rule, obj_type, act, eft`, matcher
  `(type or *) && (action or *) && eval(sub_rule)`, effect *allow unless deny*.
- Rules are expressions over `r.sub` (Principal), `r.obj` (Resource), and `r.act`, with the
  functions `hasRole`, `hasAnyRole`, `isAnonymous`.
- Policies persisted in the `iam` schema (`casbin_rule`, custom pgx adapter), administered by
  `IamService`; other services query `CheckPermission` via `authz.Authorizer`.
- In all-in-one mode (`goap-dev`), the enforcer is in-memory within the process.

## Consequences
- A single authorization semantics for all services; rules changeable on the fly.
- Every decision costs one RPC call to iam; a decision cache can be added if needed.
- Default policies are only inserted if the table is empty: evolving them on an existing
  database goes through the admin API (or a migration).
- Identity headers (`X-Goap-*`) are only trustworthy if services are reachable only through the gateway.
