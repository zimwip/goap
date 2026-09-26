# GOAP — notes for contributors and agents

- **English is the project's official language.** All code, comments, docs, commit messages, and UI text must be in English.
- Architecture and concepts: `docs/architecture.md`. Keep it in sync with code changes.
- Go module `github.com/zimwip/goap`; core logic in `pkg/` (no infra deps), services in `internal/` + `cmd/`.
- Contracts: `proto/` → `make generate` (buf) → `gen/` (committed, never edit by hand).
- Tests: `make test`; PostgreSQL-backed tests (graph, registry, iam) run when `GOAP_TEST_PG_DSN` is set (`internal/pgtest`); SQLite variants always run.
- Storage has two SQL dialects: PostgreSQL (`migrations/`) and SQLite for the local mode (`migrations_sqlite/`, `make devlocal`, ADR 0010); schema changes go to both.
- `make lint` must pass (go vet + gofmt).
- Example methodologies reference the shared domains of `domains/` (`alm`, `metamodel`; loaded in tests with `methodology.LoadFile`). `methodologies/impact-analysis.yaml` is exercised by `pkg/methodology` and `pkg/engine` tests; `methodologies/sdlc.yaml` (ALM domain, seeded by `internal/graphsvc/seed.go`) by `pkg/engine/sdlc_test.go`.
- Versioning rule of the graph: outgoing links belong to the source node version (see `pkg/graph/apply.go`).
- Authorization is ABAC (Casbin) via `authz.Authorizer`; default policies in `pkg/authz/casbin.go`, stored by the iam service.
- Methodologies live in the registry database (structured); YAML is import/export only. The object part (node/link types) can be a shared `Domain` (ADR 0013): a methodology references it with `domainRef`, and its NodeTypes are keyed `D:<domain>/nodetype/<name>` on the graph.
- Agents (goap/utility/hybrid planners) and script actions (JS via goja, Go via yaegi) use the DSL in `pkg/dsl` (docs/dsl.md); scripts run in sandboxes (`internal/sandbox`, `GOAP_SANDBOX`).
- Algorithms (ADR 0018): the DSL is tied to a usage (`pkg/algo`, `pkg/dsl/algo.go`); shared domains declare algorithms + instances that node types (validators) and lifecycle transitions (guards, actions) plug; the graph embeds them resolved in NodeType nodes and runs them (`pkg/graph/algorithms.go`). Only shared domains carry them.
- Telemetry: `internal/telemetry` (OpenTelemetry); keep span / attribute names stable (docs/architecture.md §3.7).
- Execution journal (ADR 0011): the engine records ticks / actions / approvals on the change (`domain.ExecutionRecord`); items carry `execution`. Published methodologies are projected onto the graph (`pkg/metamodel`, keys `M:<methodology>/<type>/<name>`); the observer (`methodologies/methodology-improvement.yaml`, `pkg/observe`) turns runs into methodology drafts.
- `web/src/lib/help/dsl.md` is a copy of `docs/dsl.md` (the web dev container only mounts `web/`); `make lint` checks they are identical.
