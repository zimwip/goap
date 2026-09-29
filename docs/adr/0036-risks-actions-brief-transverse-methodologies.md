# ADR 0036 — Risks and actions, the change brief, transverse methodologies, visualizers

**Status**: accepted, implemented · **Date**: 2026-09 ·
Builds on ADR 0001 (the change as blackboard), ADR 0009 §4 (decision points), ADR 0030 (one change log), ADR 0034
(processes), ADR 0035 (methods, roles).

## Context

The change carries the whole traceability of a piece of work and its decisions. Three things were missing:

1. **Risks and actions.** A change is also where its risks are raised, assessed and mitigated, and where the actions
   that follow from risks and decisions are tracked.
2. **Context for the models.** An LLM call should receive the most information in the fewest tokens, and have tools to
   follow an information through the change (who produced it, from what, what it led to).
3. **Transverse methodologies.** Risk management is a process of its own, but it runs permanently and across every
   change, not as a step of one methodology.

And, on the web side, a way to *see* a process (its steps, how conditions chain them, the methods behind a step) and
to navigate the whole graph (the organisation as it is).

## Decision

### 1. Risks and actions are facts of the change

Two item kinds join the blackboard, `risk` and `action`. An item is a **version** of a record identified by its `key`
(`RSK-1`, `ACT-1`): raising, assessing, mitigating or closing a risk adds a new version, and the current register is
the last version of each key (`domain.Risks`, `domain.ActionItems`); the change log keeps every version, who wrote it
and in which run (ADR 0030).

| Record | Fields |
|---|---|
| risk | `key`, `title`, `description`, `probability` and `impact` (1–5), `score` (their product), `status` (`open`, `mitigating`, `accepted`, `occurred`, `closed`), `owner` (a role), `actions` (keys), `step` (where it was raised) |
| action | `key`, `title`, `status` (`open`, `done`, `cancelled`), `owner` (a role), `due`, `for` (a risk key or a decision point id), `result` |

- CEL sees `risks` and `actions` (the register); platform conditions `open_risks`, `unmitigated_risks` (an open risk of
  score ≥ 9 with no open or done action), `risks_under_control`, `open_actions`, `no_open_actions`.
- `goap-change` serves `risks`, `risk` (raise or update), `actions`, `action`; actions and people write them as items
  (`{"kind":"risk","data":{...}}`).
- The change page has a Risks & actions pane (register, heat, actions by owner).

### 2. The change brief and the trace tools

- **Brief.** Every LLM action on a change gets, in its system prompt, a compact brief of the change: one line per fact,
  no JSON — the intent, the step and its roles, the change impacts (`key type intent review`), open decision points
  and questions, the risk register (`key score status owner`), open actions, the latest artifacts (type and a short
  summary). Bounded (the most recent and the most important first) so its size does not grow with the change.
  `goap-change/brief` returns it for agents that call tools.
- **Trace.** `goap-change/trace` follows an information through the change: for an item, the items it derives from,
  those derived from it, the action and run that produced it and what superseded it; for a node, the events of its
  change impact (declared, written, reviewed, landed) with their callers.

### 3. Transverse methodologies: `appliesTo`, coupled by events

A methodology may declare `appliesTo: [sdlc, ...]`: its processes run **alongside** the changes of those
methodologies, on the same change. The two methodologies are not orchestrated: they are **choreographed** by the
events of the change they share, and by its state.

- **Events.** Besides the change and process events, the engine publishes `step.completed` when a step of a process
  completes (`{path, process, name, action, method}`), so another methodology can react to each step, asynchronously.
- **Subscriptions.** A transverse methodology declares what it reacts to: `on: [{event, filter}]` (a CEL filter over
  `event`: `{type, change, process, step, items}`); default: a process attached to the change, a step completed, a
  risk or an action added by someone else. Its own productions never wake it.
- **One companion run per change and process**, run again in place for each matching event, with the event in
  `vars.event` (`id`, `at` in milliseconds, and what the event carries): its conditions and prompts work on *this*
  event — `risks_reviewed` holds when a review was written after it (`a.at >= vars.event.at`: items carry `at`), the
  prompt names the step that completed. Events that arrive while it works wait in its **inbox** and are handled one
  after the other: no step goes unreviewed.
- **Back to the served methodology, through the state.** A step may wait for what the transverse process establishes
  (sdlc's `release` needs `risks_under_control`). Until then the served process is stuck (the progress shows the step
  blocked with what it needs); a process stuck on a change is **tried again when the change moves** because of
  someone else (a step completed, a process completed, an item added), so it resumes as soon as the risks are
  mitigated. Signals (`change.signal`, ADR 0031) remain for what must wake a process that is not stuck.

A transverse methodology has no namespace of its own: it acts in the namespace of the change.

`methodologies/risk-management.yaml` applies to `sdlc` and reacts to the steps of `software_delivery` and
`release_train`: at each step it reviews the risks that step introduces or changes (an LLM risk analyst, or by hand),
and has an action defined for every high one, under the roles `risk_manager` (responsible) and `product_owner`
(accountable).

### 4. Visualizers

- **Process graph** (methodology editor): the steps of a process as a graph whose edges are the **conditions** that
  chain them (a step whose exit criteria meet another step's entry), phases as groups, nested processes and methods as
  their own nodes; selecting a method focuses on it (its context, guidance, references, agent and the agent's actions).
  The registry serves the compiled graph (`GetProcessGraph`).
- **Graph explorer**: the whole graph of the heads of every namespace (organisation, platform, methodology, domains'
  data), laid out by a force simulation (d3-force), zoomable, filterable by namespace and type; selecting a node
  highlights its neighbourhood and opens it.

## Consequences

- Risks and actions reuse the item machinery (log, provenance, flows): no new table.
- The brief adds a bounded number of tokens to every LLM action on a change; its lines are stable, so prompts stay
  cacheable across calls of a run.
- A companion run is one process per change and transverse process, re-run in place; its runs are journaled like any
  other.
