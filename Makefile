SERVICES := graph registry engine modelgw gateway mcp iam connector-localfs goap-dev goap-runner
COMPOSE  := docker compose -f deploy/compose/docker-compose.yml
export PATH := $(PATH):$(shell go env GOPATH)/bin

.PHONY: all build test test-pg lint generate tools up down logs dev devlocal devlocal-reset web runner-image

all: generate build test

tools: ## install code generators
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest

generate: ## regenerate connect-rpc code from proto/
	buf lint
	buf generate

build:
	@mkdir -p bin
	@for s in $(SERVICES); do go build -o bin/$$s ./cmd/$$s || exit 1; done

test:
	go test ./...

test-pg: ## tests against PostgreSQL (graph, registry, iam)
	GOAP_TEST_PG_DSN=$${GOAP_TEST_PG_DSN:-postgres://goap:goap@localhost:5432/goap?sslmode=disable} go test ./pkg/graph/... ./internal/...

lint:
	go vet ./...
	gofmt -l . | (! grep -v node_modules | grep .)
	@cmp -s docs/dsl.md web/src/lib/help/dsl.md || (echo 'web/src/lib/help/dsl.md is out of sync with docs/dsl.md' && exit 1)

dev: ## single process, in-memory, demo data: http://localhost:8080
	go run ./cmd/goap-dev

devlocal: web/dist/index.html ## on this machine, no docker: SQLite (.goap/goap.db) + IDE on http://localhost:8080
	GOAP_STORE=sqlite go run ./cmd/goap-dev

devlocal-reset: ## drop the local SQLite database (demo data and methodologies are seeded again)
	rm -rf .goap

# the IDE is rebuilt when its sources change (served by goap-dev)
web/dist/index.html: web/package.json web/index.html web/vite.config.ts $(shell find web/src -type f 2>/dev/null)
	cd web && npm install --no-audit --no-fund && npm run build

web:
	cd web && npm install && npm run dev

runner-image: ## image of the script sandboxes (goap-runner)
	docker build --build-arg SERVICE=goap-runner -t goap/runner:dev .

# Engine socket for the docker-proxy service (docker / podman / Docker Desktop)
export GOAP_DOCKER_SOCK ?= $(shell ./deploy/compose/docker-sock.sh)

up: runner-image
	$(COMPOSE) up --build -d

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f engine graph registry modelgw gateway
