# ADR 0019 — MCP, connectors and adapters in the organisation

**Status**: accepted · **Date**: 2026-09 · Builds on ADR 0015 (namespaces), 0016 (organisation namespace and sub-changes), 0018 (algorithms).

## Context
Actions must reach real services (documents, tickets, ...). A methodology stays generic while each
organisation plugs its own systems, possibly the same connector configured differently (a file access
rooted in a different directory). Several organisations can work on one change, through sub-changes.

## Decision
1. **Three independent concepts and the place where they meet.**
   - **MCP**: the generic usage of a tool by an LLM (name + tool signatures). A node of the **`platform`**
     domain, type `platform@MCP`, key `MCP:<name>`, declared in `domains/platform.yaml`. It knows no connector and no adapter.
     Actions and agents reference it by name (`mcps:`).
   - **Connector**: a driver wrapping a real API, **deployed as a separate service** implementing
     `connector.v1.ConnectorService` (`Describe`, `Invoke`). It registers itself with the MCP hub and renews the
     registration as a heartbeat (lease); the hub needs no configuration. It announces its configuration
     schema, the secrets it needs and its operations. It knows no MCP and no adapter.
     `internal/connectorkit` provides serving and registration: adding a connector is a `Connector` and a `main`.
     Registration and calls are authenticated with a shared token (`GOAP_CONNECTOR_TOKEN`).
   - **Adapter**: the code that makes the two work together. An MCP expects functions (its tools), a connector
     exposes its own (its operations); the adapter implements the former with the latter, and only it knows both.
     It is a **`platform@AdapterDef` node** (key `ADD:<name>`, usage `adapter` of ADR 0018),
     so creating or changing one is a change like any modification of the platform's configuration. It names one
     `mcp` and one `connector`, with typed parameters (a `secret` type holds a
     reference the code can never read). The code is the body of `function (ctx)` with `ctx.tool()`,
     `ctx.args()`, `ctx.param(name)`, `ctx.call(operation, args)` (the operations the connector exposes) and
     `ctx.fail(msg)`; it returns the result of the tool. A **template is generated** from the MCP definition (one
     case per tool) and the connector's operations, and the parameters from the connector's configuration schema.
     Parameters are handed to the connector as its configuration under the same name; secret parameters are
     resolved by the hub and passed under their name.
     **Each organisational unit holds an instance**: an `Adapter` node of the **`organisation`** namespace
     (key `ADP:<unit>/<mcp>`, linked by `owner` to the OrgUnit) with the name of the definition (`adapter`) and the
     parameter values. It is the only place where unit,
     MCP and connector meet, and it is what lets units share the same adapter with different scopes.
2. **Organisations are OrgUnits.** No second notion of organisation: the hierarchy is `part_of` (ADR 0016).
   The default organisation `ORG-DEFAULT` is created at the first start (`graphsvc.SeedDefaults`, with
   `document-repository`) and is the implicit root of every unit's ancestors. A new organisation is a new unit.
3. **The organisation holding a change** is its owner unit (`ownerOrg`; empty: `ORG-DEFAULT`), settable when
   the change or the process is created. Sub-changes split by owner have their own unit.
4. **Resolution: nearest wins.** For a tool call, the adapter of an MCP is looked up in the holding unit, then
   its ancestors, then `ORG-DEFAULT`. The same MCP and connector can thus be configured differently by each
   unit (each with its own root directory), and a unit without adapter inherits its ancestor's.
5. **The hub** (`cmd/mcp`) keeps the connector registry only. It reads MCPs, adapter instances and the hierarchy
   from the graph (snapshot of the head of `main`) and the adapter definitions from the same snapshot, checks the arguments against the tool's schema, runs the adapter (bounded: 30 s, 32 connector
   calls), resolves secrets, invokes the connector. It hands a connector only the secrets the connector
   declares. `CheckAdapter` validates an instance before it is saved as a node; `AdapterTemplate` generates code.
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
- Adapter code runs in the hub process (goja / yaegi, no file, network or process access, bounded), the same
  trust model as the algorithms of ADR 0018: authors of the domain library are trusted; sandboxing them with the
  pool of ADR 0007 is the next step if that changes. The seeded definition
  `localfs-document-repository` is the reference adapter.
- Web: one **Adapters** explorer manages MCPs, connectors and adapters together; an adapter opens in a dedicated editor
  (MCP and connector pickers showing which tools / operations the code handles, parameters, code editor) edits the
  `AdapterDef` node through a change. Domains cannot declare `adapter` algorithms any more (validation rejects them).
- Follow-ups: validating adapter instances as a node type algorithm (ADR 0018) so that a change cannot apply an
  invalid one; "try it" for adapters against a fake connector; splitting a change by owner could pick the adapter
  units automatically.
