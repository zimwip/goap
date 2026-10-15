SERVICES := graph registry engine modelgw preferences conversations credentials indexer events gateway mcp connector-localfs connector-workspace connector-claude-coding-agent goap-dev goap-runner goap-eval
COMPOSE  := docker compose -f deploy/compose/docker-compose.yml
export PATH := $(PATH):$(shell go env GOPATH)/bin

.PHONY: all build test web-test test-pg lint eval generate tools up down logs dev devlocal devlocal-backend devlocal-web devlocal-reset web runner-image claude-agent-runner

all: generate build test

tools: ## install code generators
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
	go install github.com/air-verse/air@latest

generate: ## regenerate connect-rpc code from proto/ and the catalogue of operations (pkg/operations)
	buf lint
	buf generate
	go generate ./pkg/operations/

build:
	@mkdir -p bin
	@for s in $(SERVICES); do go build -o bin/$$s ./cmd/$$s || exit 1; done

test:
	go test ./...

web-test: ## unit tests of the web (needs npm install in web/)
	cd web && npm test

PGTEST_IMAGE ?= pgvector/pgvector:pg17
PGTEST_PORT  ?= 55432

test-pg: ## every test, the PostgreSQL ones included (GOAP_TEST_PG_DSN, else a throw-away pgvector container on $(PGTEST_PORT))
	@if [ -n "$$GOAP_TEST_PG_DSN" ]; then go test -count=1 ./...; else \
	  c=$$(docker run -d --rm -e POSTGRES_USER=goap -e POSTGRES_PASSWORD=goap -e POSTGRES_DB=goap -p $(PGTEST_PORT):5432 $(PGTEST_IMAGE)) || exit 1; \
	  trap "docker stop $$c >/dev/null" EXIT; \
	  until [ "$$(docker logs $$c 2>&1 | grep -c 'ready to accept connections')" -ge 2 ]; do sleep 1; done; \
	  GOAP_TEST_PG_DSN="postgres://goap:goap@localhost:$(PGTEST_PORT)/goap?sslmode=disable" go test -count=1 ./...; fi

# offline evaluation of LLM work (ADR 0099) against a running goap-dev or gateway: GOAP_TOKEN, GOAP_EVAL_URL, EVAL (cases)
EVAL ?= evals
eval:
	go run ./cmd/goap-eval $(EVAL)

lint:
	go vet ./...
	gofmt -l . | (! grep -v node_modules | grep .)
	@cmp -s docs/dsl.md web/src/lib/help/dsl.md || (echo 'web/src/lib/help/dsl.md is out of sync with docs/dsl.md' && exit 1)

dev: ## single process, in-memory, demo data: http://localhost:8080
	go run ./cmd/goap-dev

devlocal: ## on this machine, no docker: SQLite (.goap/goap.db), backend + IDE both reload on code change: http://localhost:5173
	@command -v air >/dev/null 2>&1 || go install github.com/air-verse/air@latest
	@trap 'kill 0' EXIT INT TERM; \
	$(MAKE) devlocal-backend & \
	$(MAKE) devlocal-web & \
	wait

devlocal-backend: ## SQLite backend only, rebuilt and restarted by air on .go changes: http://localhost:8080
	air -c .air.toml

devlocal-web: ## IDE only, hot-reloaded by vite, proxied to the backend: http://localhost:5173
	cd web && npm install --no-audit --no-fund && npm run dev

devlocal-reset: ## drop the local SQLite database (demo data and methodologies are seeded again)
	rm -rf .goap

# static build of the IDE, served by goap-dev itself (used by `make dev`, not devlocal)
web/dist/index.html: web/package.json web/index.html web/vite.config.ts $(shell find web/src -type f 2>/dev/null)
	cd web && npm install --no-audit --no-fund && npm run build

web: devlocal-web

runner-image: ## image of the script sandboxes (goap-runner)
	docker build --build-arg SERVICE=goap-runner -t goap/runner:dev .

claude-agent-runner: ## install the Claude Agent SDK of the claude-coding-agent connector (GOAP_CLAUDE_AGENT_DIR, default .goap/claude-agent; needs npm)
	go run ./cmd/connector-claude-coding-agent install

# Engine socket for the docker-proxy service (docker / podman / Docker Desktop)
export GOAP_DOCKER_SOCK ?= $(shell ./deploy/compose/docker-sock.sh)

up: runner-image
	$(COMPOSE) up --build -d

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f engine graph registry modelgw gateway
