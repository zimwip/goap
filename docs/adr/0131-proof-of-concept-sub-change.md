# ADR 0131 — Proofs of concept and prototypes: a relaxed build in a sub-change, to derisk or to explore the need

**Status**: accepted, implemented (see *As built*) · **Date**: 2026-10 · Builds on ADR 0036 (risks and actions, transverse risk management), 0075
(criticality), 0103 (sub-changes on their own branch), 0106 (methodologies integrating methodologies), 0109 (scoped
conditions, assessments as verdicts of a claim), 0110 (reactive activities, methodology events), 0115 (forms of human
actions), 0118 (workspace of a change), 0129 (a person reviews model output)

## Context

`sdlc` meets its risks through the transverse `risk-management` (ADR 0036 §3): at every step completed it identifies
and scores the risks of the change, and every high one (score >= 9) gets an action, or is accepted. The actions are
free text: nothing in the methodology *does* them, and the only gate is `risks_under_control` before the release train,
long after the build has spent the effort.

Many risks of a software change are questions that only running code answers: will the library do what we expect, will
the two systems talk, does it hold the load, can the legacy component be extended at all. The usual answer is a
**proof of concept** (or a prototype): a small, throw-away build that checks one thing works as expected, before the
change invests in the full delivery. Today a team would do it outside GOAP, and its result would come back as a
sentence in an action.

What a POC must not be is a second, lighter delivery path: its code is built without the quality rules of the
delivery (no traced requirements, no design check, no coverage, no review), so nothing it builds may reach a release.

## Decision

A POC is a **mitigation action of kind `poc`** of the risk register that, once a person opens it, becomes a
**sub-change following a methodology of its own, `poc`**: its requirements are the risks it checks, it builds in the
workspace of the change under relaxed rules, concludes with a verdict per risk, carries the verdicts back to the risk
register of its parent, and is closed without landing anything.

```
sdlc change (C2)                                    poc sub-change (C1, methodology poc)
  risk-management: RSK-3 "the PSP SDK cannot          ┌──────────────────────────────────────────────┐
  refund partially" (4x4=16)                          │ risks RSK-3, RSK-5 (copied: its requirements) │
  action ACT-2 {kind: poc, risks: [RSK-3, RSK-5]} ──► │ framing: one probe per risk (what to observe) │
  derisking/open_poc (a person) ── create(parent) ──► │ build: relaxed, in the workspace              │
                                                      │ conclusion: an assessment per risk            │
  derisking/conclude  ◄── poc.concluded (integrated) ─┤ (confirmed: the risk holds / refuted)         │
    verdicts copied, risks rescored, ACT-2 done       └──────────── closed (abandoned), lands nothing ┘
  build waits for pocs_concluded
```

### 1. The POC is a mitigation action: `kind: poc`

A POC is decided where mitigations are decided, on the risk register. The `action` item of `contracts/risk` gains three
optional fields:

| Field | Meaning |
|---|---|
| `kind` | `mitigation` (default, what exists today) or `poc` |
| `risks` | the keys of the risks the POC checks (`for` stays the first one, so existing readers keep working) |
| `change` | the id of the sub-change carrying it, once opened |

`validateRecord` checks that a `poc` action names at least one risk of the register, and `risk.Actions` folds the new
fields (`ActionItem.Kind`, `Risks`, `Change`). `lib/brief` shows them (`ACT-2 poc [RSK-3, RSK-5] → change <id>`).

Anyone may propose one: a person from the *Risks & actions* pane (*Propose a POC* on one or several risks), or
`risk-management` 1.2.0, whose `plan_mitigation` prompt may answer a `poc` action for a high risk that is a question of
feasibility, integration or performance (the kinds of risk running code settles). A proposal is only an `open` action;
**nothing is built until a person opens it** (§3).

The `risks` library changes how an action covers a risk, and gains the conditions of the POCs (§6bis gives the whole
chain).

### 2. A methodology of its own: `poc`

The POC is not `sdlc` with switches. It answers another question (*does this work?*, not *deliver this*), its route is
short and its rules are the opposite of the delivery's on purpose; a mode flag spread over the conditions of `sdlc` would
make both harder to read. `methodologies/poc.yaml` (namespace `alm`, `criticality: C1`, goal `conclude`):

