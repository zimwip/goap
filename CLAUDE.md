# GOAP — notes for contributors and agents

- Architecture and concepts: `docs/architecture.md` (French). Keep it in sync with code changes.
- Go module `github.com/zimwip/goap`; core logic in `pkg/` (no infra deps), services in `internal/` + `cmd/`.
- Contracts: `proto/` → `make generate` (buf) → `gen/` (committed, never edit by hand).
- Tests: `make test`; PostgreSQL-backed tests (graph, registry, iam) run when `GOAP_TEST_PG_DSN` is set (`internal/pgtest`); SQLite variants always run.
- Storage has two SQL dialects: PostgreSQL (`migrations/`) and SQLite for the local mode (`migrations_sqlite/`, `make devlocal`, ADR 0010); schema changes go to both.
- `make lint` must pass (go vet + gofmt).
- The example methodology `methodologies/impact-analysis.yaml` is exercised by `pkg/methodology` and `pkg/engine` tests.
- Versioning rule of the graph: outgoing links belong to the source node version (see `pkg/graph/apply.go`).
- Authorization is ABAC (Casbin) via `authz.Authorizer`; default policies in `pkg/authz/casbin.go`, stored by the iam service.
- Methodologies live in the registry database (structured); YAML is import/export only.
- Agents (goap/utility/hybrid planners) and script actions (JS via goja, Go via yaegi) use the DSL in `pkg/dsl` (docs/dsl.md); scripts run in sandboxes (`internal/sandbox`, `GOAP_SANDBOX`).
- Telemetry: `internal/telemetry` (OpenTelemetry); keep span / attribute names stable (docs/architecture.md §3.7).
- `web/src/lib/help/dsl.md` is a copy of `docs/dsl.md` (the web dev container only mounts `web/`); `make lint` checks they are identical.
