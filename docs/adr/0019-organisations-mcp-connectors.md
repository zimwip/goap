# ADR 0019 — Organisations, MCP and connectors

**Status**: accepted (step 2 implemented: organisations; step 3 planned) · **Date**: 2026-09

## Context
Actions need to reach real services (documents, tickets, ...). A methodology must stay generic
while each organisation plugs its own systems. Every modification (change) must therefore belong
to an organisation, which decides what its actions can use.

## Decision
1. **Organisation** (tenant): stored by iam (`organization` table, PostgreSQL and SQLite). The
   `default` organisation is seeded. Not to be confused with `OrgUnit` nodes (ADR 0016), the internal
   structure of a company.
2. **Every change has an `orgId`** (`change_set.org_id`, backfilled with `default`). It comes from the
   caller's principal, falling back to the request, then to `default`; a sub-change inherits it from
   its parent. Executions take their organisation from their change.
3. **MCP** (planned, step 3): a generic declaration (name + tool signatures, e.g. `document-repository`),
   kept with the registry.
4. **Connector** (planned): Go driver wrapping a real service API, in a pluggable registry
   (same pattern as the `modelgw` providers).
5. **ConnectorAdapter** (planned): declarative mapping of the tools of an MCP onto operations of a
   connector (no scripts).
6. **Organisation bindings** (planned): MCP to (adapter, connector configuration, secret reference).
   An LLM or tool action lists the MCPs it uses; it is schedulable only when the organisation of
   the change binds all of them.

## Consequences
Steps 3+ add proto services, tables in both dialects and a scheduling filter in `Engine.cycle`.
