SERVICES := graph registry engine modelgw preferences conversations credentials indexer events gateway mcp connector-localfs connector-workspace connector-claude-coding-agent goap-dev goap-runner goap-eval
COMPOSE  := docker compose -f deploy/compose/docker-compose.yml
# the modules of the go.work (ADR 0125): `./...` at the root matches none, every Go command names them
MODULES  := contracts lib graph engine mcp gateway platform
PKGS     := $(foreach m,$(MODULES),./$(m)/...)
export PATH := $(PATH):$(shell go env GOPATH)/bin

.PHONY: help all build test test-wasm tidy web-test test-pg lint eval generate tools up down logs dev dev-backend dev-web dev-reset web runner-image claude-agent-runner

.DEFAULT_GOAL := help

help: ## list the targets and what they do (the default target)
	@echo "Usage: make <target>"
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | sed -E 's/^([a-zA-Z_-]+):.*## (.*)/\1\t\2/' | awk -F'\t' '{ printf "  %-20s %s\n", $$1, $$2 }'

all: generate build test ## generate, build, test

tools: ## install code generators (and air)
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
	go install github.com/air-verse/air@latest

generate: ## regenerate connect-rpc code and the web's TypeScript (web/src/gen) from proto/, and the catalogue of operations (contracts/operations)
	@test -x web/node_modules/.bin/protoc-gen-es || (cd web && npm ci --ignore-scripts)
	buf lint
	buf generate
	go generate ./contracts/operations/

build: ## build all services into bin/
	@mkdir -p bin
	@for s in $(SERVICES); do go build -o bin/$$s ./platform/cmd/$$s || exit 1; done

test: ## go test of every module of the go.work
	go test $(PKGS)

test-wasm: ## every go test with the scripts run by the wasm JavaScript engine (GOAP_JS_ENGINE=wasm, ADR 0132)
	GOAP_JS_ENGINE=wasm go test -count=1 $(PKGS)

tidy: ## go mod tidy in every module
	@for m in $(MODULES); do (cd $$m && go mod tidy) || exit 1; done

web-test: ## unit tests of the web (needs npm install in web/)
	cd web && npm test

PGTEST_IMAGE ?= pgvector/pgvector:pg17
PGTEST_PORT  ?= 55432

test-pg: ## every test, the PostgreSQL ones included (GOAP_TEST_PG_DSN, else a throw-away pgvector container on PGTEST_PORT, 55432)
	@if [ -n "$$GOAP_TEST_PG_DSN" ]; then go test -count=1 $(PKGS); else \
	  c=$$(docker run -d --rm -e POSTGRES_USER=goap -e POSTGRES_PASSWORD=goap -e POSTGRES_DB=goap -p $(PGTEST_PORT):5432 $(PGTEST_IMAGE)) || exit 1; \
	  trap "docker stop $$c >/dev/null" EXIT; \
	  until [ "$$(docker logs $$c 2>&1 | grep -c 'ready to accept connections')" -ge 2 ]; do sleep 1; done; \
	  GOAP_TEST_PG_DSN="postgres://goap:goap@localhost:$(PGTEST_PORT)/goap?sslmode=disable" go test -count=1 $(PKGS); fi

# offline evaluation of LLM work (ADR 0099) against a running goap-dev or gateway: GOAP_TOKEN, GOAP_EVAL_URL, EVAL (cases)
EVAL ?= evals
eval: ## offline evaluation cases of evals/ (or EVAL) against a running gateway, as GOAP_TOKEN
	go run ./platform/cmd/goap-eval $(EVAL)

lint: ## go vet + gofmt + tidy go.mod files + dsl.md sync check
	go vet $(PKGS)
	gofmt -l $(MODULES) | (! grep .)
	@for m in $(MODULES); do (cd $$m && go mod tidy -diff >/dev/null) || { echo "$$m/go.mod is not tidy: make tidy"; exit 1; }; done
	@cmp -s docs/dsl.md web/src/lib/help/dsl.md || (echo 'web/src/lib/help/dsl.md is out of sync with docs/dsl.md' && exit 1)

dev: ## on this machine, no docker: SQLite (.goap/goap.db), backend + IDE both reload on code change: http://localhost:5173
	@command -v air >/dev/null 2>&1 || go install github.com/air-verse/air@latest
	@trap 'kill 0' EXIT INT TERM; \
	$(MAKE) dev-backend & \
	$(MAKE) dev-web & \
	wait

dev-backend: ## SQLite backend only, rebuilt and restarted by air on .go changes: http://localhost:8080
	air -c .air.toml

dev-web: ## IDE only, hot-reloaded by vite, proxied to the backend: http://localhost:5173
	cd web && npm install --no-audit --no-fund && npm run dev

dev-reset: ## drop the local SQLite database (demo data and methodologies are seeded again)
	rm -rf .goap

# static build of the IDE, served by goap-dev itself (when web/dist exists; `make dev` serves it from vite instead)
web/dist/index.html: web/package.json web/index.html web/vite.config.ts $(shell find web/src -type f 2>/dev/null)
	cd web && npm install --no-audit --no-fund && npm run build

web: dev-web ## same as dev-web

runner-image: ## image of the script sandboxes (goap-runner)
	docker build --build-arg SERVICE=goap-runner -t goap/runner:dev .

claude-agent-runner: ## install the Claude Agent SDK of the claude-coding-agent connector (GOAP_CLAUDE_AGENT_DIR, default .goap/claude-agent; needs npm)
	go run ./platform/cmd/connector-claude-coding-agent install

# Engine socket for the docker-proxy service (docker / podman / Docker Desktop)
export GOAP_DOCKER_SOCK ?= $(shell ./deploy/compose/docker-sock.sh)

up: runner-image ## start the docker compose stack, then print how to reach its database (DBeaver)
	$(COMPOSE) up --build -d
	@echo
	@echo "PostgreSQL of the stack (DBeaver: New connection > PostgreSQL):"
	@echo "  host localhost, port 5432, database goap, user goap, password goap"
	@echo "  JDBC URL: jdbc:postgresql://localhost:5432/goap"
	@echo "  one schema per service (graph, engine, modelgw, preferences, conversations, mcp, index, credentials),"
	@echo "  each also reachable as its own role, password = name (e.g. graph / graph)"

down: ## stop the docker compose stack
	$(COMPOSE) down

logs: ## follow the logs of the docker compose stack
	$(COMPOSE) logs -f engine graph registry modelgw gateway
