SERVICES := graph registry engine modelgw preferences conversations credentials indexer events jobs gateway mcp connector-localfs connector-workspace connector-claude-coding-agent importer-git goap-local goap-runner goap-eval goap-bench
COMPOSE  := docker compose -f deploy/compose/docker-compose.yml
# the modules of the go.work (ADR 0125): `./...` at the root matches none, every Go command names them
MODULES  := contracts lib graph engine mcp gateway platform
PKGS     := $(foreach m,$(MODULES),./$(m)/...)
export PATH := $(PATH):$(shell go env GOPATH)/bin

.PHONY: perf perf-images perf-up perf-run perf-rust perf-rust-up perf-rust-run perf-diff rust-test rust-lint perf-capture perf-analysis perf-analysis-down perf-down perf-reset help all build test test-wasm tidy web-test test-pg lint eval generate cypher tools up urls down reset logs dev dev-backend dev-web dev-reset local local-build local-test local-bench local-reset web runner-image images claude-agent-runner

.DEFAULT_GOAL := help

help: ## list the targets and what they do (the default target)
	@echo "Usage: make <target>"
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | sed -E 's/^([a-zA-Z_-]+):.*## (.*)/\1\t\2/' | awk -F'\t' '{ printf "  %-20s %s\n", $$1, $$2 }'

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

# make dev builds the Rust binaries with the cargo profile dev-local, rebuilt in a fraction of release's time
dev dev-backend: LOCAL_CARGO_PROFILE = dev-local

dev: local-build ## on this machine, no docker: the local stack (make local) with the IDE, rebuilt and restarted on a change of the sources (Go and Rust): http://localhost:5173; Ctrl-C stops every process
	$(LOCAL_BIN)/goap-local -watch -web -graph $(LOCAL_GRAPH) -cargo-profile $(LOCAL_CARGO_PROFILE) $(LOCAL_ARGS)

dev-backend: local-build ## the local stack without the IDE, rebuilt and restarted on a change of the sources: http://localhost:8080
	$(LOCAL_BIN)/goap-local -watch -graph $(LOCAL_GRAPH) -cargo-profile $(LOCAL_CARGO_PROFILE) $(LOCAL_ARGS)

dev-web: ## IDE only, hot-reloaded by vite, proxied to the gateway: http://localhost:5173
	cd web && npm install --no-audit --no-fund && npm run dev

dev-reset: ## drop the data of the local stack (.goap/local; demo data and methodologies are seeded again)
	rm -rf .goap/local

# the platform as the compose stack runs it, one process per binary, without docker (goap-local): an embedded NATS, the
# graph (LOCAL_GRAPH: rust, or go) and every service on SQLite files of .goap/local, the binaries in .goap/bin
LOCAL_BIN      := .goap/bin
LOCAL_SERVICES := domains graph registry engine modelgw mcp indexer preferences credentials events conversations jobs gateway connector-localfs importer-git goap-bench goap-local
LOCAL_GRAPH    ?= rust
LOCAL_ARGS     ?=
# the cargo profile of goap-graph-rs and goap-proxy (make dev: dev-local), and the directory of target/ it builds in
LOCAL_CARGO_PROFILE ?= release
LOCAL_CARGO_DIR = $(if $(filter dev,$(LOCAL_CARGO_PROFILE)),debug,$(LOCAL_CARGO_PROFILE))

local-build: ## the binaries of the local stack in .goap/bin: the Go services, goap-graph-rs, goap-domains-rs and goap-proxy (cargo, LOCAL_CARGO_PROFILE: release, dev-local for make dev)
	@mkdir -p $(LOCAL_BIN)
	cd platform && go build -o ../$(LOCAL_BIN)/ $(addprefix ./cmd/,$(LOCAL_SERVICES))
	cd rust && cargo build --profile $(LOCAL_CARGO_PROFILE) --locked -p graph-service -p goap-proxy
	cp rust/target/$(LOCAL_CARGO_DIR)/goap-graph-rs rust/target/$(LOCAL_CARGO_DIR)/goap-domains-rs rust/target/$(LOCAL_CARGO_DIR)/goap-proxy $(LOCAL_BIN)/

