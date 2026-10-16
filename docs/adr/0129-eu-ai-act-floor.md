# ADR 0129 — An EU AI Act floor: disclosure, logs kept, provenance carried out, a person reviews model output

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0030 (one change log), ADR 0057 (PROV-O),
ADR 0059 (prompts in the log), ADR 0075 (review policy), ADR 0089 (ledger of model calls), ADR 0104 (snapshot export),
ADR 0111 (OKF export)

## Context

goap is an AI system (agents planning and acting with models, Regulation (EU) 2024/1689 Art. 3(1)); whoever
distributes it is its provider, an enterprise running it a deployer. It is no GPAI model provider: it trains no model,
the model gateway routes to third-party ones. With its shipped methodologies it is not high-risk (Annex III), but it
lets a customer write any methodology, and a customer who uses it for an Annex III purpose becomes the provider of a
high-risk system (Art. 25(1)(c)). The mechanisms that Art. 12 (logs) and Art. 14 (human oversight) ask for were in
place; a review against the Act (`docs/ai-act.md`) found that several defaults and edges defeated them:

- the assistant and the helper never said they were an AI (Art. 50(1));
- the ledger of model calls was kept 90 days by default, under the six months of Art. 19 and Art. 26(6), and turning
  the prompts off said nothing;
- `PurgeChange` deleted a change with its whole log when it had landed nothing, the journal of its runs and the
  exchanges with the models included;
- the snapshot and OKF exports dropped who or what produced a version (Art. 50(2) asks AI output to be marked in a
  machine-readable form);
- oversight was what the methodology said: an llm action could accept the change impacts it wrote, a script the ones
  a model wrote, so a change could land with no person having reviewed what a model produced.

## Decision

1. **Disclosure.** The assistant shows an *AI* badge on its title and, under the input of every conversation,
   `AI_DISCLOSURE` (`web/src/lib/assistant/enabled.ts`); the helper's bubble says *AI suggestion*.
2. **Six months of logs.** `modelgw.DefaultCallRetentionDays` is `MinAuditRetentionDays` (183); a shorter
   `GOAP_LLM_CALL_RETENTION_DAYS` is allowed and logged as a warning at start (`Service.KeepCalls`), so is
   `GOAP_LLM_CALL_PROMPTS` off. `0` still keeps everything.
3. **The record of an execution is never purged.** `Graph.PurgeChange` refuses (`ErrConflict`) a change whose log holds
   an entry of a stream the graph does not write itself (`recordsExecution`: anything but `fact`, `impact`, `change`,
   i.e. the journal of a run, the exchanges with a model); such a change is abandoned, not purged. Personal changes
   staging settings and the changes of a seed are unaffected: they hold no execution.
4. **Provenance carried out.** A `SnapshotNode` has `provenance` (`graph.SnapshotProvenance`: version, the change that
   wrote it, `producedBy` of its change impact, `model: true` when the action run that produced it called a model, read
   from the `model.` stream of the change, `domain.LogModel`); an import ignores it. The OKF export writes it in the
   front matter (`provenance`, `okf.KeyProvenance`; a property of that name is prefixed). The whole story stays the
   change's PROV-O export.
5. **A person reviews what a model produced** (`Engine.PersonReviewsModelOutput`, `engine/engine/oversight.go`; set from
   `GOAP_PERSON_REVIEWS_MODEL_OUTPUT`, **on by default** in `goap-dev` and `platform/cmd/engine`, off for an engine built
   bare, as in the tests). The node operations of an automated action run (llm, script, tool, builtin: `withAutomated`,
   the executor's result) may reject any change impact and accept one no model produced (a deployment a script records),
   but an llm action accepts nothing and no automated action accepts an impact whose producing run called a model (a
   `model.` entry of that execution, or its journal record of kind llm or with model calls); `authz.ErrForbidden`, the
   action fails and the planner falls back (a human action, a person's review). The verdicts of a person (a human
   action, a review from the web or `goap-change`) are not constrained. The check is the engine's, which knows what an
   action is; the graph keeps knowing no action kind.

## Consequences

- A methodology whose llm actions accepted their own output now needs a human step (or a review in the change tab):
  that is the point. `sdlc`'s llm actions only reject; its script that records deployments accepts what it wrote and
  stays allowed.
- Deployers who must keep less than six months (data minimisation) set the retention and get a warning, not a refusal.
- Not done: a `Generated-by` trailer on commits pushed by agents (the agents commit through `bash`, the workspace only
  pushes), tamper evidence on the logs (hash chain / WORM), data rules on what may reach which provider, continuous
  evaluation, a criticality-driven floor finer than on / off. `docs/ai-act.md` lists them.
