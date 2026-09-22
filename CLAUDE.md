# GOAP — notes for contributors and agents

- Architecture and concepts: `docs/architecture.md` (French). Keep it in sync with code changes.
- Go module `github.com/zimwip/goap`; core logic in `pkg/` (no infra deps), services in `internal/` + `cmd/`.
- Contracts: `proto/` → `make generate` (buf) → `gen/` (committed, never edit by hand).
- Tests: `make test`; PostgreSQL graph tests: `GOAP_TEST_PG_DSN=... go test ./pkg/graph/...`.
- `make lint` must pass (go vet + gofmt).
- The example methodology `methodologies/impact-analysis.yaml` is exercised by `pkg/methodology` and `pkg/engine` tests.
- Versioning rule of the graph: outgoing links belong to the source node version (see `pkg/graph/apply.go`).
