# ADR 0026 — Node index and search (RAG + full text)

**Status**: accepted, implemented · **Date**: 2026-09 · Extends ADR 0010 (local mode), ADR 0012 (node types),
ADR 0021 (model configuration in the graph).

## Context
Finding a node means listing and filtering client-side. We want semantic search (RAG over an embedding index) and
full-text search with facets per property, without coupling the graph to a search engine.

Question answered (CLAUDE.md, rule 8): **WHAT** is being changed — finding nodes of the domain. The index is a
derived read model of the graph; it holds no truth and can always be rebuilt.

## Decision

### 1. The NodeType says what is searchable
A node type declares its searchable properties, like its validators (ADR 0018):

```yaml
- name: Requirement
  properties: [title, description, priority, status]
  search:
    - {property: title, text: true, facet: true}
    - {property: description, text: true}
    - {property: priority, facet: true}
```

- `text`: the value goes into the indexed document (full text and embedding).
- `facet`: the value is filterable and counted (`GROUP BY`).
- Inherited through `extends` (subtype adds to, and may override by property name, its parent's).
- The graph resolves the spec from its type catalogue (the copy of the registry's model, ADR 0012 §2) so the indexer never reads node types: it knows
  the graph events and the model gateway only.
- Built-in facets, always present: `namespace`, `type`, `state`, `branch`, `main`.

### 2. The graph publishes what it writes
`Graph.Observe` wraps the repo in a decorator that observes each transaction: `PutNode` and `PutBaseline` are collected, and
**only after the commit** published. It sees every write path (change impacts, merges, flows, direct writes, seeds)
with no call-site change.

| Subject | Payload |
|---|---|
| `goap.node.<ns>.<type>.<id>.written` (`<type>`: the qualified type, `alm@Requirement`; `.`, `*`, `>` and spaces escaped) | `NodeEvent`: id, version, branch, namespace, key, type, state, deleted, `text` (resolved searchable text), `facets` (resolved facet values), `changeId`, time |
| `goap.baseline.<branch>.advanced` | `BaselineEvent`: id, branch, parent, and the **diff** with the parent baseline: `set: {nodeId: version}`, `removed: [nodeId]` |

### 3. Index everything, filter main by a facet
Every node **version on every branch** is indexed (key `(node_id, version)`). Whether a version is the head of `main` is
not a property of the version (a fast-forward merge keeps the version on its change branch), so the indexer maintains
a `main` flag from the `baseline.main.advanced` diffs: `set` marks `(id, version)` main and clears the previous
version of that node; `removed` clears it. Searching main only = filter `main = true`. The RPC searches every version unless `main` is set;
the IDE search defaults to the heads of main.

### 4. Embedding model = platform setup
- The embedding model is a catalog model like any other (`LlmModel`, quota and roles apply) that the alias `embed`
  points to, configured through changes like every model node (ADR 0021). Whether it can embed depends on its
  provider protocol. Without keys, the default configuration aliases `embed` to the fake provider (hashed words).
- `model.v1` `Embed(texts, alias) → vectors`. Protocol `openai` (`/embeddings`), `gemini` (`batchEmbedContents`),
  `fake` (deterministic hash vector, tests). `anthropic` has none.
- The dimension is the one the model answers; the index service creates the HNSW index for it on the first
  embedding. Changing the model means `Reindex` (a query with another dimension than the stored ones fails).
- No embedding model configured: the index is text-only; `Search` reports `semantic: false`.

### 5. The index service
`cmd/indexer` / `internal/indexersvc`, own database (schema per service), **durable JetStream consumer** on
`goap.node.>` and `goap.baseline.>` (at-least-once; upserts idempotent on `(node_id, version)`).

- Table `node_index`: identity, `namespace`, `type`, `key`, `state`, `branch`, `main`, `facets jsonb`, `doc text`,
  `tsv`, `embedding`.
- RPC `Search(text, namespaces, types, states, branches, main, facet filters, facets, limit, offset)` → hits + facet
  counts; `Reindex()` rebuilds from the graph
  (bootstrap, model change, lost events); `Status()`.
- **Hybrid** retrieval: vector top-k and full-text top-k merged by reciprocal rank fusion.
- **Authorization (ABAC)**: hits are post-filtered by `pkg/access` on the caller (the index stores no policy). The
  page is over-fetched so filtering keeps the page full; facet counts are computed on the authorized set.

### 6. Two stores behind one interface (`pkg/index`)
| | PostgreSQL | SQLite (devlocal) |
|---|---|---|
| Full text | `tsvector` generated column + GIN | FTS5 virtual table |
| Vector | `pgvector`, HNSW, cosine | BLOB column, exact cosine scan in Go |
| Facets | `jsonb` (GIN `jsonb_path_ops`), `GROUP BY` | `json_extract`, `GROUP BY` |

The exact scan is fine at development scale (tens of thousands of nodes). Schema changes go to both
(`migrations/`, `migrations_sqlite/`). Deployment images move to `pgvector/pgvector` (`CREATE EXTENSION vector` in
`deploy/postgres/init.sql`).

`goap-dev` runs the indexer in process, fed by an in-process sink (`indexersvc.NewSink`, through `Graph.Observe`),
and publishes the graph again at start (`Republish`), so no event is missed across restarts.

### 7. Limits
- Events are published after the commit, best effort: a crash between commit and publish loses them; `Reindex`
  repairs. (An outbox in the graph transaction would remove the gap.)
- A change of the `search` declaration of a type does not reindex the nodes already stored: run `Reindex`.
- Search returns at most `PoolSize` (500) candidates; totals and facets cover them (`truncated`).

## Consequences
- The graph stays free of search code; the index can lag (eventually consistent) and is rebuildable.
- The `main` flag depends on baseline diffs; `Reindex` recomputes it from the head of `main`.
- Embedding cost is per node version on every branch; batching and a text hash (skip the call when the document is
  unchanged) bound it.
- Coupling respects the design rules: the indexer knows graph events, the model gateway and the graph (to
  republish it and for access control), no methodology or connector.
