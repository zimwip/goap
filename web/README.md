# GOAP — web workshop (IDE)

Svelte 5 + Vite + TypeScript interface for the GOAP platform, organized as an
IDE (VS Code style): methodology design (agents, actions, conditions,
goals, domain), intent testing, live execution monitoring, change review,
repositories, triggers, and access policies. A conversational **assistant**
lets non-specialists phrase a request in plain language.

## Layout

```
┌──────────────────────────────── Header ───────────────────────────────────┐
│ GOAP Workshop  [ Search (Ctrl+P) — "> " : commands ]         ● live 👤     │
├──────────────────────────────── Toolbar ──────────────────────────────────┤
│ actions of the active editor (Save, Validate, Publish…)        global     │
├──┬──────────────┬──────────────────────────────────────┬──────────────┬──┤
│A │ Navigation   │ Editing tabs                          │ Right panel │A │
│c │ (activity    │                                      │ (Test,       │c │
│t │  bar tool)   │  active tab's editor                 │  Properties, │t │
│. │              ├──────────────────────────────────────┤  DSL Help)   │. │
│  │              │ Console: Events · Logs ·              │              │  │
│  │              │ Problems · Tokens                     │              │  │
├──┴──────────────┴──────────────────────────────────────┴──────────────┴──┤
│ ● Platform OK   live stream      2 runs in progress   👤 dev  🔔 3        │
└──────────────────────────────── Status bar ───────────────────────────────┘
```

- **Header**: object search (open tabs, methodologies, agents,
  runs, repositories, changes); `>` prefix for the command
  palette. User menu: identity (`WhoAmI`), access token (stored in
  `localStorage`, key `goap.token`, sent as `Authorization: Bearer …`), theme
  (system / light / dark).
- **Toolbar**: contextual actions of the active editor (methodology:
  Save, Validate, Publish, Export, New version, Reload,
  Delete / Archive; run: Refresh, Trace, Change, Parent…),
  plus "New intent test" and panel toggles.
- **Left activity bar** → navigation panel (resizable,
  collapsible):
  - **Assistant**: plain-language conversation (also available as a tab);
  - **Methodologies**: methodology → version → Agents / Actions / Conditions /
    Goals / Domain (problem counters, ● changes, `+` to add);
  - **Runs**: processes grouped by status, sub-agents nested under
    their parent;
  - **Triggers**: status of published agents' triggers (`ListTriggers`),
    "Fire" button (`FireTrigger`) that opens the created run;
  - **Repository**: baselines → nodes by type;
  - **Changes**; **Access** (ABAC policies).
- **Tabbed editing area**: each object opens in a tab. A single click
  opens a **preview** (italicized title) replaced by the next
  selection; a tab becomes **pinned** on double click (on the tab or in
  the explorer) or as soon as its content is modified; the pin icon
  unpins it. ● signals unsaved changes; middle click closes it;
  drag and drop to reorder. Tabs are remembered (`localStorage`).
- **Console** (resizable, collapsible): **Events** (live
  `WatchEvents` stream, click → run), **Logs** (filter by level and
  process), **Problems** (validation of the active draft, click → relevant
  field), **Tokens** (one LLM call per row: time, process, agent,
  action, model, input / output, duration, totals).
- **Right activity bar**: **Test** (optional methodology and agent,
  repository, intent → "Send" = `StartProcess`; identification
  candidates, clarification question and answer; opens the run),
  **Properties** (details of the selected object), **DSL Help** (`docs/dsl.md`).
- **Status bar**: platform health (`GET /api/status` every 15 s,
  per-service detail on click), my running runs (`ListProcesses`
  `{mine, rootsOnly, statuses}` + stream; list on click), identity, and
  **notifications** (completion, failure, blocked, pending input or approval,
  clarification, triggered launch; last 100, remembered).

Below 1024 px wide, the side panels overlay the editor.

## Editors

- **Methodology**: general and domain (node and link types), content.
- **Agent**: name, description, examples, planner (`goap`, `utility`,
  `hybrid`), admissible actions and goals (empty list = all), and
  **triggers** (event or UTC cron schedule, CEL filter, goal,
  intent, target, roles, enabled).
- **Action**: all fields; `script`: code editor (CodeMirror)
  JavaScript / Go with syntax highlighting and `ctx.` API completion; `llm`: prompt
  editor; `utility`: numeric CEL expression.
- **Condition** (CEL expression), **Goal**.

Agents, actions, conditions, and goals are edited in their
methodology@version's in-memory **draft**, shared by all its tabs:
saving from any of them saves the entire methodology
(`SaveMethodology`). The draft is automatically validated after each
change. Renaming updates references (pre / effects, agents,
triggers). Published or archived versions are read-only.

- **Run** (live via `WatchEvents`, falling back to `GetProcess` every
  2 s if the stream fails): status, agent, planner, goal, initiator,
  "triggered by", totals (tokens, LLM / tool calls), **Trace** link to
  Jaeger, parent / sub-agents, plan, world state, steps (usage, LLM calls
  and tool calls, logs, sandbox, sub-agents), pending task (input,
  approval, waiting on a sub-agent), and intent dialog.
