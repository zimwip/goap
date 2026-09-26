# GOAP — notes for contributors and agents

- **English is the project's official language.** All code, comments, docs, commit messages, and UI text must be in English.
- Architecture and concepts: `docs/architecture.md`. Keep it in sync with code changes.

## Mental model: who, why, how, what, with what

GOAP manages the whole scope of an enterprise. **Organisations work on the Domain to change it as their needs
require. The need is the intent of a Change; how they perform it is the Methodology; its actions say which tools
they use.** Every design decision starts by asking which of these questions it answers, and lives in that concept.

| Question | Concept | What it is | Where it lives |
|---|---|---|---|
| **WHO** | **Organisation** | Who acts, and the scope of responsibility. A hierarchy of units (`OrgUnit`, `part_of`); the default unit `ORG-DEFAULT` is the root of every unit. A unit holds changes and the adapters of its tools. | graph, `organisation` namespace |
| **WHY** | **Change** (intent) | The reason to act, and the blackboard of the execution: everything that modifies the graph goes through one. It has an intent, an owner unit, a methodology, and acts on one namespace. Agents and actions work on it. Pure questions about the world state need none. | `domain.ChangeSet` |
| **HOW** | **Methodology** | How a change is performed: goals, conditions, agents, actions, triggers. | registry (structured), projected on the graph |
| ↳ | **Agent** | A broad task scope that needs planning and loops to be achieved (goap / utility / hybrid planner); may call sub-agents. | methodology |
| ↳ | **Action** | The smallest task, not splittable: preconditions, effects, cost. Kinds `llm`, `tool`, `human`, `builtin`, `script`. It states the tools (MCPs) it uses. | methodology |
| **WHAT** | **Domain** | What is being changed: node types, links, lifecycles, algorithms (`alm`, `organisation`, `platform`, ...). The versioned graph is its content. | `domains/`, graph |
| **WITH WHAT** | **MCP, Connector, Adapter** | **MCP**: the generic usage of a tool by an LLM (`platform` namespace). **Connector**: the driver of a real service, a separate service that registers itself. **Adapter**: the code that implements the tools an MCP expects with the operations a connector exposes. It is an algorithm of the domain library (usage `adapter`, template generated from the MCP and the connector); a unit holds an *instance* with its parameter values (secrets as references), where organisation, MCP and connector converge (`Adapter` node, `organisation` namespace, owned by the unit). | graph + connector registry (MCP hub) |

Design rules that follow, to keep the whole consistent:

1. **Keep the concepts independent; couple only at the designated meeting points.** A methodology knows no
   organisation and no connector (it names MCPs; it references domains with `domainRef`). An MCP knows no connector
   and no adapter. A connector knows no MCP and no organisation. A domain knows no methodology. The **Adapter** is
   the only place where organisation, MCP and connector meet; the **Change** is the only place where who, why, how
   and what meet at run time.
2. **Every modification is a Change.** No write to the graph outside one (the direct-write endpoints are for
   seeding and non-controlled nodes only), and every change names its intent (why), its holding unit (who), its
   methodology (how) and its namespace (what).
3. **Responsibility flows down the organisation.** Anything an organisation provides (adapters today, policies and
   quotas tomorrow) is resolved from the unit holding the change, then its ancestors, then `ORG-DEFAULT`: the nearest
   wins. Units share an MCP and a connector with different scopes (same file access, different root directory).
4. **What describes the enterprise is graph data, changed through changes** (organisation, adapters, MCPs, domains,
   methodologies): versioned, reviewable, journaled. Service configuration and runtime registrations (a connector
   announcing itself and its liveness) are the only exceptions.
5. **What is available comes from who.** An action is schedulable only where the unit holding the change resolves the
   MCPs it declares (`mcps:`); an agent may declare MCPs for its llm / script actions. ABAC decides what a principal
   may do; adapters decide what the organisation can reach.
6. **Agents plan, actions execute.** An action does one thing; anything that needs a loop, a choice or planning is an
   agent over smaller actions.
7. **Library and instance.** Whatever is reusable across organisations is written once in the domain library (an
   algorithm with declared parameters, ADR 0018) and instantiated where it is used with parameter values: an adapter
   is code in the library, and each unit gives it its own scope (same adapter, another root directory).
8. **Adding a capability**: name the question it answers, put it in that concept, and cross concepts only at the
   meeting points above. Adding a real service is a new connector (a service that registers itself), never a change to
   the platform.

## Working notes

- Go module `github.com/zimwip/goap`; core logic in `pkg/` (no infra deps), services in `internal/` + `cmd/`.
- Contracts: `proto/` → `make generate` (buf) → `gen/` (committed, never edit by hand).
- Tests: `make test`; PostgreSQL-backed tests (graph, registry, iam) run when `GOAP_TEST_PG_DSN` is set (`internal/pgtest`); SQLite variants always run.
- Storage has two SQL dialects: PostgreSQL (`migrations/`) and SQLite for the local mode (`migrations_sqlite/`, `make devlocal`, ADR 0010); schema changes go to both.
- `make lint` must pass (go vet + gofmt).
- Example methodologies reference the shared domains of `domains/` (`alm`, `platform`, `organisation`; loaded in tests with `methodology.LoadFile`). `methodologies/impact-analysis.yaml` is exercised by `pkg/methodology` and `pkg/engine` tests; `methodologies/sdlc.yaml` (ALM domain, seeded by `internal/graphsvc/seed.go`) by `pkg/engine/sdlc_test.go`.
- Versioning rule of the graph: outgoing links belong to the source node version (see `pkg/graph/apply.go`).
- Authorization is ABAC (Casbin) via `authz.Authorizer`; default policies in `pkg/authz/casbin.go`, stored by the iam service.
- Methodologies live in the registry database (structured); YAML is import/export only. The object part (node/link types) can be a shared `Domain` (ADR 0013): a methodology references it with `domainRef`, and its NodeTypes are keyed `D:<domain>/nodetype/<name>` on the graph.
- Agents (goap/utility/hybrid planners) and script actions (JS via goja, Go via yaegi) use the DSL in `pkg/dsl` (docs/dsl.md); scripts run in sandboxes (`internal/sandbox`, `GOAP_SANDBOX`).
- Algorithms (ADR 0018): the DSL is tied to a usage (`pkg/algo`, `pkg/dsl/algo.go`); shared domains declare algorithms + instances that node types (validators) and lifecycle transitions (guards, actions) plug; the graph embeds them resolved in NodeType nodes and runs them (`pkg/graph/algorithms.go`). Only shared domains carry them.
- Telemetry: `internal/telemetry` (OpenTelemetry); keep span / attribute names stable (docs/architecture.md §3.7).
- Execution journal (ADR 0011): the engine records ticks / actions / approvals on the change (`domain.ExecutionRecord`); items carry `execution`. Published methodologies are projected onto the graph (`pkg/metamodel`, keys `M:<methodology>/<type>/<name>`); the observer (`methodologies/methodology-improvement.yaml`, `pkg/observe`) turns runs into methodology drafts.
- `web/src/lib/help/dsl.md` is a copy of `docs/dsl.md` (the web dev container only mounts `web/`); `make lint` checks they are identical.