local: local-build ## no docker: the platform one process per binary (embedded NATS, SQLite in .goap/local, LOCAL_GRAPH rust|go, LOCAL_ARGS: goap-local -h): gateway http://localhost:8080, then make dev-web
	$(LOCAL_BIN)/goap-local -graph $(LOCAL_GRAPH) $(LOCAL_ARGS)

local-test: local-build ## no docker: the platform on fresh data (.goap/local), then the end-to-end test of platform/e2e/stack through its gateway, then stop
	$(LOCAL_BIN)/goap-local test -graph $(LOCAL_GRAPH) $(LOCAL_ARGS)

local-bench: local-build ## no docker: the graph alone on SQLite behind goap-proxy (LOCAL_PROXY: rust, go, compare, shadow), loaded by goap-bench (PERF_ARGS); reports in .goap/local/bench and .goap/local/diff
	$(LOCAL_BIN)/goap-local bench -graph $(LOCAL_GRAPH) -proxy $(or $(LOCAL_PROXY),rust) $(LOCAL_ARGS) -- $(PERF_ARGS)

local-reset: ## drop the data of the local stack (.goap/local)
	rm -rf .goap/local

web: dev-web ## same as dev-web

runner-image: ## image of the script sandboxes (goap-runner)
	docker build --build-arg SERVICE=goap-runner -t goap/runner:dev .

