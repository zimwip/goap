SERVICES := graph registry engine modelgw preferences conversations credentials indexer events gateway mcp connector-localfs connector-workspace connector-claude-coding-agent goap-dev goap-runner goap-eval
COMPOSE  := docker compose -f deploy/compose/docker-compose.yml
# one Cargo workspace at the root, a crate per package (CLAUDE.md, "Rust conventions"); the toolchain is pinned by
# rust-toolchain.toml and installed by `make toolchain`
export PATH := $(HOME)/.cargo/bin:$(PATH)
CARGO    ?= cargo
# extra arguments of the workspace commands, e.g. CARGO_EXCLUDE="--exclude goap-e2e-engine"
CARGO_EXCLUDE ?=
# buf lints the protos and writes the web's TypeScript (web/src/gen); the one on the PATH, else the npm package
BUF_VERSION ?= 1.50.0
BUF      ?= $(shell command -v buf 2>/dev/null || echo npx --yes @bufbuild/buf@$(BUF_VERSION))

.PHONY: help toolchain watch-tool rs-build rs-test rs-lint all build test tidy web-test test-pg lint eval generate tools up down logs dev devlocal devlocal-backend devlocal-web devlocal-reset web runner-image claude-agent-runner

.DEFAULT_GOAL := help

help: ## list the targets and what they do (the default target)
	@echo "Usage: make <target>"
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | sed -E 's/^([a-zA-Z_-]+):.*## (.*)/\1\t\2/' | awk -F'\t' '{ printf "  %-20s %s\n", $$1, $$2 }'

all: generate build test ## generate, build, test

toolchain: ## install what building needs when it is missing: rustup + the pinned Rust toolchain (rust-toolchain.toml), checks node / npm for the web
	@if ! command -v rustup >/dev/null 2>&1; then \
	  echo "rustup not found: installing it (https://rustup.rs)"; \
	  command -v curl >/dev/null 2>&1 || { echo "curl is needed to install rustup"; exit 1; }; \
	  curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain none || exit 1; \
	fi
	@rustup show active-toolchain >/dev/null 2>&1 || rustup toolchain install
	@rustup show active-toolchain
	@command -v node >/dev/null 2>&1 || echo "warning: node / npm not found, needed for the web (web/) and make generate: https://nodejs.org"

watch-tool: toolchain ## install watchexec (devlocal's rebuild on change) when it is missing
	@command -v watchexec >/dev/null 2>&1 || $(CARGO) install --locked watchexec-cli

# the former names of the Rust targets, kept as aliases
rs-build: build ## former name of build
rs-test: test ## former name of test
rs-lint: lint ## former name of lint

tools: watch-tool ## install the development tools: watchexec, and buf when it is not on the PATH (npm)
	@command -v buf >/dev/null 2>&1 || npm install -g @bufbuild/buf@$(BUF_VERSION)

generate: toolchain ## regenerate the web's TypeScript (web/src/gen) from proto/, the catalogue of operations (contracts/operations) and the form widget catalogue of the web
	@test -x web/node_modules/.bin/protoc-gen-es || (cd web && npm ci --ignore-scripts)
	$(BUF) lint
	$(BUF) generate
	GOAP_UPDATE_OPERATIONS=1 $(CARGO) test -p goap-opgen --lib catalogue_is_current
	GOAP_UPDATE_WEB=1 $(CARGO) test -p goap-form --test catalog

build: toolchain ## build the workspace, and the services into bin/
	$(CARGO) build --workspace $(CARGO_EXCLUDE)
	@mkdir -p bin
	@for s in $(SERVICES); do cp target/debug/$$s bin/$$s || exit 1; done

test: toolchain ## cargo test of the workspace (CARGO_EXCLUDE: e.g. --exclude <crate>)
	$(CARGO) test --workspace $(CARGO_EXCLUDE)

tidy: toolchain ## bring Cargo.lock in line with the manifests of the workspace
	$(CARGO) update --workspace

web-test: ## unit tests of the web (needs npm install in web/)
	cd web && npm test

PGTEST_IMAGE ?= pgvector/pgvector:pg17
PGTEST_PORT  ?= 55432

