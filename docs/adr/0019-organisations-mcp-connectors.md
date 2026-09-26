# ADR 0019 — Organisations, MCP and connectors

**Status**: accepted (implemented: organisations, MCP hub, connectors, scheduling; web screens to come) · **Date**: 2026-09

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
3. **MCP**: a generic declaration (name + tool signatures, e.g. `document-repository`), kept by the MCP hub
   (`cmd/mcp`) with its adapters and the organisation bindings (its own database; one service owns the tool
   layer rather than splitting definitions from bindings).
4. **Connector**: a driver wrapping a real API, **deployed as a separate service** implementing
   `connector.v1.ConnectorService`. It registers itself with the hub and renews its registration as a
   heartbeat (lease); the hub needs no configuration. `internal/connectorkit` provides the serving and
   registration; adding a connector is writing a `Connector` and a `main`. The hub authenticates the
   registration and calls with a shared token (`GOAP_CONNECTOR_TOKEN`).
5. **Adapter**: declarative mapping of the tools of an MCP onto operations of a connector (`$.path`
   references to the tool arguments, a result path). No scripts.
6. **Organisation binding**: MCP -> (connector, configuration, secret references) per organisation. Secrets
   are references resolved by the hub at call time (Vault or `env:`), and only the ones the connector declares
   are passed on.
7. **Scheduling**: an action lists the MCPs it uses (a tool action is `<mcp>/<tool>`); the engine offers it to
   the planner only when the organisation of the change binds them all, and an action can only call the tools
   of the MCPs it declared. Tool calls carry the initiator's principal and need `tool:call` in the
   organisation.
8. **LLM tools**: an LLM action with `mcps` exchanges tool calls as JSON on top of the existing model
   gateway (bounded rounds), instead of a provider-specific tool API: it works with every provider now, and a
   native tool-calling protocol can replace it later without changing methodologies.

## Consequences
Bindings and MCP administration need admin (policies can grant `mcp` / `binding` writes). Web
administration screens (connectors, MCPs, adapters, bindings) are the remaining piece.
