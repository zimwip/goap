# ADR 0006 — Methodologies stored in the database, YAML for import/export

**Status**: accepted · **Date**: 2026-09

## Context
Methodologies must be editable from the frontend and administrable in the database. Storing the
YAML source made editing fragile (free text) and SQL administration impossible.

## Decision
- **Normalized relational** storage in the `registry` schema: a `methodology` header
  (name, version, status, author, dates) and one table per section (node types, link types,
  conditions, actions, goals) ordered by `position`. Variable-structure fields (pre/effects,
  `expects`, `params`) are `jsonb`.
- Lifecycle: `draft` → `published` (immutable, validated) → `archived`. The engine only executes the latest
  published version; published versions are cached by name@version in the engine.
- Detailed validation (`methodology.Validate`): a list of anomalies with field path, used by
  the editor; an invalid draft can be saved, not published.
- YAML kept as an exchange format: `ImportMethodology` / `ExportMethodology`, and import of
  `methodologies/*.yaml` files at startup (missing versions only).
- Writes subject to ABAC (resource `methodology`, actions `write`, `publish`, `delete`).

## Consequences
- The frontend edits a structured model (forms) rather than text.
- Modifying a published methodology requires a new version: processes in progress remain
  consistent with the version that started them (to be explicitly pinned in the process at milestone M1).
