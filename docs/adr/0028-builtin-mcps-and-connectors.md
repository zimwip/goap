# ADR 0028 — Built-in MCPs and connectors, and restricting an MCP in a unit

**Status**: accepted, implemented · **Date**: 2026-09 ·
Builds on ADR 0019 (MCP, connectors, adapters), ADR 0016 (organisation), ADR 0020 (access control), ADR 0024 (change impacts).

## Context
Actions reach the outside world through MCPs, but an agent could not use the platform itself as a tool: read the
graph, prepare a change, start another process, look up the organisation. An agent harness ships its own tools
(read / glob / grep a workspace, write and edit files, hand a task to a sub-agent, list tasks) next to the ones users
plug in, and lets each workspace allow or deny them. GOAP needs the same thing, placed in its own concepts.

Question answered (CLAUDE.md, rule 8): **WITH WHAT** (the tools, their connectors and adapters) and **WHO** (which
unit may use them).

## Decision

### 1. Four built-in MCPs, split by concern
The platform is exposed as four MCPs of the `platform` namespace (`pkg/mcp/builtin.go`), each with a connector
of the same name:

| MCP | Tools | What it is for |
|---|---|---|
| `goap-graph` | `read`, `glob`, `grep`, `links`, `baselines` (all read-only) | read the versioned graph, as of the head of `main` of a namespace or of a baseline |
| `goap-change` | `create`, `read`, `write`, `edit`, `link`, `retire`, `note`, `validate` | work on a change, the blackboard of every modification: open one, declare and write nodes (change impacts), add notes, check it |
| `goap-scheduler` (scope `agent`) | `start`, `list`, `get`, `triggers`, `fire` | start a process for an intent (another agent), follow processes, fire triggers |
| `goap-admin` | `units`, `users`, `mcps`, `connectors`, `domains`, `methodologies` (all read-only) | describe the platform: who, with what, what, how |

- The split follows the platform's own services: graph reads, changes (graph writes, always through a change —
  rule 2), the engine (scheduler), and the administration (organisation, hub, registry). It is the unit of
  restriction too: a unit can keep `goap-graph` and drop `goap-change`.
- Tools carry `readOnly` (the MCP `readOnlyHint`): a unit can keep only those.
- `goap-change` writes on the change branch only (`AddNodes` then `WriteNode`); **applying stays a decision** of
  the methodology or of a reviewer, so there is no `apply` tool. `edit` takes `expect` (current values) to refuse
  an edit made on stale content. Without a `change` argument the tools work on the change of the calling process.
- `goap-admin` changes nothing: the organisation, the adapters and the methodologies are graph data, changed
  through changes (`goap-change`).

