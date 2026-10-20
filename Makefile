SERVICES := graph registry engine modelgw preferences conversations credentials indexer events gateway mcp connector-localfs connector-workspace connector-claude-coding-agent goap-dev goap-runner goap-eval goap-bench
COMPOSE  := docker compose -f deploy/compose/docker-compose.yml
# the modules of the go.work (ADR 0125): `./...` at the root matches none, every Go command names them
MODULES  := contracts lib graph engine mcp gateway platform
PKGS     := $(foreach m,$(MODULES),./$(m)/...)
export PATH := $(PATH):$(shell go env GOPATH)/bin

.PHONY: perf perf-up perf-run perf-capture perf-analysis perf-analysis-down perf-down perf-reset help all build test test-wasm tidy web-test test-pg lint eval generate tools up urls down reset logs dev dev-backend dev-web dev-reset web runner-image images claude-agent-runner

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

test-wasm: ## every go test with the scripts run by the wasm JavaScript engine (GOAP_JS_ENGINE=wasm, ADR 0134)
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

images: runner-image ## the image of every Go service of the compose stack (goap/goap:dev), built once
	docker build --target services -t goap/goap:dev .

claude-agent-runner: ## install the Claude Agent SDK of the claude-coding-agent connector (GOAP_CLAUDE_AGENT_DIR, default .goap/claude-agent; needs npm)
	go run ./platform/cmd/connector-claude-coding-agent install

# Engine socket for the docker-proxy service (docker / podman / Docker Desktop)
export GOAP_DOCKER_SOCK ?= $(shell ./deploy/compose/docker-sock.sh)

up: images ## start the docker compose stack, then print its URLs and how to reach its database (DBeaver)
	$(COMPOSE) up -d
	@cat deploy/compose/urls.txt

urls: ## the URLs of the compose stack: IDE, gateway, each service, tools, database (DBeaver)
	@cat deploy/compose/urls.txt

down: ## stop the docker compose stack
	$(COMPOSE) down

reset: ## stop the docker compose stack and drop everything it keeps: containers, volumes (database, NATS, web modules), images built for it
	$(COMPOSE) down --volumes --remove-orphans
	-docker image rm goap/goap:dev goap/runner:dev

# Performance stack (deploy/compose/perf.yml): the graph service on PostgreSQL, goap-bench, Prometheus and Grafana
PERF      := docker compose -f deploy/compose/perf.yml
PERF_ARGS ?= -versions 100000
# goap-bench stays up once its run ends, so its series stay in Grafana, until interrupted; PERF_AUTOSTOP=30m stops it
# after that long, PERF_AUTOSTOP=0 with the run
PERF_AUTOSTOP ?=
PERF_HOLD := -hold=$(if $(PERF_AUTOSTOP),$(PERF_AUTOSTOP),-1s)
# captures of the performance stack's Prometheus (make perf-capture), replayed by make perf-analysis
PERF_CAPTURES := .goap/perf/captures
PERF_ANALYSIS := docker compose -f deploy/compose/perf-analysis.yml

perf: perf-up perf-run ## performance test in docker: the graph service on PostgreSQL loaded by goap-bench (PERF_ARGS, goap-bench -h; PERF_AUTOSTOP), measured in Grafana http://localhost:3300
	@echo "Grafana: http://localhost:3300/d/goap-perf · Prometheus: http://localhost:9390 · web: http://localhost:5174 · reports: .goap/perf/*.csv"

perf-up: ## start the performance stack (builds goap/goap:dev): graph, PostgreSQL, collector, Prometheus, Grafana, gateway and web
	docker build --target services -t goap/goap:dev .
	$(PERF) up -d

perf-run: ## one more run of goap-bench against the performance stack (PERF_ARGS, e.g. PERF_ARGS="-versions 1000000 -workers 8"); it holds once done until interrupted, or for PERF_AUTOSTOP (e.g. 30m; 0: exits with the run)
	$(PERF) run --rm bench $(PERF_HOLD) $(PERF_ARGS)

perf-capture: ## save what the performance stack's Prometheus holds in .goap/perf/captures/<PERF_CAPTURE, default the time>, for make perf-analysis
	@set -e; name="$${PERF_CAPTURE:-$$(date +%Y%m%d-%H%M%S)}"; dir="$(PERF_CAPTURES)/$$name"; \
	test ! -e "$$dir" || { echo "perf-capture: $$dir exists"; exit 1; }; \
	from=$$($(PERF) exec -T prometheus wget -qO- http://localhost:9090/metrics | awk '$$1 == "prometheus_tsdb_lowest_timestamp_seconds" {printf "%d000", $$2}'); \
	snap=$$($(PERF) exec -T prometheus wget -qO- --post-data= http://localhost:9090/api/v1/admin/tsdb/snapshot | sed -n 's/.*"name":"\([^"]*\)".*/\1/p'); \
	test -n "$$snap" || { echo "perf-capture: Prometheus took no snapshot (make perf-up first)"; exit 1; }; \
	mkdir -p "$(PERF_CAPTURES)"; $(PERF) cp "prometheus:/prometheus/snapshots/$$snap" "$$dir"; \
	$(PERF) exec -T prometheus rm -rf "/prometheus/snapshots/$$snap"; \
	echo "$$from $$(date +%s)000" > "$$dir/range"; \
	echo "captured $$dir: make perf-analysis PERF_CAPTURE=$$name"

perf-analysis: ## replay a capture (PERF_CAPTURE, default the latest) in a Grafana of its own, http://localhost:3301, with no graph nor bench
	@set -e; name="$${PERF_CAPTURE:-$$(ls -1 $(PERF_CAPTURES) 2>/dev/null | tail -n 1)}"; \
	test -n "$$name" -a -d "$(PERF_CAPTURES)/$$name" || { echo "perf-analysis: no capture $$name in $(PERF_CAPTURES) (make perf-capture)"; exit 1; }; \
	GOAP_PERF_CAPTURE="$$(cd "$(PERF_CAPTURES)/$$name" && pwd)" $(PERF_ANALYSIS) up -d --force-recreate; \
	read from to < "$(PERF_CAPTURES)/$$name/range"; \
	echo "Grafana: http://localhost:3301/d/goap-perf?from=$$from&to=$$to · Prometheus: http://localhost:9391 · make perf-analysis-down to stop"

perf-analysis-down: ## stop the replay of a capture (the capture stays)
	GOAP_PERF_CAPTURE=. $(PERF_ANALYSIS) down

perf-down: ## stop the performance stack, keeping its data (the graph grows run after run)
	$(PERF) --profile bench down

perf-reset: ## stop the performance stack and drop its data (database, Prometheus)
	$(PERF) --profile bench down --volumes --remove-orphans

logs: ## follow the logs of the docker compose stack
	$(COMPOSE) logs -f engine graph registry modelgw gateway
