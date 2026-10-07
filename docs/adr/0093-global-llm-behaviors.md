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
- **One built-in example, off.** `graphsvc.SeedBehaviors` (a step of `Boot` after `SeedProtectedAliases`) creates `terse`
  (`llmcfg.TerseBehavior`: "Answer tersely: drop articles, filler, pleasantries and hedging; fragments are fine; keep technical
  terms, code, identifiers and errors exact; never shorten security warnings or irreversible-action confirmations.")
  disabled, only when no node of that key exists: an edited, enabled or retired `terse` is never touched again. Nothing else
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

## Consequences

- A style costs input tokens on every matching call (the instruction) and saves output tokens; the preview and the ledger
  column make the cost visible.
- Behaviours do not reach calls with `JSON: true` by default, so the platform's protocols cannot be broken by turning `terse` on.
- A behaviour of a retired node leaves at the next snapshot (one second by default), like the other LLM configuration.

## Not done

Per-organisation or per-project behaviours (resolved along the unit chain like the adapters, ADR 0054); behaviours on a
call-by-call basis from a methodology; a measured (not estimated) count of the added tokens.
