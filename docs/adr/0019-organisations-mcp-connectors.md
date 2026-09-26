# ADR 0019 — MCP, connectors and adapters in the organisation

**Status**: accepted · **Date**: 2026-09 · Builds on ADR 0015 (namespaces), 0016 (organisation namespace and sub-changes), 0018 (algorithms).

## Context
Actions must reach real services (documents, tickets, ...). A methodology stays generic while each
organisation plugs its own systems, possibly the same connector configured differently (a file access
rooted in a different directory). Several organisations can work on one change, through sub-changes.

## Decision
1. **Three independent concepts and the place where they meet.**
   - **MCP**: the generic usage of a tool by an LLM (name + tool signatures). A node of the **`platform`**
     namespace (the former `metadata`, renamed: it holds the platform model, methodologies, domains and
     MCPs), type `MCP`, key `MCP:<name>`, in `domains/platform.yaml`. It knows no connector and no adapter.
     Actions and agents reference it by name (`mcps:`).
   - **Connector**: a driver wrapping a real API, **deployed as a separate service** implementing
     `connector.v1.ConnectorService` (`Describe`, `Invoke`). It registers itself with the MCP hub and renews the
     registration as a heartbeat (lease); the hub needs no configuration. It announces its configuration
     schema, the secrets it needs and its operations. It knows no MCP and no adapter.
     `internal/connectorkit` provides serving and registration: adding a connector is a `Connector` and a `main`.
     Registration and calls are authenticated with a shared token (`GOAP_CONNECTOR_TOKEN`).
   - **Adapter**: a node of the **`organisation`** namespace (`domains/organisation.yaml`, type `Adapter`,
     key `ADP:<unit>/<mcp>`) linked by `owner` to the OrgUnit that holds it. It names the MCP and the connector
     and carries the connector parameters, secret *references* (Vault path or `env:`) and the declarative
     mapping of the MCP tools onto connector operations (`$.arg` references, a result path; no scripts).
     It is the only place where unit, MCP and connector meet.
2. **Organisations are OrgUnits.** No second notion of organisation: the hierarchy is `part_of` (ADR 0016).
   The default organisation `ORG-DEFAULT` is created at the first start (`graphsvc.SeedDefaults`, with
   `document-repository`) and is the implicit root of every unit's ancestors. A new organisation is a new unit.
3. **The organisation holding a change** is its owner unit (`ownerOrg`; empty: `ORG-DEFAULT`), settable when
   the change or the process is created. Sub-changes split by owner have their own unit.
4. **Resolution: nearest wins.** For a tool call, the adapter of an MCP is looked up in the holding unit, then
   its ancestors, then `ORG-DEFAULT`. The same MCP and connector can thus be configured differently by each
   unit (each with its own root directory), and a unit without adapter inherits its ancestor's.
5. **The hub** (`cmd/mcp`) keeps the connector registry only. It reads MCPs, adapters and the hierarchy from the
   graph (snapshot of the head of `main`) and resolves, maps arguments, resolves secrets, invokes. It hands a
   connector only the secrets the connector declares. `CheckAdapter` validates an adapter before it is saved
   as a node.
6. **Scheduling.** An action lists the MCPs it uses (a tool action is `<mcp>/<tool>`); the engine offers it to
   the planner only when the unit holding the change resolves an adapter for each. An action can only call
   the tools of the MCPs it declared, plus, for `llm` / `script` actions, those of its agent (`agents[].mcps`).
   Tool calls carry the initiator's principal and need `tool:call`.
7. **LLM tools** are exchanged as JSON on top of the existing model gateway (bounded rounds) instead of a
   provider-specific tool API; it works with every provider, and a native protocol can replace it later
   without changing methodologies.

## Consequences
- Adapters and MCPs are versioned and reviewed through changes like any node; secrets are references.
- The engine depends on the hub for the MCPs of a unit, and the hub on the graph for the hierarchy: a
  graph outage makes MCP actions unschedulable rather than wrong.
- Renaming `metadata` to `platform` moves persisted nodes and changes with a migration; the `D:metamodel/…`
  node types of the former domain name are re-projected as `D:platform/…` at the next sync.
- Follow-ups: validating adapters as a node type algorithm (ADR 0018) so that a change cannot apply an invalid
  one; splitting a change by owner could pick the adapter units automatically.
