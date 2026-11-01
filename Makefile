SERVICES := graph registry guardian engine modelgw preferences conversations credentials indexer events jobs gateway mcp connector-localfs connector-workspace connector-claude-coding-agent importer-git goap-local goap-runner goap-eval goap-bench
COMPOSE  := docker compose -f deploy/compose/docker-compose.yml
# the modules of the go.work (ADR 0125): `./...` at the root matches none, every Go command names them
MODULES  := contracts lib graph engine mcp gateway platform
PKGS     := $(foreach m,$(MODULES),./$(m)/...)
export PATH := $(PATH):$(shell go env GOPATH)/bin

.PHONY: perf perf-images perf-up perf-run perf-dump perf-capture perf-analysis perf-analysis-down perf-down perf-reset help all build test test-wasm tidy web-test web-e2e web-e2e-browser web-e2e-run test-pg lint eval generate cypher tools up urls down reset logs dev dev-backend dev-web dev-reset local local-build local-test local-bench local-reset web runner-image images claude-agent-runner

.DEFAULT_GOAL := help

help: ## list the targets and what they do (the default target)
	@echo "Usage: make <target>"
	@echo
	@grep -hE '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | sed -E 's/^([a-zA-Z0-9_-]+):.*## (.*)/\1\t\2/' | awk -F'\t' '{ printf "  %-20s %s\n", $$1, $$2 }'

all: generate build test ## generate, build, test

tools: ## install code generators
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest

generate: ## regenerate connect-rpc code and the web's TypeScript (web/src/gen) from proto/, and the catalogue of operations (contracts/operations)
	@test -x web/node_modules/.bin/protoc-gen-es || (cd web && npm ci --ignore-scripts)
	buf lint
	buf generate
	go generate ./contracts/operations/

cypher: ## regenerate the Cypher parser of lib/cypher from its grammar (ANTLR, needs Java; ADR 0160)
	cd lib && go generate ./cypher/

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

# the end-to-end tests of the IDE (Playwright, web/e2e): the browser they drive, then the tests (E2E_ARGS: npx playwright
# test's, e.g. E2E_ARGS="e2e/change.e2e.ts --headed"); the report in .goap/e2e/report
E2E_ARGS ?=
web-e2e-browser:
	cd web && npm install --no-audit --no-fund && npx playwright install chromium

web-e2e: local-build web-e2e-browser ## no docker: the platform on fresh data with the IDE and the demo data, then the Playwright tests of web/e2e in a browser, then stop
	$(LOCAL_BIN)/goap-local e2e $(LOCAL_ARGS) -- $(E2E_ARGS)

web-e2e-run: web-e2e-browser ## the Playwright tests of web/e2e against an IDE already running (make dev; GOAP_E2E_URL, default http://localhost:5173; GOAP_E2E_SUBJECT / GOAP_E2E_PASSWORD an administrator of it)
	cd web && npx playwright test $(E2E_ARGS)

PGTEST_IMAGE ?= pgvector/pgvector:pg17
PGTEST_PORT  ?= 55432

test-pg: ## every test, the PostgreSQL ones included (GOAP_TEST_PG_DSN, else a throw-away pgvector container on PGTEST_PORT, 55432)
	@if [ -n "$$GOAP_TEST_PG_DSN" ]; then go test -count=1 $(PKGS); else \
	  c=$$(docker run -d --rm -e POSTGRES_USER=goap -e POSTGRES_PASSWORD=goap -e POSTGRES_DB=goap -p $(PGTEST_PORT):5432 $(PGTEST_IMAGE)) || exit 1; \
	  trap "docker stop $$c >/dev/null" EXIT; \
	  until [ "$$(docker logs $$c 2>&1 | grep -c 'ready to accept connections')" -ge 2 ]; do sleep 1; done; \
	  GOAP_TEST_PG_DSN="postgres://goap:goap@localhost:$(PGTEST_PORT)/goap?sslmode=disable" go test -count=1 $(PKGS); fi

# offline evaluation of LLM work (ADR 0099) against a running gateway: GOAP_TOKEN, GOAP_EVAL_URL, EVAL (cases)
EVAL ?= evals
eval: ## offline evaluation cases of evals/ (or EVAL) against a running gateway, as GOAP_TOKEN
	go run ./platform/cmd/goap-eval $(EVAL)

lint: ## go vet + gofmt + tidy go.mod files + dsl.md sync check
	go vet $(PKGS)
	gofmt -l $(MODULES) | (! grep .)
	@for m in $(MODULES); do (cd $$m && go mod tidy -diff >/dev/null) || { echo "$$m/go.mod is not tidy: make tidy"; exit 1; }; done
	@cmp -s docs/dsl.md web/src/lib/help/dsl.md || (echo 'web/src/lib/help/dsl.md is out of sync with docs/dsl.md' && exit 1)

dev: local-build ## on this machine, no docker: the local stack (make local) with the IDE, rebuilt and restarted on a change of the sources: http://localhost:5173; Ctrl-C stops every process
	$(LOCAL_BIN)/goap-local -watch -web $(LOCAL_ARGS)

