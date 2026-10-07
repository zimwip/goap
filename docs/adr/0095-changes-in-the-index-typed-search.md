# ADR 0095 — Changes in the index, typed search by proximity

**Status**: accepted, implemented (server, proto, TS client types) · **Date**: 2026-10 · Builds on ADR 0026 (node index),
0037 (personal changes), 0039 and 0054 (projects), 0079 (drafts and versions), 0091 (a change names its project).

## Context

Finding "a change like this one" (before opening a duplicate, to reuse a past decision) and finding "the nodes of this
type close to this idea" both need the index. The index knew only node versions, searched them with one fixed shape (a
text, a few facet filters) and authorized them by node type only. The platform must let a caller search **nodes by type
and by semantic proximity**, and **changes by proximity**, without leaking what the caller may not see.

## Decision

### Who, why, how, what

A read model on the *what* (nodes) and the *why* (changes): it changes nothing. It stays a read model fed by events; the
authorization uses the *who* (organisation, projects) through one seam; no concept gains a dependency (`pkg/index` still
imports neither `pkg/access` nor `pkg/authz`).

### A change is a document

- **Storage**: one table (`node_index`), one FTS index, one HNSW index, with a `kind` column (`node` | `change`) and the
  primary key `(kind, node_id, version)`; a change is version 0 of its id. A sibling table would have doubled the
  stores' code (upsert, filters, fusion, nearest scan, HNSW, hash-skip, reset) for no gain, and a mixed search
  ("everything about the password reset") would need a union. The columns the filters need are real columns (`project`,
  `owner`, `status`, `methodology`, `parent`, `personal_to`, `title`), not facets, so they are indexable and the same for
  both kinds; a document has the columns of the other kind empty. Edited in place in both dialects (greenfield); the
  table keeps its name.
- **Event**: `domain.ChangeDocEvent` on `goap.changeindex.<id>`, built by the graph (`Graph.changeDocs`) inside the
  transaction that wrote the header or an impact of the change, published after commit with the other index events. A
  log entry alone does not trigger it (the log is written all the time and is not in the document). The existing
  `change.updated` event on `goap.changed.<id>` was not extended: it carries the header only when the header was written,
  never the impacts the text needs, and the hub forwards it to every browser; adding impact keys there would bloat it.
  A purge (`DeleteChange`) publishes `deleted: true`. A move to another project (ADR 0091) writes the header, so the
  document follows. Cost: an impact write whose header is not in the transaction reads the change (`Tx.Change`, which
  also loads its items: the repository has no lighter read), only when an index observer is plugged.
- **Text** (embedded and searched): the change id, then title, intent, goal, methodology, namespace and the keys (with
  types) of the nodes it acts on, each field capped at 2000 runes, at most 200 impacts, the whole at 4000 runes
  (`index.ChangeText`). **Not** the items, decisions, review comments or drafts: they are large, volatile and often
  private. The hash of the text skips the embedding call when only the status, state or project moved.
- **Fields**: id, title, namespace, status, lifecycle state, project (key), holding unit (`ownerOrg`), parent, branch,
  `personalTo` (the subject of a personal unit, derived by the indexer service with `access.PersonalSubject`; the graph
  and `pkg/index` do not know personal units), creation time; `main` is always true (`main: true` includes changes,
  `false` excludes them).
- **Feed**: `indexersvc.Subjects` adds `goap.changeindex.>`; the in-process `Sink` of `goap-dev` accepts it;
  `Graph.Republish` publishes the document of every change after reading them, so `Reindex` rebuilds both kinds
  (`RepublishIndexResponse.versions` counts every document published).
- Nodes gain `project` and `owner` on `NodeEvent` / `Doc` (the **keys** of the project and unit, resolved from
  `Node.Project` / `Node.Owner`, which are node ids; an unresolvable one keeps the id and so stays closed to non-admins).

### Typed search

`Search` (greenfield change of `goap.index.v1`): `kinds` (default `node`), `types`, `namespaces`, `states`, `branches`,
`main`, `facet_filters`, `facets`, `projects` (+ `include_subprojects`), `owner_units`, `statuses`, `methodologies`,
`roots_only`, `mode`, `min_similarity`, `snippet`, `similar_to`, `limit` / `offset` (the `k` of a nearest-neighbour
search is `limit`). Filters are combined with AND and a field a kind does not have leaves no document of that kind
(`types` or `statuses` set with `kinds: [node, change]` keeps only what has them); `roots_only` keeps only changes with
no parent.