### 2. The scope of an MCP: action, agent or both
An MCP (`platform@MCP`, any MCP, not only the built-in ones) has a `scope` saying where a methodology may use it
(platform domain 2.1.0):
- `action`: declared by actions (`actions[].mcps`, a tool action's `<mcp>/<tool>`);
- `agent`: declared by agents only (`agents[].mcps`) and reached by the **llm actions** of the agent — the agent
  (scheduler) level; script actions and the action's own declarations do not reach it;
- `both` (the default, empty).

`goap-scheduler` has scope `agent`: creating other agents and following them is orchestration, the business of the
agent level, not of a step. The rule holds everywhere:
- **Scheduling**: an MCP of scope agent is never bound for actions, so an action declaring it, or a tool action on it,
  is never planned (`Engine.boundMCPs`).
- **Calls**: the action host knows which MCPs come from the action and which from its agent; it lists and calls a
  tool only when the scope of its MCP allows it from there (`Host.permitted`). The hub carries the scope on every
  listed tool (`mcp.ToolInfo.Scope`, `Tool.scope`).
- **Validation**: the registry reports an action declaring an agent-scoped MCP (`actions[i].mcps` / `.tool`) and an
  agent declaring an action-scoped one (`agents[i].mcps`), from the scopes of the head of the platform namespace
  (`Service.MCPScopes`); unknown MCPs stay allowed.
- The IDE edits the scope of an MCP, and the action and agent editors say which MCPs they may not declare.

### 3. Built-in connectors, served by the hub
- `internal/connectors/builtin` implements the four connectors with `connectorkit.Connector`. The hub serves them
  in-process (`inproc://<id>`, `mcpsvc.InprocInvoker`) and registers them like any connector (`KeepRegistered`);
  `goap-dev` wires the in-process graph, engine handler, registry and hub, `cmd/mcp` the service clients
  (`GOAP_GRAPH_URL`, `GOAP_ENGINE_URL`, `GOAP_REGISTRY_URL`). A connector still knows no MCP: it declares its own
  operations, a test checks they match the tools of the MCP of the same name.
- **They act for the caller, never for themselves.** The hub has already checked `tool:call` for the principal (the
  initiator of the process). The connectors then go through the platform's own rules: reads are filtered by the
  `read` authorization of each node type, `User` and `Policy` nodes stay behind the access gate (`policy:write`
  against the floor, ADR 0020), processes are started and read through the engine service, which authorizes them.
  An anonymous call is refused. Service clients forward the caller's identity (`builtin.ForwardIdentity`).
- **The call context.** A tool call carries what it runs for (`mcp.CallContext`: unit, change, process). The engine
  sets the change and the process of the action; `CallToolRequest` carries them to a remote hub (`change_id`,
  `process_id`); the hub sets the unit holding the change. This is how `goap-change` defaults to the change of the
  process and `goap-scheduler/start` to its unit and namespace.

### 4. Every unit gets them from the default organisation
Each built-in has a pass-through adapter definition (`ADD:<name>`, code `return ctx.call(ctx.tool(), ctx.args())`)
and the default organisation holds an instance of each (`ADP:ORG-DEFAULT/<name>`). By the resolution of ADR 0019
(nearest wins, `ORG-DEFAULT` last) every unit can use them without configuration.
`graphsvc.SeedBuiltins` runs at every start of the graph (and `goap-dev`): the MCP and adapter definition nodes are
created or brought back to what the code declares (like the built-in domains, they ship with the platform); the
instances of the default organisation are created only when the MCP is first seeded, so that removing one sticks.

### 5. A unit restricts an MCP; restrictions add up down the organisation
An `organisation@Adapter` node gains four properties (organisation domain 1.4.0): `disabled`, `tools` (allow-list),
`deny`, `readOnly`. An instance may **only restrict** (no `adapter`): the implementation is then the one of the
nearest ancestor. Resolution (`mcpsvc.Snapshot`):
- the **implementation** is the nearest instance of the chain that names an adapter (unchanged);
- the **restriction** is the sum of every instance of the chain (`mcp.Restriction`): disabled anywhere disables,
  allow-lists intersect, deny-lists and read-only add up. A unit narrows what it inherits and passes it down to its
  sub-units; a sub-unit **cannot widen** it (rule 3: responsibility flows down).

The hub lists only the allowed tools (`ListTools`, an MCP with no tool left is not bound), refuses a restricted tool
(`ErrNotBound`), and reports the restriction in `ListEffective` (`allowed_tools`, `restricted_by`, `disabled`,
`builtin`). The engine plans a `tool` action only when its tool is allowed, and an `llm` / `script` action only
sees the allowed tools. This applies to every MCP, not only to the built-in ones.

ABAC (who may call a tool, `tool:call`) and restrictions (what a unit can reach) stay separate, as in rule 5.

## Consequences
- Agents can inspect the graph, prepare changes and delegate sub-tasks with the same tool loop as any MCP, and every
  write they make is a reviewable change impact.
- The IDE shows the built-in MCPs on every unit's MCP pane (tagged *built in*, with the number of allowed tools and
  who restricts them) and edits the restrictions of a unit (disable, read-only, tools refused).
- The hub now depends on the engine and the registry for two of the built-ins; when they are unreachable those
  tools fail, the others work.
- Follow-ups: protect the built-in MCP and adapter definition nodes from edits (today they are brought back at the
  next start); a `goap-change` flow argument for processes working on a flow branch (ADR 0025); per-tool quotas as
  another restriction flowing down the organisation.