- **Change**, **Repository**, **Access policies**, **YAML import**.

## Assistant

For non-specialist users: cards of available agents with
their example requests, free-text entry → `StartProcess` without a methodology
(identification among all published agents, on the most recent
repository — configurable in settings). The conversation follows the run
live with readable messages (action descriptions), asks clarification
questions as buttons, shows human tasks and
approvals in the conversation, follows sub-agents, and summarizes the result
(artifacts, impacts, proposals, tokens, "View details"). History is
kept in the browser; "New conversation" resets it.

## Shortcuts

| Shortcut                     | Action                                   |
| ---------------------------- | ---------------------------------------- |
| `Ctrl`/`Cmd` + `S`           | save the active editor                   |
| `Ctrl`/`Cmd` + `W`, `Alt`+`W` | close the tab (`Alt`+`W` if the browser intercepts `Ctrl`+`W`) |
| `Ctrl`/`Cmd` + `J`           | show / hide the console                  |
| `Ctrl`/`Cmd` + `B`           | show / hide navigation                   |
| `Ctrl`/`Cmd` + `P` (or `K`)  | search; `>` for commands                 |
| `Ctrl` + `Space`             | completion in the code editor            |
| `Ctrl` + `Enter`             | send the intent (Test)                   |
| double click                 | pin a tab / open a pinned one            |
| middle click                 | close a tab                              |
| arrow keys                   | navigate trees, tabs, and activity bars; resize a focused splitter |

## Getting started

Prerequisites: Node.js ≥ 20.19 (or ≥ 22.12) and an accessible GOAP gateway
(or `go run ./cmd/goap-dev` at the repository root).

```sh
cd web
npm install
npm run dev          # http://localhost:5173
```

In development, Vite proxies `/goap.*` (Connect RPC) and `/api` (platform
status) to the gateway, defaulting to `http://localhost:8080`:

```sh
GOAP_GATEWAY_URL=http://gateway:8080 npm run dev
```

Optional build variables:

| Variable               | Role                                                    |
| ---------------------- | ------------------------------------------------------- |
| `VITE_GOAP_BASE_URL`   | base URL of the gateway (default: relative)              |
| `VITE_GOAP_JAEGER_URL` | Jaeger base URL for "Trace" links (default `http://localhost:16686`) |

## Scripts

| Command           | Role                                             |
| ----------------- | ------------------------------------------------ |
| `npm run dev`     | development server with proxy                    |
| `npm run build`   | production build into `dist/`                    |
| `npm run check`   | type checking (`svelte-check`)                   |
| `npm run preview` | serves the build locally                         |

## Protocol

- Unary RPCs: Connect JSON (`POST /{package.Service}/{Method}`), without
  code generation (`src/lib/api.ts`). `int64` values arrive as strings (proto3
  JSON): `int()` converts them.
- `EngineService.WatchEvents` server stream (`src/lib/stream.ts`): `fetch` +
  `ReadableStream`, `Content-Type: application/connect+json`, Connect envelopes
  (1 flag byte + 32-bit big-endian length + JSON; flag
  `0x02` = end of stream, possible error), `AbortController`, exponential-backoff
  reconnection. The server only sends headers with the first
  event: an idle stream stays "pending", which is normal.

## Organization

- `src/lib/shell/`: the IDE's mini-framework — view registry
  (`registerView({id, zone: 'left'|'right'|'bottom'|'editor', title, icon, component})`),
  persisted layout (`layout.svelte.ts`), preview / pinned tabs
  (`tabs.svelte.ts`), contextual actions and highlights
  (`workbench.svelte.ts`), shell components (header, bars, panels,
  tabs, console, status bar).
- `src/lib/views/`: registered views (`index.ts`) — `nav/` (explorers),
  `editors/` (tabs), `bottom/` (console), `right/` (tools), `assistant/`.
- `src/lib/stores/`: methodology drafts, catalogs, live data
  (events, processes, logs, tokens), identity, notifications, platform
  status, assistant.
- `src/lib/api.ts`, `src/lib/stream.ts`: Connect client (unary and stream).
- `src/lib/methodologyForm.ts`: editing model of a methodology ↔ proto message.
- `src/lib/codemirror.ts`, `src/lib/dsl.ts`: code editor and DSL completion.
- `src/lib/help/dsl.md`: **copy** of `docs/dsl.md` embedded at build time (the
  development container only mounts `web/`) — to be resynced whenever
  `docs/dsl.md` changes.
- `src/lib/components/`: shared components (human task forms,
  approval, condition rows, step timeline…).

CodeMirror is loaded on demand (a separate bundle chunk).

## TypeScript version

TypeScript is deliberately constrained to `^6`: `svelte-check` (4.7) declares
`typescript: ^5.0.0 || ^6.0.0` as a peer dependency. Only move to TypeScript 7
once a compatible version of `svelte-check` is published.
