# GOAP — notes for contributors and agents

- **English is the project's official language.** All code, comments, docs, commit messages, and UI text must be in English.
- Architecture and concepts: `docs/architecture.md`. Keep it in sync with code changes.

## Mental model: who, why, how, what, with what

GOAP manages the whole scope of an enterprise. **Organisations work on the Domain to change it as their needs
require. The need is the intent of a Change; how they perform it is the Methodology; its actions say which tools
they use.** Every design decision starts by asking which of these questions it answers, and lives in that concept.

| Question | Concept | What it is | Where it lives |
|---|---|---|---|
| **WHO** | **Organisation** | Who acts, and the scope of responsibility. A hierarchy of units (`OrgUnit`, `part_of`); the default unit `ORG-DEFAULT` is the root of every unit. A unit holds changes, the adapters of its tools and its users; who may do what (`Policy` nodes, ADR 0020) lives here too. | graph, `organisation` namespace |
| **WHY** | **Change** (intent) | The reason to act, and the blackboard of the execution: everything that modifies the graph goes through one. It has an intent, an owner unit, a methodology, and acts on one namespace. Agents and actions work on it. Pure questions about the world state need none. | `domain.Change` |
| **HOW** | **Methodology** | How a change is performed: goals, conditions, agents, actions, triggers. | graph, `methodology` namespace (`MethodologyVersion` + element nodes typed by the `methodology` meta-domain, ADR 0023) |
| ↳ | **Agent** | A broad task scope that needs planning and loops to be achieved (goap / utility / hybrid planner); may call sub-agents. | methodology |
| ↳ | **Action** | The smallest task, not splittable: preconditions, effects, cost. Kinds `llm`, `tool`, `human`, `builtin`, `script`. It states the tools (MCPs) it uses. | methodology |
| **WHAT** | **Domain** | What is being changed: node types, links, lifecycles, algorithms (`alm`, `organisation`, `platform`, ...). One domain per namespace; a type is referenced as `<namespace>@<NodeType>` (ADR 0012, 0013). The versioned graph is its content. | `domains/`, registry database (`domain_version`); each graph holds the published ones in memory (type catalogue) |
| **WITH WHAT** | **MCP, Connector, Adapter** | **MCP**: the generic usage of a tool by an LLM (`platform` namespace). **Connector**: the driver of a real service, a separate service that registers itself. **Adapter**: the code that implements the tools an MCP expects with the operations a connector exposes. It is an `AdapterDef` node of the `platform` namespace (usage `adapter`, template generated from the MCP and the connector), changed through a Change; a unit holds an *instance* with its parameter values (secrets as references), where organisation, MCP and connector converge (`Adapter` node, `organisation` namespace, owned by the unit). | graph + connector registry (MCP hub) |

Design rules that follow, to keep the whole consistent:

1. **Keep the concepts independent; couple only at the designated meeting points.** A methodology knows no
   organisation and no connector (it names MCPs; it references the types of domains, `alm@Requirement`). An MCP knows no connector
   and no adapter. A connector knows no MCP and no organisation. A domain knows no methodology. The **Adapter** is
   the only place where organisation, MCP and connector meet; the **Change** is the only place where who, why, how
   and what meet at run time.
2. **Every modification is a Change.** No write to the graph outside one (the direct-write endpoints are for
   seeding and non-controlled nodes only), and every change names its intent (why), its holding unit (who), its
   methodology (how) and its namespace (what).
3. **Responsibility flows down the organisation.** Anything an organisation provides (adapters today, policies and
   quotas tomorrow) is resolved from the unit holding the change, then its ancestors, then `ORG-DEFAULT`: the nearest
   wins. Units share an MCP and a connector with different scopes (same file access, different root directory).
4. **What describes the enterprise is graph data, changed through changes** (organisation, adapters, MCPs,
   methodologies): versioned, reviewable, journaled. The exceptions: the domains, which define the graph itself (the
   registry versions them in its database, each graph holds them in memory, ADR 0023), service configuration and
   runtime registrations (a connector announcing itself and its liveness).
5. **What is available comes from who.** An action is schedulable only where the unit holding the change resolves the
   MCPs it declares (`mcps:`); an agent may declare MCPs for its llm / script actions. ABAC decides what a principal
   may do; adapters decide what the organisation can reach.
6. **Agents plan, actions execute.** An action does one thing; anything that needs a loop, a choice or planning is an
   agent over smaller actions.