dev-backend: local-build ## the local stack without the IDE, rebuilt and restarted on a change of the sources: http://localhost:8080
	$(LOCAL_BIN)/goap-local -watch $(LOCAL_ARGS)

dev-web: ## IDE only, hot-reloaded by vite, proxied to the gateway: http://localhost:5173
	cd web && npm install --no-audit --no-fund && npm run dev

dev-reset: ## drop the data of the local stack (.goap/local; demo data and methodologies are seeded again)
	rm -rf .goap/local

# the platform as the compose stack runs it, one process per binary, without docker (goap-local): an embedded NATS, the
# graph and every service on SQLite files of .goap/local, the binaries in .goap/bin
LOCAL_BIN      := .goap/bin
LOCAL_SERVICES := domains graph registry guardian engine modelgw mcp indexer preferences credentials events conversations jobs gateway connector-localfs importer-git goap-bench goap-local
LOCAL_ARGS     ?=

local-build: ## the binaries of the local stack in .goap/bin
	@mkdir -p $(LOCAL_BIN)
	cd platform && go build -o ../$(LOCAL_BIN)/ $(addprefix ./cmd/,$(LOCAL_SERVICES))

local: local-build ## no docker: the platform one process per binary (embedded NATS, SQLite in .goap/local, LOCAL_ARGS: goap-local -h): gateway http://localhost:8080, then make dev-web
	$(LOCAL_BIN)/goap-local $(LOCAL_ARGS)

local-test: local-build ## no docker: the platform on fresh data (.goap/local), then the end-to-end test of platform/e2e/stack through its gateway, then stop
	$(LOCAL_BIN)/goap-local test $(LOCAL_ARGS)

local-bench: local-build ## no docker: the graph alone on SQLite (GOAP_DB_DSN: on PostgreSQL), loaded by goap-bench (PERF_ARGS); reports in .goap/local/bench
	$(LOCAL_BIN)/goap-local bench $(LOCAL_ARGS) -- $(PERF_ARGS)

local-reset: ## drop the data of the local stack (.goap/local)
	rm -rf .goap/local

web: dev-web ## same as dev-web

runner-image: ## image of the script sandboxes (goap-runner)
	docker build --build-arg SERVICE=goap-runner -t goap/runner:dev .

images: runner-image ## the images of the compose stack: every service (goap/goap:dev)
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
# the CPU and memory of the containers: cadvisor (Docker), podman (the podman exporter: the docker command is podman's) or
# none; PERF_PODMAN_SOCK the socket of the podman service (DOCKER_HOST, else the rootless one, else the root one)
PERF_CONTAINERS ?= $(if $(shell docker --version 2>/dev/null | grep -i podman),podman,cadvisor)
PERF_PODMAN_SOCK ?= $(or $(patsubst unix://%,%,$(filter unix://%,$(DOCKER_HOST))),$(wildcard $(XDG_RUNTIME_DIR)/podman/podman.sock),/run/podman/podman.sock)
PERF_UP_ENV := GOAP_PODMAN_SOCK='$(PERF_PODMAN_SOCK)'

perf: perf-up perf-run ## performance test in docker: the graph service on PostgreSQL loaded by goap-bench (PERF_ARGS, goap-bench -h; PERF_AUTOSTOP), measured in Grafana http://localhost:3300
	@echo "Grafana: http://localhost:3300/d/goap-perf · Prometheus: http://localhost:9390 · web: http://localhost:5174 · reports: .goap/perf/*.csv"

perf-images: ## the image of the performance stack: the Go services (goap/goap:dev)
	docker build --target services -t goap/goap:dev .

perf-up: perf-images ## start the performance stack (builds its image): graph, PostgreSQL, collector, Prometheus, Grafana, gateway and web, cAdvisor or the podman exporter (PERF_CONTAINERS)
	$(PERF_UP_ENV) $(PERF) $(if $(PERF_CONTAINERS),--profile $(PERF_CONTAINERS)) up -d

perf-run: ## one more run of goap-bench against the performance stack (PERF_ARGS, e.g. PERF_ARGS="-versions 1000000 -workers 8"); it holds once done until interrupted, or for PERF_AUTOSTOP (e.g. 30m; 0: exits with the run)
	$(PERF) run --rm bench $(PERF_HOLD) $(PERF_ARGS)

perf-dump: ## text summary of the last run of the performance stack (its Prometheus), by volume written, with the date and the commit: printed and kept in .goap/perf/dumps (PERF_ARGS written in its header)
	go run ./platform/cmd/goap-perfdump -args "$(PERF_ARGS)"

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
	$(PERF_UP_ENV) $(PERF) --profile bench --profile cadvisor --profile podman down

perf-reset: ## stop the performance stack and drop its data (database, Prometheus)
	$(PERF_UP_ENV) $(PERF) --profile bench --profile cadvisor --profile podman down --volumes --remove-orphans

logs: ## follow the logs of the docker compose stack
	$(COMPOSE) logs -f engine graph registry modelgw gateway