```yaml
name: poc
namespace: alm
criticality: C1                # ADR 0075: the lowest level; its oracles accept a tool or a model
goal: conclude
imports: [risks, assessments]
events:
  - {name: poc.concluded, description: "A person confirmed the verdicts of the POC on the risks it checks"}
processes:
  - name: proof_of_concept
    steps:
      - name: framing          # one probe per risk: what to build, what to observe, the criterion that refutes it
        actions: [propose_probes, write_probes]          # llm, then a person (human fallback)
      - name: confirmation     # a person confirms the probes and the timebox (manual step)
      - name: build
        method: poc_build      # relaxed, in the workspace of the change (§4)
      - name: conclusion       # an assessment per risk, drafted by a model, confirmed by a person
        actions: [draft_verdicts, conclude_by_hand]
      - name: closing          # emits poc.concluded once a person confirmed
        action: confirm_conclusion
```

Its **requirements are the risks**: at its creation the sub-change receives a copy of the risks it checks as its own
`risk` items (same keys, `data.from` = the parent change), so the CEL `risks`, the brief and the assessments of
`item:RSK-n` (ADR 0110 §6) work in the POC as in any change. They are not `alm@Requirement` nodes: a POC lands nothing
in the repository (§5), and a requirement written only to be thrown away would pollute the traceability of the
delivery. A **probe** (an `artifact` of target `probe`, one per risk: `risk`, `build` what to build, `observe` what to
measure, `refutes` the criterion that retires the risk, `check` the command that shows it) is the acceptance criterion
of one requirement. The conditions are scoped per risk (ADR 0109 §3): `probed`, `probe_run`, `judged` on scope `risk`,
`all_probed`, `all_judged` their aggregates.

`risk-management` does not apply to `poc` (`appliesTo: [sdlc]` unchanged): a POC does not open a risk register of
its own about itself.

### 3. Opening the POC: a sub-change, opened by a person

`sdlc` 0.10.0 declares `integrates: [{methodologies: [poc]}]` (ADR 0106, not exclusive) and an optional process step
`derisking`, between the design and the build, made of two reactive steps (ADR 0110):

- `derisking/open_poc` reacts to `change.item_added` with an `action` of kind `poc` and no `change`. Its action
  `open_poc` is **human** (ADR 0115 form: the risks, prefilled from the action; the components whose sources the POC
  needs, among the impacts of the change; a timebox; free notes on what to try). Its script creates the sub-change
  (`goap-change/create` with the new `parent` argument: title `POC: <action title>`, intent generated from the risks
  and the notes, methodology `poc`, `Data.poc = {action, risks, timebox}`), copies the risks into it, declares the
  chosen components as its impacts (checked out, never accepted, so the workspace knows their repositories) and writes
  a new version of the action with `change`. A POC that a person starts on its own (a root `poc` change) may be
  attached later (`goap-change/attach`, ADR 0106): the parent's `open_poc` is then not needed.
- `derisking/conclude` reacts to `poc.concluded` **from an integrated change** (`on: {event: poc.concluded, from:
  integrated}`, §6).