7. **Library and instance.** Whatever is reusable across organisations is written once in the domain library (an
   algorithm with declared parameters, ADR 0018) and instantiated where it is used with parameter values: an adapter
   is a definition (`AdapterDef`), and each unit gives it its own scope (same adapter, another root directory).
8. **Adding a capability**: name the question it answers, put it in that concept, and cross concepts only at the
   meeting points above. Adding a real service is a new connector (a service that registers itself), never a change to
   the platform.

## Working notes

- Go module `github.com/zimwip/goap`; core logic in `pkg/` (no infra deps), services in `internal/` + `cmd/`.
- Contracts: `proto/` → `make generate` (buf) → `gen/` (committed, never edit by hand).
- Tests: `make test`; PostgreSQL-backed tests (graph) run when `GOAP_TEST_PG_DSN` is set (`internal/pgtest`); SQLite variants always run.
- Storage has two SQL dialects: PostgreSQL (`migrations/`) and SQLite for the local mode (`migrations_sqlite/`, `make devlocal`, ADR 0010); schema changes go to both.
- `make lint` must pass (go vet + gofmt).
- Example methodologies use the domain of `domains/` (`alm`) and the built-in `organisation` and `platform` (loaded in tests with `methodology.LoadFile`). `methodologies/examples/` (`impact-analysis`, `test-design`) is exercised by `pkg/methodology`, `pkg/engine` and registry tests and is not seeded at start (the registry seeds the `*.yaml` directly under `methodologies/`); `methodologies/sdlc.yaml` (ALM domain, seeded by `internal/graphsvc/seed.go`) by `pkg/engine/sdlc_test.go`.
- Versioning rule of the graph: outgoing links belong to the source node version (see `pkg/graph/apply.go`).
- Versions and branches (ADR 0032): `node_version.branch` is the branch a version was written on and never changes; a merge that lands a version as is makes it join the target (`Tx.JoinBranch`, `node_branch`), only a node changed on both sides gets a merge version. Ask "is it on branch X" with `LatestOn` / `Node.On`, not the label. Baselines are stored as deltas from their parent with checkpoints (`pkg/graph/baselinedelta.go`); callers still pass and get whole maps.
- Options (ADR 0009 §3, ADR 0032 §6): an option is a flow opened as a hypothesis (`pkg/graph/option.go`); the change's active option is where every graph call that names no flow goes (`Change.ResolveFlow` at the public entry points: `AddNodes`, `WriteNode`, `ReviewNodeOn`, `AddItems`, `BlackboardIn`, `ValidateBoard`, `ChangeView`, `ChangeGraph`); `domain.MainFlow` ("main") names the main flow explicitly, internal code keeps passing "" for it.
- Decision points (ADR 0009 §4): facts of the main flow (`domain.KindDecisionPoint` + `DecisionEvent`, replayed by `Change.DecisionPointsAt(now)`), written only through `Graph.OpenDecision` / `RuleDecision` / `AnswerQuestion` / `RatifyDecision` (`AddItems` refuses the kind); actions reach them through items of kind `decisionPoint` (`pkg/engine/decisions.go`) and the builtin `decision.investigate`. Conditions see `options`, `activeOption`, `decisionPoints`, `questions`; the platform conditions (`condition.Platform`: `open_questions`, `no_decision_pending`...) are added to every compiled methodology unless it declares the name.
- Authorization is ABAC (Casbin) via `authz.Authorizer`; the rules are `Policy` nodes and the callers `User` nodes of the `organisation` namespace, read by `pkg/access` (compiled-in defaults `pkg/authz/casbin.go` seed them and apply while the graph has none; administrators are never locked out). There is no IAM service.
- Node types (ADR 0012, 0013): a node's type is composite, `<namespace>@<NodeType>` (one domain per namespace: `alm`, ...; shipped with the code and frozen, `domains/builtin/`: the meta-domain `methodology`, `organisation` and `platform`; a new namespace is a new domain); every type or link type reference is qualified. The registry is the reference of the types (published domains); the graph and the engine keep an in-memory type catalogue loaded from it at startup and synced by its events (lifecycle, validators, search, editor, `extends`), and the graph refuses an unknown type. Nothing is projected onto the graph.
- Methodologies are graph nodes (ADR 0023): a version node (`MV:`) and one node per element typed by the meta-domain (`methodology@Agent`, ...), kept by `registrysvc.GraphStore`. Domains are the definition of a graph, not graph data: the registry keeps them in its database (`registrysvc.SQLDomainStore`, `internal/registrysvc/migrations*/`). YAML is import/export only.
- Processes (ADR 0034): a methodology's `processes` are trees of steps (sub-steps, `action`, alternative `actions`, `agent`, nested `process`, or manual) sequenced by their conditions only (never by position), with `references` to their reference documents, compiled by `pkg/methodology/process.go` into an agent + goal per process and one action per step (named by its path); agent and nested-process steps run through the builtin `process.step` (`pkg/engine/steps.go`) as sub-agents on the same change.
- Agents (goap/utility/hybrid planners) and script actions (JS via goja, Go via yaegi) use the DSL in `pkg/dsl` (docs/dsl.md); scripts run in sandboxes (`internal/sandbox`, `GOAP_SANDBOX`).
- Algorithms (ADR 0018): the DSL is tied to a usage (`pkg/algo`, `pkg/dsl/algo.go`); domains declare algorithms + instances that node types (validators) and lifecycle transitions (guards, actions) plug; the type catalogue (the graph's copy of the registry's model) resolves them and the graph runs them (`pkg/graph/algorithms.go`). Only domains carry them.
- Node index (ADR 0026): node types declare `search` properties (`pkg/methodology`); `Graph.Observe` publishes `NodeEvent` / `BaselineEvent` after each commit (`pkg/graph/events.go`); `pkg/index` holds the stores (memory, SQLite FTS5, PostgreSQL pgvector — schema changes go to `pkg/index/migrations*/`), `internal/indexersvc` + `cmd/indexer` the service; `goap-dev` runs it in process. PostgreSQL index tests need pgvector (`pgvector/pgvector:pg17` with `GOAP_TEST_PG_DSN`).
- Node editors (ADR 0027): a node type names the IDE editor of its nodes (`editor`, inherited through `extends`); the web opens a node only through `openNode` (`web/src/lib/nodeEditors.ts`), editors are registered with `registerNodeEditor` (`web/src/lib/views/nodeEditors.ts`), unknown or empty: the default node editor.
- Built-in MCPs (ADR 0028): `goap-graph`, `goap-change`, `goap-scheduler`, `goap-admin` (`pkg/mcp/builtin.go`), served by the connectors of `internal/connectors/builtin` inside the hub, acting for the caller; `graphsvc.SeedBuiltins` gives them to every unit through `ORG-DEFAULT`. An MCP has a `scope` (`action`, `agent`: declared on agents only and reached by their llm actions, as `goap-scheduler`; `both` by default), enforced by the engine host and scheduling and checked by the registry. An `organisation@Adapter` node restricts an MCP (`disabled`, `tools`, `deny`, `readOnly`); restrictions add up along the unit chain (`mcp.Restriction`).
- Telemetry: `internal/telemetry` (OpenTelemetry); keep span / attribute names stable (docs/architecture.md §3.7).
- Change impacts are event-sourced (ADR 0029): every operation on them goes through `Graph.emit` (`pkg/graph/impactevents.go`), which appends a `domain.ImpactEvent` to the log of the change and updates the `change_impact` projection; never call `Tx.PutChangeImpact` elsewhere (the graph tests replay every change's log against the projection). A flow's view is `domain.ImpactsSeenBy`, a fold of the log.
- One log per change (ADR 0030): facts, journal records and impact events are entries of `change_log` (one order, `seq`), with `type` (`fact.*`, `journal.*`, `impact.*`), `flow`, `process_id`, `execution`, `subject` as columns to filter on; write and read them through `pkg/graph/changelog.go` (`Tx.AppendLog` / `Tx.Log`), `ListChangeLog` serves the filtered log.
- Execution journal (ADR 0011): the engine records ticks / actions / approvals on the change (`domain.ExecutionRecord`); items carry `execution`. The observer (`methodologies/methodology-improvement.yaml`, `pkg/observe`) turns runs into change impacts on the definition nodes of a methodology draft.
- `web/src/lib/help/dsl.md` is a copy of `docs/dsl.md` (the web dev container only mounts `web/`); `make lint` checks they are identical.
- Svelte 5 rune state modules (`*.svelte.ts`) must not share a base name with a component (`*.svelte`) in the same directory (e.g. `searchOverlay.svelte.ts` next to `SearchOverlay.svelte`): on case-insensitive filesystems (Windows, default macOS), an extensionless import of the state module (`from './foo.svelte'`) resolves to the component file instead, since Vite tries an exact match before extending to `.svelte.ts`. This builds fine on case-sensitive Linux CI and only breaks locally on Windows/macOS. Either name the pair distinctly or always import the state module with its full extension (`from './foo.svelte.ts'`).
