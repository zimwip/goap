# ADR 0021 — Model gateway configuration in the graph

**Status**: accepted, implemented · **Date**: 2026-09

## Context

What describes the enterprise is graph data, changed through changes (CLAUDE.md, rule 4). The model gateway kept its
providers, catalog (quotas, roles) and aliases in SQL tables, with API keys sealed by a key of its own.

## Decision

1. Providers, models and aliases are nodes of the `platform` domain: `LlmProvider` (`LLP:<name>`), `LlmModel`
   (`LLM:<provider>/<model>`), `LlmAlias` (`LLA:<alias>`). They reference each other by name, like an `AdapterDef` names
   its MCP and connector; a model of an unknown provider and an alias of an unknown model are reported and ignored.
2. `pkg/llmcfg` holds the node schema and a snapshot of the head of `main` (`pkg/graphsnap.Cache`, shared with `pkg/access`);
   the gateway rebuilds its router when the head moves (looked at once per second).
3. An API key is never in the graph: `apiKeyRef` (`env:<VAR>` or `<vault path>#<field>`, alternatives separated by `|`) is
   resolved by the gateway at load time. A provider that needs a key and has none is reported inactive.
4. The only state left in SQL is the token usage (`llm_usage`, keyed by the key of the model node, so it follows the node).
   Migration `0002_graph_native` drops the configuration tables and re-keys the usage.
5. The administration RPCs that wrote configuration are gone; the frontend writes the nodes through changes.
   The initial configuration (`GOAP_MODELS_CONFIG` or the default) seeds a graph with no provider once (`graphsvc.SeedModels`).

## Consequences

- Model configuration is versioned, reviewable and journaled. Editing needs the rights of a change on the graph, not only `admin`
  on `platform`, which still guards the reads of the administration.
- The gateway depends on the graph for its configuration and keeps serving the last snapshot when the graph is unreachable.
- Provider keys move to Vault or the environment; `GOAP_SECRET_KEY` and the AES box are removed.
