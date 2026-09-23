# ADR 0010 — Local mode without containers (SQLite)

**Status**: accepted · **Date**: 2026-09

## Context
The full stack (compose: PostgreSQL, NATS, Vault, services, observability) is heavy for working on
a methodology or on the IDE. `goap-dev` (all-in-one in memory) loses everything on stop: graph,
edited methodologies, policies, processes waiting on a human action.

## Decision
- `goap-dev` selects its storage via `GOAP_STORE`: `memory` (default) or `sqlite`, a **single file**
  (`GOAP_SQLITE_PATH`, default `.goap/goap.db`) shared by the graph, the registry, IAM, and the engine.
- **`modernc.org/sqlite`** driver (pure Go, no cgo): nothing to install besides Go (and Node for the IDE).
- Each component has its own SQLite migrations (`migrations_sqlite/`), tracked per component in
  `schema_migrations`; same storage interfaces as PostgreSQL (`graph.Repo`, `registrysvc.Store`,
  Casbin adapter, `engine.Store`) and the same tests (the graph, registry, and IAM run on memory,
  SQLite, and PostgreSQL).
- The graph keeps the normalized model (versions, links, baselines, branches); methodologies and
  processes are JSON documents (queryable with SQLite's JSON functions).
- A single write connection (SQLite has only one writer), WAL, foreign keys enabled.
- On startup, processes left `running` are marked `failed` (no work-queue to resume);
  `waiting` / `clarifying` processes resume normally.
- `goap-dev` serves the compiled IDE (`GOAP_WEB_DIR`, default `web/dist`) with an SPA fallback; `make devlocal`
  rebuilds the IDE if its sources changed, then launches everything on http://localhost:8080.
- Scripts run in-process (`GOAP_SANDBOX=inproc`) or as a subprocess
  (`GOAP_SANDBOX=process`), without docker.

## Consequences
- SQLite remains a **development** mode: no clustering, a single process; production stays
  on PostgreSQL (one database per service).
- Any schema change must be made in both dialects (`migrations/` and `migrations_sqlite/`).
