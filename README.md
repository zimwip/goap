# GOAP — agentic platform for enterprise methodologies

GOAP lets you deploy **enterprise methodologies** (impact analysis, requirements management,
reviews…) as declarative definitions, executed by an agentic engine inspired
by [Embabel](https://github.com/embabel/embabel-agent): **intent loop**, **GOAP (A\*) planning**,
action execution (LLM, human, code, MCP tools), and **replanning** after each action.

The engine's blackboard is the **change axis** of a versioned knowledge graph whose
**domain axis** carries the reference content. Conditions are
[CEL](https://cel.dev) expressions on the state of the change, hydrated with the domain
elements it references.

👉 **Read first: [docs/architecture.md](docs/architecture.md)** · decisions: [docs/adr](docs/adr)

## Quick start

```bash
# 1. Local, without docker: a single process, SQLite (.goap/goap.db), IDE included
make devlocal                 # IDE + API on http://localhost:8080 (Go + Node required)
make devlocal-reset           # start from an empty database (demo and methodologies re-imported)

#    in-memory variant, hot reload of the IDE
make dev                      # Connect API on http://localhost:8080
make web                      # UI on http://localhost:5173 (separate terminal)

# 2. Full stack: postgres, nats, vault, services, web
ANTHROPIC_API_KEY=... make up # without a key: "fake" LLM provider
```

Provided methodologies (`methodologies/`, imported at startup): `impact-analysis`, `test-design`,
`sdlc` (development cycle on the ALM domain: need → requirement → function → component → build
artifact → application → solution, data, interfaces, flows; releases and dev → test → staging →
production deployment) and `methodology-improvement` (self-observation).

Without an API key, the `fake` provider returns empty responses: LLM actions fail to produce
their effects, get disabled, and the planner falls back to human actions — handy for
observing replanning.

Example call (Connect JSON protocol, via the gateway):

```bash
curl -s localhost:8080/goap.graph.v1.GraphService/ListBaselines -H 'Content-Type: application/json' -d '{}'
curl -s localhost:8080/goap.engine.v1.EngineService/StartProcess -H 'Content-Type: application/json' \
  -d '{"methodology":"impact-analysis","baselineId":"<id>","intent":"The PSP is moving to API v2: what does this break?"}'
```

## Services

| Service | Port (compose) | Role |
|---|---|---|
| gateway | 8080 | entry point, authentication (none / HS256 + dev tokens), Connect routing |
| graph | 8081 | domain axis (versioned nodes, version-to-version links, baselines) + change axis |
| registry | 8082 | methodologies structured in the database (draft → published), YAML import/export |
| engine | 8083 | agentic processes: intent → planning → execution |
| modelgw | 8084 | multi-provider LLM gateway (Anthropic, OpenAI-compatible, fake) |
| iam | 8086 | ABAC access control (Casbin), policies in the database |
| mcp | — | skeleton (API defined, not implemented) |
| goap-runner | — | script action sandbox (one container / pod / process per execution) |
| otel-collector, jaeger, prometheus, grafana | 4318, 16686, 9090, 3000 | OpenTelemetry observability |

## Development

```bash
make tools      # buf + protoc plugins
make generate   # proto/ -> gen/
make test       # unit tests
make test-pg    # graph PostgreSQL repository tests
make lint
```

Script action DSL: [docs/dsl.md](docs/dsl.md).

Stack: Go 1.26 · Echo · connect-rpc · NATS JetStream · PostgreSQL · Vault · Svelte 5.
