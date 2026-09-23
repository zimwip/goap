# ADR 0011 — Execution journal, methodology in the domain, self-observation

**Status**: accepted · **Date**: 2026-09

## Context
The execution of a change must be **traceable and auditable end to end**: not just the blackboard
items, but also every planning tick, every action execution (LLM or formal), every
model or tool call, every human decision, linked to the items they produced. This data
enables **self-observation**: a platform agent analyzes executions to **improve
the methodologies themselves**. For its proposals to be changes like any other, the
methodology must be **modeled in the domain** as a versioned element. OpenTelemetry remains the
source of truth for durations and technical pain points.

## Decision

### 1. Execution journal (change axis)
- Each change carries an `ExecutionRecord` **journal** (`pkg/domain/execution.go`), written by the engine:
  `process.started`, `tick` (world state, plan, chosen action, `replanned`, unknown conditions),
  `action` (action, specialization executed, type, effects held, items produced, tokens, model and
  tool calls, output, error, waiting on human / sub-agent), `approval` (decider, decision), `process.ended`
  (status, totals, disabled actions).
- Each record carries `methodology@version`, agent, planner, goal, `traceId` / `spanId`
  (link to the OpenTelemetry span). Blackboard items carry `execution`: the record that
  produced them (provenance down to the LLM calls).
- Stored by the graph service (memory, PostgreSQL, SQLite), write order; RPC `RecordExecutions` /
  `ListExecutions`. Writing is "best effort": a journal failure is logged, never blocking.

### 2. The methodology as a versioned domain element
- `pkg/metamodel` projects each published methodology onto nodes (`Methodology`, `Agent`, `Action`,
  `Goal`, `Condition`, `Trigger`, `NodeType`, keys `M:<methodology>[/<type>/<name>]`) and links
  (`contains`, `uses`, `pursues`, `has`, `requires`, `achieves`, `specializes`, `extends`).
- The projection goes through a **change applied on main**: each publication creates new versions
  of the modified elements (links carried by the source version, ADR 0003). The graph's history explains
  the methodology's evolution, and a journal record designates the exact version executed.
- The registry remains the source of truth for the definition; the graph is its versioned projection
  (synchronized at startup and on every publication).

### 3. Self-observation agent (`methodology-improvement`)
- Triggered at the end of a root process (`process.completed`, `process.failed`, `process.stuck`);
  the event is exposed to the process (`vars.event`).
- `analyze_run` (`observe.analyze`): journal of the run and its sub-agents, items produced, **OpenTelemetry
  spans** of the trace (Jaeger API, `GOAP_TRACE_QUERY_URL`) → cost report (`cost_report`):
  statistics per action and **findings** — loops, executions with no effects, disabled actions,
  costly LLM action, **systematizable LLM action** (regular output), replans, slow actions and spans.
- `propose_improvements` (abstract) specialized either by **rules** (`observe.propose`, default) or by
  an **LLM** (`vars.llm_review`): proposals on the methodology's nodes — specialization by a
  script for a systematizable LLM action, `fast` model, cost, an agent's actions, **MCP tool request**
  (`ToolRequest`) for a slow point.
- Human review (decisions), then `draft_methodology` (`methodology.draft`, permission
  `methodology:write`): accepted proposals are applied to the published definition and recorded
  as a **new draft version** (next patch version), to be published from the editor.

## Consequences
- The journal grows with executions: retention / archiving to plan (M1).
- Findings are heuristic and configurable (`observe.Thresholds`); the LLM can refine them.
- MCP tool requests do not modify the methodology: they feed the MCP connector's backlog.
- The loop is closed: execute → observe → propose → review → publish → execute the new version.