images: runner-image ## the images of the compose stack: every Go service (goap/goap:dev), the graph services in Rust (goap/rust:dev)
	docker build --target services -t goap/goap:dev .
	docker build --target rust -t goap/rust:dev .

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
	-docker image rm goap/goap:dev goap/rust:dev goap/proxy:dev goap/runner:dev

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
# goap-proxy in front of the graph service (ADR 0163): every call of the bench and the gateway goes through it;
# PERF_PROXY=0 sends them to the graph service itself
PERF_PROXY ?= 1
# the Rust graph service (deploy/compose/perf.rust.yml): make perf-rust runs it behind the proxy in mode
# PERF_RUST_MODE (rust; go sends the calls to the Go service of the same stack); make perf-diff compares it with the Go one call by call (PERF_DIFF_ARGS, serial, seeded;
# PERF_DIFF_RUST=go compares Go with Go), in mode PERF_DIFF_MODE: shadow (the reads sent to both on the Go service's
# schema: what Rust serves before it writes) or compare (every call, each on its schema, then their tables)
PERF_RUST := docker compose -f deploy/compose/perf.yml -f deploy/compose/perf.rust.yml
PERF_RUST_MODE ?= rust
PERF_DIFF_ARGS ?= -versions 5000
PERF_DIFF_RUST ?= rust
PERF_DIFF_MODE ?= shadow
PERF_DIFF_DSN_GO := postgres://graph:graph@postgres:5432/goap?sslmode=disable
PERF_DIFF_DSN_RS := postgres://graph_rs:graph_rs@postgres:5432/goap?sslmode=disable
PERF_GO_ENV := $(if $(filter 0,$(PERF_PROXY)),GOAP_PERF_GRAPH_URL=http://graph:8080)
PERF_RUST_ENV := GOAP_PERF_IMPL=$(PERF_RUST_MODE) $(if $(filter 0,$(PERF_PROXY)),GOAP_PERF_GRAPH_URL=http://graph-rs:8080)
PERF_DIFF_ENV := GOAP_PERF_IMPL=$(PERF_DIFF_MODE) GOAP_PERF_RS_DSN='$(if $(filter shadow,$(PERF_DIFF_MODE)),$(PERF_DIFF_DSN_GO),$(PERF_DIFF_DSN_RS))' $(if $(filter go,$(PERF_DIFF_RUST)),GOAP_PERF_RS_IMAGE=goap/goap:dev GOAP_PERF_RS_BIN=/goap/graph)

perf: perf-up perf-run ## performance test in docker: the graph service on PostgreSQL loaded by goap-bench through goap-proxy (PERF_ARGS, goap-bench -h; PERF_AUTOSTOP; PERF_PROXY=0 without the proxy), measured in Grafana http://localhost:3300
	@echo "Grafana: http://localhost:3300/d/goap-perf · Prometheus: http://localhost:9390 · web: http://localhost:5174 · reports: .goap/perf/*.csv"

perf-images: ## the images of the performance stack: the Go services (goap/goap:dev), the Rust graph service (goap/rust:dev), goap-proxy (goap/proxy:dev)
	docker build --target services -t goap/goap:dev .
	docker build --target rust -t goap/rust:dev .
	docker build --target proxy -t goap/proxy:dev .

perf-up: perf-images ## start the performance stack (builds its images): graph, goap-proxy, PostgreSQL, collector, Prometheus, Grafana, gateway and web
	$(PERF_GO_ENV) $(PERF) up -d

perf-run: ## one more run of goap-bench against the performance stack (PERF_ARGS, e.g. PERF_ARGS="-versions 1000000 -workers 8"); it holds once done until interrupted, or for PERF_AUTOSTOP (e.g. 30m; 0: exits with the run)
	$(PERF_GO_ENV) $(PERF) run --rm bench $(PERF_HOLD) $(PERF_ARGS)

perf-rust: perf-rust-up perf-rust-run ## the performance test of the Rust graph service: make perf with goap-proxy in mode PERF_RUST_MODE (rust)
	@echo "Grafana: http://localhost:3300/d/goap-perf · reports: .goap/perf/*.csv"

perf-rust-up: perf-images ## start the performance stack with the Rust graph service behind goap-proxy (PERF_RUST_MODE)
	$(PERF_RUST_ENV) $(PERF_RUST) up -d

perf-rust-run: ## one more run of goap-bench against the stack of make perf-rust-up (PERF_ARGS, PERF_AUTOSTOP)
	$(PERF_RUST_ENV) $(PERF_RUST) run --rm bench $(PERF_HOLD) $(PERF_ARGS)

perf-diff: perf-images ## the Go and the Rust graph services compared call by call (goap-proxy mode PERF_DIFF_MODE: shadow, the reads on one schema; compare, every call each on its schema, then the databases), goap-bench serial and seeded (PERF_DIFF_ARGS); report in .goap/perf/diff
	$(PERF_RUST) up -d --wait postgres
	$(PERF_RUST) run --rm --no-deps --entrypoint psql postgres-init -v ON_ERROR_STOP=1 -h postgres -U goap -d goap \
		-c 'DROP SCHEMA IF EXISTS graph CASCADE' -c 'DROP SCHEMA IF EXISTS graph_rs CASCADE'
	$(PERF_DIFF_ENV) $(PERF_RUST) up -d --force-recreate postgres-init graph graph-rs proxy otel-collector
	$(PERF_DIFF_ENV) $(PERF_RUST) run --rm bench -hold=0 -workers 1 -seed 1 $(PERF_DIFF_ARGS)
	@curl -fsS http://localhost:8383/proxy/report; echo
	$(if $(filter shadow,$(PERF_DIFF_MODE)),@true,$(PERF_DIFF_ENV) $(PERF_RUST) run --rm --no-deps --entrypoint /goap/goap-proxy proxy diff-db \
		--go '$(PERF_DIFF_DSN_GO)' --rust '$(PERF_DIFF_DSN_RS)' --ids /results/diff/ids.json)

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
	$(PERF_RUST) --profile bench down

perf-reset: ## stop the performance stack and drop its data (database, Prometheus)
	$(PERF_RUST) --profile bench down --volumes --remove-orphans

rust-test: ## cargo test of the Rust workspace (rust/: the Rust graph service and goap-proxy, ADR 0163; GOAP_TEST_PG_DSN for its PostgreSQL tests)
	cd rust && cargo test --locked

rust-lint: ## cargo fmt and clippy of the Rust workspace, warnings refused
	cd rust && cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings

logs: ## follow the logs of the docker compose stack
	$(COMPOSE) logs -f engine graph registry modelgw gateway
