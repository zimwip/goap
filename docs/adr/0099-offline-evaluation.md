# ADR 0099 — Offline evaluation of LLM work

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0089 (the ledger of LLM calls), 0098 (native tool
calling).

## Context

The prompts of the engine's LLM actions, the assistant and the models behind the aliases change; nothing measured
whether a change made the agents better or worse. The tests run on scripted models: they check the mechanism, not the
quality of what a real model does with a prompt. The runtime checks (verification, ADR 0075; the observer, ADR 0011)
come after the fact, on real changes.

## Decision

`pkg/eval` runs **cases** against a model and scores them; `cmd/goap-eval` runs case files against the model gateway.

- **A case** (YAML, `evals/`): a system text (with `prompt: engine`, the item protocol of the engine's LLM actions
  comes first: `engine.ActionSystem`), the messages, `json`, the **tools** the model may call, **mocked**: each answers
  the first `responses` entry whose `args` are a subset of the call's arguments, else its `result` or `error`; and
  `expect`: `json`, `items` (each a subset of an item of `{"items": [...]}`: maps key by key, lists element by element),
  `contains` / `notContains`, `trajectory` (the tools called, `exact`, `in_order` or `any_order`), `maxToolCalls`,
  `noToolErrors`; optionally a **judge** (a model scores the answer against a rubric, 0 to 1, against a `threshold`,
  0.7 by default). `runs` and `minPassRate` make a case a measure of reliability rather than a single draw.
- **The run** is the tool loop of an engine LLM action: the tools are given natively (`llm.Request.Tools`, ADR 0098),
  calls written as JSON text are honoured, results go back by call id, `maxRounds` bounds it. Each run records its
  checks, trajectory, rounds, tokens (judge included), latency and answer; a case reports its pass rate and the means.
- **The command** calls the gateway as the user of a token (`-token` / `GOAP_TOKEN`, `-url` / `GOAP_EVAL_URL`): the
  calls go through the admission (catalogue, roles, quota) and the ledger like any other, with the source `eval`
  (`llm.SourceEval`) and the conversation `eval:<case>`; `-model` and `-runs` override the cases, `-json` writes the
  report; the exit status is 1 when a case misses its pass rate. `make eval` / `.\make.ps1 eval` run `evals/`.

## Consequences

- A prompt or alias change can be compared before and after, on the same cases, by pass rate, tokens and latency.
- The tools are mocks: a case measures what the model does with given tool answers, not the tools.
- Not done: building cases from recorded runs (the change log keeps the exchanges as text, ADR 0059, not the mocked
  answers a case needs), user simulation for the assistant, and evaluation inside CI (it needs a model and a key).
