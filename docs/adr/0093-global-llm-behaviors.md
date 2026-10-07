# ADR 0093 — Global behaviours of the LLM calls

**Status**: accepted, implemented · **Date**: 2026-10 ·
Builds on ADR 0021 (model gateway configuration as graph data), ADR 0076 (nodes are retired, never deleted), ADR 0089 (the
ledger of LLM calls and their exchanges), ADR 0084 (a protected-alias style seed).

## Context

An administrator wants a platform-wide way of speaking for the models, for example make every call work on the "caveman"
principle: terse answers (no articles, filler or pleasantries, fragments allowed, technical content exact), which cuts the
output tokens of every run, assistant turn and helper suggestion. Editing every prompt of every action, agent and service is
neither possible (methodologies are data of the organisations) nor wanted. The question it answers is *how the platform
talks to its models*, so it is platform configuration, next to the aliases and the quotas.

## Decision

- **A behaviour is graph data.** New node type `platform@LlmBehavior` (key `LLB:<name>`, lifecycle `config`, `adminOnly`, so
  only platform administrators change it, through a change like the rest of the LLM configuration: versioned, reviewable,
  retired never deleted). Attributes: `name`, `description`, `instruction` (the text, at most 4 KiB), `enabled`, `position`
  (`prepend` | `append`, default append), `order` (lower first, then by name), and scope selectors, all optional and ANDed:
  `aliases`, `models` (`provider/model`), `sources` (`engine`, `assistant`, `helper`, `indexer`, `intent`, `other`: the
  `llm.CallMeta` source), `kinds` (`complete` only; `embed` is refused by validation) and `appliesToJSON` (default false).
  Go model `llmcfg.Behavior` (`Props` / `BehaviorFromProps` / `Validate`); `Snapshot.Behaviors` holds the ones in force
  (enabled, not retired), `Snapshot.Off` the disabled ones for the administrators' listing; malformed ones are `Problems`.
