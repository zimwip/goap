# ADR 0089 — LLM calls: one ledger at the gateway

**Status**: accepted, implemented (server and web) · **Date**: 2026-10 · Builds on
ADR 0011 (journal), 0021 (model configuration), 0059 (prompts in the change log), 0084-0087 (assistant, helper).

## Context

Every LLM call must be tracked per call. Only the engine did it, in its own process records and journal (`LLMCall`,
`journal.ModelCall`); the Tokens console and the Token usage pane of the web are built from them. The gateway kept an
aggregate per model and period (`llm_usage`, for the quotas). The assistant, the contextual helper and the indexer's
embeddings call the gateway directly, so their calls were invisible per call: the view of the platform's spending was
wrong by construction.

## Decision

- **The gateway is the one ledger.** `Service.Complete` / `Service.Embed` write one row of `llm_call` per call, around
  the provider call, **refused calls included** (unknown alias, model disabled, forbidden, quota exceeded: error text,
  zero tokens). Every path to a model goes through them (in process, or over RPC), so nothing escapes. A row that cannot
  be written is logged and never fails the call. `Router` is below the ledger and only the service calls it
  (`TestEveryModelCallGoesThroughTheLedger`: a type outside the allow-list that implements the `llm.Client` /
  `llm.Embedder` contract, a `llm.ClientFunc` that is not the gateway's `Complete`, or a `Router.Complete` outside the
  service fails the build).
- **A row**: `seq` (monotonic, the cursor of a feed), `at_ms` (Unix ms, UTC: a day or an hour is an integer division in
  both dialects), `duration_ms`, `subject` / `project` / `org` (the principal's, **never declared**), `alias` (the name
  requested, `""` for a literal `provider/model`), `provider`, `model`, `kind` (`complete` | `embed`), `input_tokens`,
  `output_tokens` (an embedding's tokens are input), `error` (capped at 500 bytes), `source` and the correlation ids.
  Indexes: subject+at, source+at, model+at, process, at. The ledger **stores no prompt and no answer**.
- **Declared meta** (`llm.CallMeta`, pure, `pkg/llm`): `Source` (free-form, lower case `[a-z0-9._:-]`, at most 40 bytes,
  else `other`), `ConversationID`, `ProcessID`, `ChangeID`, `Step`, `Call`, `Action`, `Agent`. In process it travels in
  the context (`llm.WithMeta` / `MetaFrom`); over RPC as `CallMeta` on `CompleteRequest` / `EmbedRequest`, which
  `modelgw.Client` fills from its context and the `Handler` puts back in the context. It is accounting only and
  sanitised (control characters dropped, values capped); `Step` and `Call` are `-1` unless there is a process.
- **Who stamps**: the engine (`engine`: process, change, step, action, agent, call; planning calls are the first calls
  of the step their plan opens, then the host's: the numbering of the journal, so that `(change_id, process_id, step,
  call)` finds the exchange of ADR 0059, the `model.call` entry of the change log, which **stays there**), the assistant
  (`assistant`, conversation id), the helper (`helper`, inside `Suggest`), the indexer's embeddings (`indexer`), the
  intent ranker (`intent`). `cmd/engine` now forwards the caller's identity to the gateway (`identity.Forward`), as the
  in-process composition always did, so the engine's calls carry the initiator as subject.
- **Quota**: `llm_usage` stays, updated in the same step (one code path: `recordUsage` next to the ledger write). Deriving
  the quota from `SUM(llm_call)` was rejected: the retention purge would shrink the `total` and `month` periods, and the
  admission would scan a large table. The counter is admission state, not a view.
- **Reading**: `ListUsage(filter) -> {calls, next_seq, has_more}` and `UsageSummary(filter, group_by) -> rows` in
  `model.v1`. Filter: `from`, `to`, `subject`, `project`, `model` (id or `provider/model`), `alias`, `source`,
  `process_id`, `change_id`, `conversation_id`, `after_seq`, `limit` (default 500, at most 5000). Groupings: `model`,
  `alias`, `source`, `subject`, `process`, `action`, `agent`, `day`, `hour`; rows carry calls, input, output, errors and
  duration (day `YYYY-MM-DD`, hour `YYYY-MM-DDTHH`, UTC). Calls are ascending by seq: with `after_seq`, the first ones
  after it, without, the latest `limit`; `next_seq` is the cursor to resume from.
- **Visibility**: a caller reads **its own calls** (the subject is forced; naming another is `PermissionDenied`; no
  identity reads nothing); a platform administrator (`admin` on `platform`, as the catalog RPCs) any subject or the whole
  platform. Without an authorizer nobody is an administrator. The calls of a change a caller may see but did not start
  are not granted (the gateway has no access to the change): the engine stamps the initiator as subject, so the
  initiator sees them and an administrator sees all. Widening it means an `inspect` check on the change, as `prompt:inspect`
  does in the graph service, and is left out.
- **Live**: no event is published. A feed follows with `ListUsage(after_seq = next_seq)` (cheap: indexed by seq). The
  event hub of ADR 0053 is fed by the graph, engine and registry; a gateway publisher would be a new publisher and a new
  event kind for little gain.
- **Retention**: `GOAP_LLM_CALL_RETENTION_DAYS` (default 90, 0 keeps everything); purged at start and every 24 hours by
  `Service.KeepCalls` (`cmd/modelgw`, `goap-dev`).

## Consequences

- The per-call views (Tokens console, Token usage pane) are rebuilt on the ledger by the web (see "Web"); the engine's own record
  (`Process.Usage`, step `LLMCalls`, journal `ModelCalls`) stays for its purposes (totals, budgets, the exchange link),
  but is no longer a source of the views.
- Schema: `0003_llm_call.sql` in both dialects (`TestSchemasAligned`); a PostgreSQL variant of the ledger tests runs with
  `GOAP_TEST_PG_DSN`.

## Web

The ledger is the single source of every per-call token view; nothing is derived from the engine's process records any
more (`live.tokens`, `TokenRow`, `ingestProcess`'s derivation and `computeStats` are gone).

- `api/models.ts`: `listUsage` / `usageSummary` (types in `api/types/models.ts`). `stores/usage.svelte.ts` is the feed:
  the latest 500 rows, then a cursor follow every 2 s (`afterSeq`, backing off to 15 s on errors, a backlog read without
  waiting) while a view is attached (`attachUsage`) and the page visible; at most 2000 rows kept; `clearUsage` only hides
  the rows seen (a local floor on the seq). One scope for all views: the caller's own calls (`subject` = the caller) or,
  for administrators, the whole platform (no subject).
- Tokens console: one call per line with source and (platform scope) subject, filters by source / process / model /
  alias, totals of the visible rows; the prompt link (`ModelExchangeDialog`) only for `source === 'engine'` rows with a
  process and a change (`hasExchange`). Token usage pane: `UsageSummary` by model, alias, source, agent, action,
  process, subject (platform) and day / hour, `ListUsage` for the most expensive calls; it reloads when the feed brings
  new calls. Pure mapping in `web/src/lib/tokenStats.ts` (source labels, slices, buckets).
- Consequences of the single source: the time chart shows input / output per period (a summary has one grouping, so the
  stacked per-axis series are gone); a run counts its own calls (sub-agents are runs of their own); the "most expensive
  calls" are taken among the latest 5000 of the period. `Process.usage` (RunTab, ChangesExplorer) is the engine's own
  total and stays.

## Not done

- Reading the calls of a change one may see without having started it; a live event.