test-pg: toolchain ## every test, the PostgreSQL ones included (GOAP_TEST_PG_DSN, else a throw-away pgvector container on $(PGTEST_PORT))
	@if [ -n "$$GOAP_TEST_PG_DSN" ]; then $(CARGO) test --workspace $(CARGO_EXCLUDE); else \
	  c=$$(docker run -d --rm -e POSTGRES_USER=goap -e POSTGRES_PASSWORD=goap -e POSTGRES_DB=goap -p $(PGTEST_PORT):5432 $(PGTEST_IMAGE)) || exit 1; \
	  trap "docker stop $$c >/dev/null" EXIT; \
	  until [ "$$(docker logs $$c 2>&1 | grep -c 'ready to accept connections')" -ge 2 ]; do sleep 1; done; \
	  GOAP_TEST_PG_DSN="postgres://goap:goap@localhost:$(PGTEST_PORT)/goap?sslmode=disable" $(CARGO) test --workspace $(CARGO_EXCLUDE); fi

# offline evaluation of LLM work (ADR 0099) against a running goap-dev or gateway: GOAP_TOKEN, GOAP_EVAL_URL, EVAL (cases)
EVAL ?= evals
eval: toolchain ## offline evaluation cases of evals/ (or EVAL) against a running gateway, as GOAP_TOKEN
	$(CARGO) run -q -p goap-cmd-goap-eval -- $(EVAL)

lint: toolchain ## rustfmt + clippy (warnings refused) + Cargo.lock in line with the manifests + dsl.md sync check
	$(CARGO) fmt --all --check
	$(CARGO) clippy --workspace $(CARGO_EXCLUDE) --all-targets -- -D warnings
	@$(CARGO) metadata --locked --format-version 1 >/dev/null || { echo "Cargo.lock is not in line with the manifests: make tidy"; exit 1; }
	@cmp -s docs/dsl.md web/src/lib/help/dsl.md || (echo 'web/src/lib/help/dsl.md is out of sync with docs/dsl.md' && exit 1)

dev: toolchain ## single process, in-memory, demo data: http://localhost:8080
	$(CARGO) run -p goap-cmd-goap-dev

devlocal: watch-tool ## on this machine, no docker: SQLite (.goap/goap.db), backend + IDE both reload on code change: http://localhost:5173
	@trap 'kill 0' EXIT INT TERM; \
	$(MAKE) devlocal-backend & \
	$(MAKE) devlocal-web & \
	wait

# what a change of rebuilds the backend: the crates, the protos, the manifests (not web/, .goap/, target/, deploy/)
WATCH := -w contracts -w lib -w graph -w engine -w mcp -w gateway -w platform -w proto -w Cargo.toml -w Cargo.lock -e rs,toml,lock,sql,proto,yaml,mjs,json

devlocal-backend: watch-tool ## SQLite backend only, rebuilt and restarted by watchexec on source changes: http://localhost:8080
	GOAP_STORE=sqlite watchexec -r --stop-signal SIGINT $(WATCH) -- $(CARGO) run -p goap-cmd-goap-dev

devlocal-web: ## IDE only, hot-reloaded by vite, proxied to the backend: http://localhost:5173
	cd web && npm install --no-audit --no-fund && npm run dev

devlocal-reset: ## drop the local SQLite database (demo data and methodologies are seeded again)
	rm -rf .goap

# static build of the IDE, served by goap-dev itself (used by `make dev`, not devlocal)
web/dist/index.html: web/package.json web/index.html web/vite.config.ts $(shell find web/src -type f 2>/dev/null)
	cd web && npm install --no-audit --no-fund && npm run build

web: devlocal-web ## same as devlocal-web

runner-image: ## image of the script sandboxes (goap-runner)
	docker build --build-arg SERVICE=goap-runner -t goap/runner:dev .

claude-agent-runner: toolchain ## install the Claude Agent SDK of the claude-coding-agent connector (GOAP_CLAUDE_AGENT_DIR, default .goap/claude-agent; needs npm)
	$(CARGO) run -q -p goap-cmd-connector-claude-coding-agent -- install

# Engine socket for the docker-proxy service (docker / podman / Docker Desktop)
export GOAP_DOCKER_SOCK ?= $(shell ./deploy/compose/docker-sock.sh)

up: runner-image ## start the docker compose stack
	$(COMPOSE) up --build -d

down: ## stop the docker compose stack
	$(COMPOSE) down

logs: ## follow the logs of the docker compose stack
	$(COMPOSE) logs -f engine graph registry modelgw gateway
