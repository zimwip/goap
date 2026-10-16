# goap and the EU AI Act

This page is the provider's statement of what goap is for, how to operate it under Regulation (EU) 2024/1689 (the AI
Act) and where the technical documentation lives. It is an engineering document, not legal advice: the qualification of
a deployment (its purpose, its sector, who places it on the market) is for the deployer's counsel to confirm.

## Intended purpose

goap runs enterprise methodologies (software delivery, ALM, impact analysis, reviews, risk management) as agents that
plan with a goal-oriented planner and act through language models, scripts, tools and people, on a versioned knowledge
graph of the enterprise. Every modification is a change that people review before it lands. The models are third-party
general-purpose models reached through the model gateway; goap trains and fine-tunes none.

Roles: whoever distributes goap is the **provider of an AI system**; an organisation running it is a **deployer**.
goap is not a GPAI model provider; it is a downstream provider of the models it calls (Art. 53(1)(b): keep the
documentation of each model's provider with its model entry).

Risk class: with its shipped methodologies goap is **not high-risk** (Annex III). Its transparency obligations apply
(Art. 50).

## Excluded uses

goap is not designed, documented or evaluated for the uses below. A deployer who writes or runs a methodology for one of
them changes the intended purpose and becomes the provider of a high-risk AI system (Art. 25(1)(c)), with the obligations
of Art. 8 to 17 and 43:

- the uses of Annex III: biometrics; safety components of critical infrastructure (energy, water, transport, critical
  digital infrastructure); education and vocational training; employment and the management of workers (recruitment,
  selection, evaluation, promotion, termination, task allocation by personal traits); access to essential services
  (credit scoring, insurance pricing, public benefits, emergency dispatch); law enforcement, migration, justice,
  democratic processes;
- any practice of Art. 5 (manipulation, exploitation of vulnerabilities, social scoring, emotion recognition at work or
  in education, ...).

## Instructions for use

**Human oversight (Art. 14, Art. 26(1)-(2)).**

- Assign the roles of each methodology to trained people on each project (Assignments, ADR 0043); they answer for the
  runs and review the change impacts.
- Keep `GOAP_PERSON_REVIEWS_MODEL_OUTPUT` on (the default, ADR 0129): a change impact a model produced is accepted by a
  person only. Turning it off leaves oversight to each methodology.
- Prefer methodologies whose gates and guardians (ADR 0058, 0106) need a decided decision point or a human step before
  a merge, and declare independent verification on actions whose output matters (`verify:`, ADR 0075).
- An automatic review (`review: {automatic: true}` on a step, ADR 0130) runs a model without anyone launching it: its
  rating is an assessment, advice that accepts nothing; follow it with a step a person does (`sdlc`'s framing is
  confirmed by a person after the review of its description).
- Everyone can stop a run (cancel, the assistant's *Stop*), unblock or abandon it (ADR 0109); the engine bounds every run
  (`GOAP_MAX_STEPS`, failures, loop budgets).
- Review what an agent did before accepting it: the change tab shows each impact with its producer, the run's steps and
  what they relied on (ADR 0112), and what waits on people (ADR 0117). Beware of accepting model output by habit
  (automation bias): read the diff.

**Transparency (Art. 50).** The assistant and the helper say they are AI. Inside goap every impact and item names its
producer; the snapshot and OKF exports carry `provenance` (`model: true` for what a model produced); the PROV-O export
of a change carries the whole story. Keep that mark when you publish content goap produced.

**Logs (Art. 12, Art. 19, Art. 26(6)).**

- The change log (facts, journal of the runs, impact events, the request and answer of every model call of a run) is
  insert-only; a change that recorded an execution is never purged (ADR 0129), only abandoned.
- The ledger of model calls (`llm_call`, ADR 0089) records every call with its caller and end user. Keep
  `GOAP_LLM_CALL_RETENTION_DAYS` at 183 or more (the default) and `GOAP_LLM_CALL_PROMPTS` on (the default); the gateway
  warns at start otherwise.
- If you give units the Claude coding agent (ADR 0123), set `GOAP_MODEL_PROXY_SECRET` on the gateway and the MCP hub,
  and `GOAP_MODEL_PROXY_URL` on the hub (`goap-dev` does both): its sessions then call the model through the gateway and
  each call is a ledger row. Without them the sessions use the adapter's own key and their calls are logged nowhere in
  goap.
- Export a change's provenance (Audit pane, *Export PROV-O*) to keep it outside goap.
- These logs hold personal data (who asked what): give them a GDPR basis and a retention schedule.

**Accuracy and security (Art. 15).** Measure the actions you rely on with the offline evaluation (`make eval`,
ADR 0099) before and after changing a model or a prompt. Run coding agents in the Docker workspace (`workspace-docker`,
no network by default), not the local one, in production. Treat tool results and graph content given to a model as
untrusted input.

**AI literacy (Art. 4).** Train the people who configure methodologies and those who review their output on what the
agents do, their limits and the settings above.

## Technical documentation (Annex IV)

| Annex IV item | Where |
|---|---|
| General description, intended purpose, versions | this page; `README.md`; methodology and domain versions in the registry (ADR 0113) |
| Design, architecture, components | `docs/architecture.md`, `docs/architecture-overview.html`, `docs/adr/` |
| Logic of the system and its decisions | the methodologies (`methodologies/`, versioned graph nodes, ADR 0023), planners (`engine/goap`), conditions (ADR 0002, 0109) |
| Models used | the model entries of the platform namespace (`platform@LlmModel`), the ledger per call |
| Human oversight measures | ADR 0058, 0067, 0075, 0090, 0105, 0106, 0115, 0117, 0129, 0130 |
| Logging | ADR 0011, 0030, 0057, 0059, 0089, 0114, 0123 §6 |
| Accuracy, robustness, cybersecurity | ADR 0007 (sandbox), 0099 (evaluation), 0118 (workspace), 0020 / 0045 (access, sessions) |
| Changes over the lifecycle | `git log`, the ADRs, the versions of methodologies and domains |

## Known gaps

Not yet provided: tamper evidence on the logs (hash chain or WORM export), rules on which data may reach which model
provider (classification, redaction, residency), continuous evaluation with kept results, a documented posture against
prompt injection, a `Generated-by` mark on commits agents push, a post-market monitoring and incident procedure
(Art. 72-73). A deployer bound by the high-risk requirements needs them.