- **One built-in default, on.** `graphsvc.SeedBehaviors` (a step of `Boot` after `SeedProtectedAliases`) creates `terse`
  (`llmcfg.TerseBehavior`: "Answer tersely: drop articles, filler, pleasantries and hedging; fragments are fine; keep technical
  terms, code, identifiers and errors exact; never shorten security warnings or irreversible-action confirmations.")
  enabled (the platform answers tersely by default; it still skips every call that requires JSON), only when no node of that
  key exists: an edited, disabled or retired `terse` is never touched again. Nothing else
  is hardcoded; a behaviour is an ordinary retirable node.
- **Applied at the gateway, as close to the call as possible.** `modelgw.Service.Complete`, after `admit` and before
  `Router.Complete`: `Snapshot.Apply(system, CallInfo)` (pure, in `pkg/llmcfg`) selects the enabled behaviours matching the alias
  requested (`default` when none; a literal `provider/model` matches no alias selector), the resolved provider/model and the
  source, sorts them, puts the `prepend` ones before the request's system text and the `append` ones after, joined by blank
  lines. The request is the gateway's by-value copy: the caller's `System` is never mutated. Embeddings never get a behaviour
  (`Embed` does not look at them, validation refuses the `embed` kind). The quota admission counts nothing special (the added
  tokens are part of the provider's input tokens).
- **Hard cap**: the text added to one call is at most 8 KiB (`llmcfg.MaxBehaviorBytes`); in order, a behaviour that no longer
  fits is dropped, logged as a warning and recorded in the ledger as skipped (`!name`).
- **JSON calls.** Every call that depends on structured output sets `llm.Request.JSON`: the engine's `llm` actions and their
  items / tool-call protocol (`pkg/engine/executors.go`, `llmSystem`), the planners (`planners.go`), the DSL `complete` with
  `json`, the intent rankers, the helper (`suggest.go`) and the assistant turn all do. The gateway therefore skips, for a call
  with `JSON` true, every behaviour whose `appliesToJSON` is false (the default). One flagged `appliesToJSON` is applied wrapped:
  `These style rules apply to free-text fields only; the output format required by the other instructions is unchanged.`
  followed by the instruction (`llmcfg.JSONNotice`; "by the other instructions" rather than "below", since a behaviour may be
  prepended or appended). **Limit**: the gateway cannot detect a protocol that asks for a structured answer in the prompt only
  without setting `JSON` (no such caller exists today; a new one must set `JSON`). A text-mode call of a free-text purpose gets the behaviours as
  any other; an administrator who must protect a specific caller scopes by `sources` / `aliases`. `terse` is therefore
  effective on free-text calls only until it is also flagged for JSON, and the settings pane says so.
- **Audit.** The gateway reports what it added in `llm.Response.Behaviors` / `BehaviorTokens` (`CompleteResponse.behaviors`,
  `behavior_tokens` over RPC) and in the ledger row (`llm_call.behaviors`: comma separated names, `!name` for a dropped one;
  `behavior_tokens`: an estimate, four bytes a token; migration `0005_llm_call_behaviors` in both dialects, `LLMCall.behaviors`
  / `behavior_tokens`). The exchange recorded is **as sent where the gateway records it, and the caller's text plus the list
  where the change log does**: the ledger's `llm_call_exchange` holds the effective system text (behaviours included), while the
  engine's `model.call` entry of the change log is built by the engine, which only sees its own text, so `journal.ModelExchange`
  keeps that text and gains `behaviors` / `behaviorTokens` from the response (a single place records the gateway's addition: the
  gateway; the log names it rather than copying it into every entry, and does not need the gateway's configuration at the time
  of reading). The prompt dialog shows an "Applied behaviours" line that says which of the two holds, the Tokens console a
  Behaviours column in its most expensive calls.
- **Admin API.** `ListBehaviors` (enabled and disabled, the caps, the sources) and `PreviewBehaviors {alias, source, json,
  system}` (pure: the effective system text, the behaviours applied or skipped, the estimated tokens) on `model.v1`, authorized
  like `ListCatalog` (`admin` on `platform`; no behaviour text is shown to other callers). The preview reads the behaviours in
  force (saved), not the staged ones.
- **Web.** Settings dialog, section "LLM behaviours" (`BehaviorsPane.svelte`): the list with an enabled toggle, order and scope
  summary, an edit form (instruction counter, alias / model / caller pickers, the JSON switch), a preview box, a note on token
  cost and JSON calls. Edits are staged like the catalog's (`llmEdit.ts`: `saveBehavior`, `setBehaviorEnabled`,
  `retireBehavior`, `overlayBehaviors`); the pure logic is `web/src/lib/behaviors.ts`.

## Measured cost

The estimate of the tokens a behaviour adds (four bytes a token) is replaced by a measure on the real model, kept and refreshed.

- **Measure** (`internal/modelgw/behaviorcost.go`). The cost of an instruction on a model is the input tokens the provider reports for a
  minimal request carrying the instruction as its system text, minus those it reports for the same request with no system text (the
  baseline of the model): one user message `.`, `MaxTokens` 1, no JSON, never negative (`costOf`). When the provider reports no usage
  (zero), or no difference between the two requests, there is no measure: the byte estimate is stored with source `estimated`, so it
  is not asked again at every call (the UI says so). The instruction measured is the trimmed text; the JSON notice of a JSON call
  and the separators are left to the estimate (a few tokens).
- **The calibration call goes through the gateway** (`Service.complete(..., calibration=true)`): admission, quota and ledger as any call,
  with three exceptions: no behaviour applies to it (it must measure the bare instruction), the roles of the model are not checked
  (the call is the gateway's own, not a caller's), and the source is `calibration` (`llm.SourceCalibration`, set by the gateway,
  never a scope of a behaviour; "Calibration" in the Tokens console). **Subject**: `authz.System("modelgw")` (`system:modelgw`) for
  the lazy and the explicit trigger alike, so a calibration is never attributed to the user whose call or click caused it and the
  meta cannot forge it. **Quota**: it is admitted and counted like any call (a few dozen tokens per measure), but it never fails a
  quota-less install, and a model whose quota is exhausted refuses it (`ErrQuotaExceeded`): nothing is stored, the estimate keeps
  serving, the failure backs off.
- **Store** (`llm_behavior_cost`, `llm_model_baseline`, `llm_call.behavior_tokens_estimated`: migration 0006, both dialects, `Store`
  interface, memory and SQL). Key: behaviour name, `instruction_hash` (sha256 of the trimmed instruction), provider, model; columns
  `tokens`, `baseline_tokens`, `measured_at`, `source` (`measured` | `estimated`). A changed instruction or an alias retargeted to
  another model is simply an absent key, measured on its first use; the rows of an old text, of a retired behaviour or of a model
  that left the catalog are purged lazily after each measure (`pruneCosts`). Entries older than `GOAP_LLM_BEHAVIOR_COST_TTL_DAYS`
  (default 30) are measured again, and meanwhile still price the calls. No automatic drift detection. The service reads the table
  into a cache, reloaded every minute.
- **When**: lazily and asynchronously. `Complete` prices the behaviours it applied (`Service.price`): the stored cost where there is one
  (`llm.Response.BehaviorTokens`, ledger `behavior_tokens`, flag `BehaviorsEstimated` / `behavior_tokens_estimated` false unless one
  cost is an estimate), the byte estimate otherwise (flag true), and enqueues a calibration for each key without a fresh cost:
  deduplicated per key (many concurrent calls, one calibration), at most two in flight, in the background with its own context (the
  caller neither waits nor sees an error), and after a failure the key waits `CostBackoff` (10 minutes) before another attempt. Not
  at start-up (it would spend tokens on models that may never be used): the first application, or the explicit trigger.
- **API**. `ListBehaviors` gives each behaviour `costs` (`BehaviorCost`: model, the aliases reaching it that the behaviour applies to,
  tokens, baseline, source, `measured_at_ms`; the estimate with `measured_at_ms` 0 when none is stored, disabled behaviours included
  so the cost is known before enabling). `PreviewBehaviors` returns `added_tokens` for the previewed alias with
  `added_tokens_estimated`. `MeasureBehaviors {names, models}` (admin, same guard as `ListBehaviors`) measures now, whatever the age
  of the stored costs and the backoff, and returns the costs, a failed one with its `error` (the estimate kept). The ledger row, the
  `CompleteResponse` (`behaviors_estimated`) and `journal.ModelExchange` (`behaviorsEstimated`) carry the flag.
- **Web**. `BehaviorsPane.svelte`: per behaviour and model "≈ 23 tokens on claude-x" (estimate) or "23 tokens on claude-x, measured
  2026-10-07", a "Measure costs now" button (progress, errors), the preview line marks estimate or measure; the prompt dialog and the
  Tokens console mark an estimate with "≈" (`behaviors.ts`: `costLine`, `behaviorsLine`, `addedLine`).

## Consequences

- A style costs input tokens on every matching call (the instruction) and saves output tokens; the preview and the ledger
  column make the cost visible.
- Behaviours do not reach calls with `JSON: true` by default, so the platform's protocols cannot be broken by turning `terse` on.
- A behaviour of a retired node leaves at the next snapshot (one second by default), like the other LLM configuration.

## Not done

Per-organisation or per-project behaviours (resolved along the unit chain like the adapters, ADR 0054); behaviours on a
call-by-call basis from a methodology.