The delivery waits for its POCs where it would otherwise spend the most: `software_delivery/build` gains
`pocs_concluded` in its `pre`. With no POC the condition holds and nothing changes; a change can still be built without
waiting by cancelling the action (a person's decision, recorded in the register). The design is not held: a POC usually
follows from the design, and its verdicts may send the design back (a refuted risk changes nothing; a confirmed one gets
another mitigation, §6).

### 4. The relaxed build: what is waived, what is kept

`poc_build` reuses the workspace of ADR 0118 with a shorter route. The rules of the delivery it **waives**:

| Waived in a POC | Rule of the delivery |
|---|---|
| framing review, `description_accepted` | the intent of a POC is generated from its risks and confirmed by a person |
| the four passes of the impact analysis and its validation | the components are chosen by the person who opens it |
| specification, traceability, test plan | the risks and their probes are the requirements |
| design and its check | — |
| the quality checks of the build (coverage, dependency check, lint, `buildCommand` of the component) | the probe's own `check` runs instead |
| publication (`publish_build`, `alm@BuildArtifact`), release train, application | nothing is published nor released |
| `risk-management` | — |

What it **keeps**, because a POC that lies is worse than none:

- **The run, not the agent, says it works.** `implement_probe` (llm, `mcps: [workspace]`, scoped per risk, a small loop
  budget: `maxRounds: 60`, `timeout: 30m`) writes the code; `run_probe` (script) runs the probe's `check` in the workspace
  and records a `probe_report` (`risk`, `ok`, `output`, revision). A failed run sends it back to the agent, three
  attempts, then a person.
- **The sandbox.** Same workspace connectors (docker preferred), same secrets handling; a POC is code a model wrote.
- **The timebox.** `Data.poc.timebox` bounds the loop budgets and the step deadline; past it the POC concludes with
  what it has (an unfinished probe is a verdict of its own: *not shown*).
- **A person concludes** (ADR 0129). The verdicts are `assessment` items of subject `item:RSK-n` with the verdicts of a
  claim (ADR 0109): `confirmed` (the risk holds: the POC could not show it works), `refuted` (it works as expected),
  with `findings` and the evidence (the report, the commit). A model drafts them (`draft_verdicts`), a person confirms
  (`confirm_conclusion`, human): an assessment advises, it never accepts an impact, so `checkOversight` is untouched.

### 5. A POC lands nothing

The code of a POC stays on its own branch of the repository (`goap/<poc change>`, pushed by `workspace/push` for the
record, never a build artifact). The sub-change commits nothing and is never merged: after its conclusion is carried
back (§6), the parent's `conclude` closes it as **abandoned** (reason *POC concluded*), so it neither holds the merge of
its parent (`unsettled` counts open sub-changes only) nor brings anything into it. Its log keeps the whole run, and
`PurgeChange` keeps refusing it (ADR 0129: it recorded an execution).

The delivery may learn from it but not reuse it: `implement_component`'s prompt shows the concluded POCs of the change
(branch, findings) as *references*, and says the component is built again under the rules of the delivery.

### 6. Carrying the verdicts back

`derisking/conclude` (script, run by the reaction to `poc.concluded`) reads the sub-change (`goap-change/read`, its
assessments) and writes, on the parent:

- the assessments of `item:RSK-n`, copied with `data.from` = the POC (the bubbles of the risks show them, ADR 0116);
- per risk a new version: **refuted** → `status: closed` with the rationale and the POC as evidence (a person may reopen
  it); **confirmed** → stays live, probability raised to at least its former value, the POC named in its description:
  `risk-management` reviews it at its next run and asks for another mitigation;
- the action `status: done`, `result` = one line per risk;
- the close of the POC (§5).

Each change writes only its own blackboard; the POC never writes on its parent.

### 6bis. Conditions and effects, end to end

The orchestration is the usual one (ADR 0109): every condition is derived from the blackboard, an effect is a
declaration the engine checks afterwards, and a run whose missing condition no action of its agent produces **waits**
for it (ADR 0036 §3) and is tried again when the change moves. No condition is both an input and an output of an
action (ADR 0051): the per-POC work is scoped (ADR 0109 §3), each action producing a scoped condition of its subject.

**What `risks_under_control` means.** It is not "every risk remediated": it is "every live high risk (score >= 9) is
covered by an action". Today `a.for == r.key && a.status != "cancelled"`. A POC changes it in two ways:

```
covers(a, r)  = (a.for == r.key || r.key in a.risks) && a.status != "cancelled"
                && !(a.kind == "poc" && a.status == "done")      # a concluded POC mitigates nothing any more
unmitigated_risks   = risks.exists(r, r.live && r.score >= 9 && !actions.exists(a, covers(a, r)))
risks_under_control = !unmitigated_risks                         # the release gate, unchanged in meaning
```

So a running POC covers its risks (the change may go on), and once it is concluded each risk is settled by its
verdict: **refuted** → the risk is closed, no longer live, the gate holds; **confirmed** → the risk stays live and the
done POC no longer covers it, so `unmitigated_risks` is true again, `risk-management` (`plan_mitigation`, effect
`risks_under_control`) is planned again on its next event and asks for a real mitigation or an acceptance. A POC can
never leave a confirmed risk silently "under control".

**The parent change (`sdlc` + `risk-management`).** Scope `action` restricted to `self.kind == "poc"`:

| Condition | Scope | Expression (sketch) |
|---|---|---|
| `poc_opened` | action | `self.change != ""` |
| `poc_concluded_now` | action | `has(vars.event) && vars.event.type == "poc.concluded" && vars.event.change.id == self.change` |
| `poc_settled` | action | `self.status != "open"` |
| `pocs_opened` | aggregate | all `poc_opened`, open POCs, empty: true |
| `pocs_concluded` | aggregate | all `poc_settled`, empty: true (no POC: holds, the POC is optional) |

| Action | Kind | Pre | Effects | Who runs it |
|---|---|---|---|---|
| `plan_mitigation` (risk-management) | llm | `risks_reviewed` | `risks_under_control` | may write a `poc` action (proposal) |
| `open_poc` (`derisking/open_poc`) | human, scoped per POC action not opened | — | `poc_opened` | its reaction to `change.item_added` of a `poc` action; a person fills the form |
| `conclude_poc` (`derisking/conclude`) | script, scoped per opened POC | `poc_opened`, `poc_concluded_now` | `poc_settled` | its reaction to `poc.concluded` from the integrated POC |
| `build` step | — | adds `pocs_concluded` | — | the delivery run |

The delivery run reaching the build with a POC open misses `pocs_concluded`; the only producer is `conclude_poc`, whose
`poc_concluded_now` exists only in the run of the reaction (it reads the event): the delivery run cannot establish it,
so it **waits** (a `condition` task on `pocs_concluded`, shown in *Waiting for others*), and is tried again when the
change moves, which `conclude_poc` does by writing the done action.

**The POC sub-change (`poc`).** Its copied risks are the subjects (scope `risk`):

| Condition | Scope | Expression (sketch) |
|---|---|---|
| `probed` | risk | an artifact `probe` with `data.risk == self.key` |
| `probe_implemented` | risk | a `probe_change` (code change) for the risk, no failed run since, three attempts at most |
| `probe_run` | risk | a `probe_report` of the latest revision, or the timebox is over (an unfinished probe counts: verdict *not shown*) |
| `judged` | risk | an assessment `item:<key>` with a verdict `confirmed` / `refuted` |
| `all_probed`, `all_probe_run`, `all_judged` | aggregates | over the copied risks |
| `conclusion_confirmed` | change | an artifact `poc_conclusion` written by a person |

| Step / action | Pre | Effects |
|---|---|---|
| framing: `propose_probes` (llm), `write_probes` (human fallback) | — | `all_probed` |
| confirmation (manual) | `all_probed` | the step's own exit (history) |
| build: `prepare_workspace` | `step:…/confirmation` | `workspace_ready` |
| build: `implement_probe` (llm, scoped per risk) | `workspace_ready` | `probe_implemented` |
| build: `run_probe` (script, scoped per risk) | `probe_implemented` | `probe_run` |
| conclusion: `draft_verdicts` (llm), `conclude_by_hand` (human) | `all_probe_run` | `all_judged` |
| closing: `confirm_conclusion` (human, `emits: [poc.concluded]`) | `all_judged` | `conclusion_confirmed` |

The goal `conclude` is `{conclusion_confirmed: true}`. `confirm_conclusion` raises `poc.concluded` once its effect
holds (ADR 0110, `Action.Emits`); the relay of the engine carries it to the parent (`from: integrated`, the reason
`sdlc` declares `integrates: [poc]`), where `conclude_poc` runs. A failed probe never blocks: after three attempts the
risk is still `probe_run` (with a failed report), and the person who concludes reads a *confirmed* verdict from it.

### 7. Platform changes

Small, and generic:

1. `goap-change/create` takes `parent` (the RPC has `parent_id` already) and `data` (the free-form `Change.Data`).
2. `goap-change/close`: abandon an open change with a reason (`change:abandon`, its own sub-changes only for an agent).
3. `contracts/risk`: `kind`, `risks`, `change` on actions; the coverage of a risk by an action (§6bis) in the `risks`
   library, the scoped conditions of the POCs in `sdlc`;
   the brief; `goap-change/risk`'s twin `goap-change/action` (an action is written today only as an item).
4. The criticality of a sub-change: today the default of the methodology is written only when the engine creates the
   change itself (`engine/engine/engine.go`); `goap-change/create` applies it too. A sub-change of another methodology
   starts from **its own methodology's** default (`poc`: C1), not its parent's level, without asking
   `change:lower-criticality`: it lands nothing.
5. Web: *Propose a POC* on the Risks & actions pane (a selection of risks), the POC action row linking its sub-change
   and its verdicts; the rest is what sub-changes and assessments already show.

### 7bis. A prototype during the framing: the same sub-change, checking the need

A prototype answers an earlier question than a POC: not *does it work?* but *is this what we need, how could it
work?* (a mock-up shown to the requester, a flow tried end to end, an API explored). The mechanism is the same: one
methodology, `poc`, one kind of action, one way back. Only what it checks and where its verdict goes differ.

**One generic subject: the hypothesis.** The POC no longer checks "risks" directly: it checks **hypotheses**, an item
type the built-in methodology domain declares (`hypothesis`: `key` `HYP-n`, `statement`, `source`, `status`; ADR
0128). Opening a POC writes one hypothesis per thing to check in the sub-change: for a risk, `"RSK-3 holds: the PSP SDK
cannot refund partially"` with `source: RSK-3`; for the framing, a question about the need turned into a statement to
confirm or refute, `"the requester can validate a voucher in the checkout without leaving the page"`, `source:
framing`. The conditions of §6bis in the sub-change take scope `item` restricted to `self.type == "hypothesis"` instead
of `risk`; the probes, the relaxed build, the verdicts (assessments of `item:HYP-n`) and `poc.concluded` are unchanged.

**The action carries its purpose.** The POC action gains `purpose`: `derisk` (§1, `risks` set) or `explore`
(`hypotheses`: the statements, no risk). An `explore` action is in the register of the change like any action (it is
work the change commits to) but covers no risk.

**Where it sits in `sdlc`.** The framing becomes:

```yaml
- name: framing
  steps:
    - name: review                 # automatic review of the description (ADR 0130), unchanged
    - name: prototype              # optional: reactive, nothing to do while no prototype is proposed
      on: [{event: change.item_added, filter: 'event.items.exists(i, i.type == "action" && i.data.kind == "poc")'},
           {event: poc.concluded, from: integrated}]
      actions: [open_poc, conclude_poc]
    - name: confirmation
      pre: {description_rated: true, prototypes_concluded: true}
```

| Condition (parent) | Expression (sketch) |
|---|---|
| `prototypes_concluded` | all `poc_settled` where `self.kind == "poc" && self.purpose == "explore"`, empty: true |
| `pocs_concluded` (build gate, §3) | all `poc_settled` where `self.purpose == "derisk"`, empty: true |

Who proposes it: the person framing the change (*Propose a prototype* on the framing, with the questions), or the
description review, whose `rate_description` may answer, next to its findings, a `suggestion` of kind `prototype`
with the questions it could not settle from the text (the bubble shows *Propose a prototype*; a person turns it into
the action, never the model). Opening is the same human `open_poc`; at this stage no impact analysis exists, so its
form takes the **repositories** to start from (or none: an empty workspace for a mock-up) instead of impacted
components.

**The way back to the framing.** `conclude_poc` for an `explore` POC writes on the parent an artifact
`prototype_findings` (per hypothesis its verdict and findings, the branch, and a proposed wording of the intent built
from them) and marks the action done. It does **not** edit the description (ADR 0110 §4: only a person applies a
suggestion): the confirmation step shows the findings and the proposed wording, the person amends the intent if they
take it, and the edit wakes the automatic review, which rates the new description by itself (ADR 0130). The analysis
then starts from a need the prototype made concrete. A refuted hypothesis is as useful as a confirmed one: it is a
scope the change leaves out, which the person writes into the intent.

The delivery run waits at the confirmation while a prototype is open, exactly as it waits at the build for a POC
(§6bis): `prototypes_concluded` is produced only by the reaction to `poc.concluded`.

### 8. Methodologies

- `poc` 0.1.0 (new), `sdlc` 0.10.0 (`integrates`, `derisking`, `build` waits for `pocs_concluded`, the prompt of
  `implement_component`), `risk-management` 1.2.0 (`plan_mitigation` may propose a POC).
- A test in `platform/e2e/engine` (`sdlc_poc_test.go`, on a local git repository like `sdlc_workspace_test.go`): a high
  risk, a POC action, opened, built, concluded refuted, the risk closed, the build of the parent released.

### As built (first cut)

Where the implementation differs from the sections above, it is what holds:

- **One step for both purposes.** `sdlc` 0.10.0 has one process step `proofs` before the build (sub-steps `open` and
  `conclusion`), for POCs and prototypes alike; `framing/confirmation` waits for `prototypes_concluded`, the build for
  `pocs_concluded`. Only `conclusion` is reactive (`on: poc.concluded, from: integrated`); `open` is planned by the
  delivery run, or launched from the starting points (goal `software_delivery/proofs/open`), so a proposed POC gives one
  human task, not one per run. `open` declares `done: {pocs_opened: true}`.
- **Hypotheses only.** The sub-change copies no risk: `open_poc` writes into it an artifact `poc_scope` (the
  hypotheses, a risk `RSK-n` becoming `"RSK-n holds: <title>"` with `source: RSK-n`, the repositories, the timebox, the
  notes), and the POC's first step `state_hypotheses` states them as `hypothesis` items (`HYP-n`). Its conditions are
  scoped `item` where `self.type == "hypothesis"`. The form takes repositories, never impacted components.
- **Criticality.** `open_poc` passes `criticality: C1` to `goap-change/create`, which writes it on the change's data;
  the create does not apply the methodology's default by itself.
- **A confirmed risk** is set back to `open` with a rationale naming the POC (probability unchanged); the done POC no
  longer covers it, so `risk-management` asks for another mitigation.
- **Timebox.** Carried in the intent and the scope as guidance; the loop budget of `implement_probe` is fixed
  (`maxRounds: 60`, `timeout: 30m`); no step deadline.
- **Platform.** `goap-change/create` takes `parent` (a sub-change takes its parent's namespace, project and unit),
  `criticality` and `data`; `goap-change/close` abandons a change with a reason (from a process: its own change or one
  of its sub-changes); `goap-change/action` takes `kind`, `purpose`, `risks`, `hypotheses`, `subChange`. The DSL gains
  `ctx.addItem(type, data)`. A human action with `code` may declare `mcps` (its code runs as the person submits), and a
  scoped human action's code sees its subject (`vars.item`). `contracts/risk`: `ActionItem.Kind` / `Purpose` / `Risks`
  / `Hypotheses` / `Change`, `Covers`, `IsPOC`, item type `hypothesis` (`risk.Hypotheses`); CEL `actions[].covers`;
  library condition `pocs_pending`; the brief shows the POC actions and the hypotheses.
- **Planning a process over scoped actions.** The steps of `poc` declare their `done`, since the pending conditions
  `chainScoped` adds to the effects would otherwise enter the goal of the process; `state_hypotheses` declares the
  pending conditions of the framing actions among its effects, the subjects it creates being what they work on.
- **The delivery learns from it.** `conclude_poc` writes an artifact `poc_findings` for every POC (its verdicts, the
  person's summary, the branches of its code; `prototype_findings` too for a prototype), which
  `implement_component`'s prompt shows as a reference, saying the component is built again under the rules of the
  delivery.
- **Web.** The Actions card of *Risks & actions* proposes a mitigation, a proof of concept (the live risks it checks)
  or a prototype (its statements); a POC row shows what it checks and links its sub-change once opened, a risk row
  says when a concluded POC covers it no more (`web/src/lib/risks.ts`: `covers`, `pocData`). The description rating
  may answer `prototype: [statements]`, the questions only trying would settle; the bubble lists them with *Propose a
  prototype*, which writes the `explore` action (a person, never the model; opened from the proofs step as any other).
- **Test**: `platform/e2e/engine/sdlc_poc_test.go` (`TestSDLCDerisksByAProofOfConcept`): a 4x4 risk and its POC
  action, opened by a person through the form, the sub-change running by itself (probe, confirmation, build in a local
  workspace, check, verdict refuted, conclusion), `poc.concluded` carried to the parent, the risk closed, the action
  done, the sub-change abandoned.

## Consequences

- Derisking by running code is part of the change: proposed on the register, done in a sub-change everyone can follow,
  concluded by a person, its verdict on the risk where the release gate reads it.
- The quality rules stay whole for what is delivered: what a POC waives is waived in a change that lands nothing, and
  its code reaches the delivery only as a reference.
- The pattern is the first use of a sub-change of another methodology opened by a methodology itself; the same shape
  serves other investigations (a spike, a benchmark, a migration rehearsal) with their own methodology.
- A POC costs a person twice (opening, concluding): deliberate, a POC spends a budget and moves a risk.

## Settled questions

1. **The build waits for the open POCs** (`pocs_concluded` in its `pre`), not only the release: what a POC checks
   decides what is built. A team that will not wait cancels the action, a decision recorded in the register.
2. **The delivery never starts from a POC's branch.** It reads the findings and the branch as a reference
   (`poc_findings`, §5); the code is built again under the rules of the delivery, so nothing that skipped them ships.
3. **Opening a POC takes** the statements to check on top of the risks, the sources, a timebox and notes. The budget
   is the loop budget of `implement_probe` (a methodology setting, not a field of the form); a target environment
   belongs to the sources or the notes. More fields come when a use asks for them.