- **Projects**: exact keys; `include_subprojects` expands each key to itself and its descendants server side
  (`Snapshot.SubProjects`, from `project_part_of`; cheap: the snapshot is cached). The expansion is not an
  authorization: the result is filtered afterwards.
- **Modes**: `hybrid` (default) fuses the full-text and vector rankings and degrades to lexical when no embedding is
  available (`semantic: false` in the answer); `lexical` never embeds; `semantic` ranks by cosine only and **fails with
  FAILED_PRECONDITION** when the embedder is missing or fails, and with INVALID_ARGUMENT without a text. Unknown mode,
  kind or a `min_similarity` outside 0..1 are INVALID_ARGUMENT.
- **min_similarity**: per request, replaces the service floor (`DefaultMinSimilarity` 0.3, or `Searcher.MinSimilarity`)
  for the vector side, 0 included.
- **Hit**: `kind`, `deleted`, `project`, `owner_unit`, `score` (fused rank, or cosine in semantic / `similar_to`),
  `similarity` (cosine when the hit came from the vector side), `snippet` (the passage of the text with most query
  terms, the beginning when none, cut at 240 runes, ellipses included; no highlighting) and, for a change,
  `change {change_id, title, status, project_id, methodology, owner_org, parent_id}`.
- **similar_to** `{kind, id}`: the stored embedding of an existing document (a node: the head of main, else its latest
  version; a change), no query text, no embedding call; the document itself (every version of a node) is excluded and
  every filter and the floor hold. Unknown or invisible source: NOT_FOUND (the source goes through the authorization, so
  its existence is not revealed); a source with no embedding yet: FAILED_PRECONDITION.

### Authorization of the results

Post-filter, as before, but before anything is counted: totals, facet counts and snippets are computed on the authorized
set only (`indexersvc.readFilter`).

| Document | Visible when |
|---|---|
| node | `read` on its type in its namespace (ABAC), **and** access to the project it was created in (`Snapshot.MayAccessProject`: a platform role, an assignment on the project or above, or administrator); a node with no project is not project-checked |
| change held by a personal unit | the caller is its subject; administrators are **not** admitted |
| any other change | access to its project, the rule of the move and the assistant checks |
| caller without identity | everything: an internal service (the gateway always identifies its callers; the index port is not public) |

The caller-dependent logic is behind the existing `authz.Authorizer` plus a small `indexersvc.Access` interface
(`MayAccessProject`, `SubProjects`), implemented by `access.Directory` and set by `cmd/indexer` and `cmd/goap-dev`. A
service with no `Access` skips the project checks (tests only). Consequence to know: a user holding no assignment and no
platform role sees no node that has a project, which is the intent. The authorization is a post-filter on at most
`PoolSize` (500) candidates, so a caller who may see little of a large pool can get fewer hits than exist; pushing the
personal-unit and project conditions into the stores' queries is a later optimisation.

### Meta types are searchable

`domains/builtin/methodology.yaml` (1.8.0) declares `search` on `MethodologyVersion`, `Agent`, `Action`, `Condition`,
`Goal`, `Role`, `Activity` (so `Method`, `Process`, `Step`, `MethodStep` inherit name and description), `Method`
(`for`, `guidance`), `Process` (`examples`), `Step` / `MethodStep` (`instructions`, `guidance`), `ToolRequest`;
`organisation.yaml` (1.14.0) on `OrgUnit` and `ProjectUnit` (`name`, `description`). Users are not declared; `User`
extends `OrgUnit` and so inherits the two properties, which a user does not set. The prompts, scripts and code of
actions are not indexed. **Reindex rule**: the index follows the declarations when a node version is written; a change of
a `search` declaration does not touch the versions already written, so it needs a `Reindex` (admin) to be applied to
existing nodes.

## Not done

Tools for the assistant and `goap-change` (`search_changes`, `similar_changes`), the web UI (kind selector, similar
changes panel), methodology integration (a methodology asking for similar past changes), pushing the authorization
conditions into the queries, a `TestSchemasAligned` for `pkg/index` (`sqlschematest` does not understand pgvector or
FTS5; the two migrations are aligned by hand), highlighting.
