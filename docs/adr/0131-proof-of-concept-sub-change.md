# ADR 0131 — Proof of concept: a risk mitigated by a relaxed build in a sub-change

**Status**: proposed · **Date**: 2026-10 · Builds on ADR 0036 (risks and actions, transverse risk management), 0075
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

The `risks` library gains two conditions:

- `pocs_pending`: an open action of kind `poc` (proposed or running);
- `pocs_concluded`: `!pocs_pending` (true when the change has none: the POC is optional).

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

### 7. Platform changes

Small, and generic:

1. `goap-change/create` takes `parent` (the RPC has `parent_id` already) and `data` (the free-form `Change.Data`).
2. `goap-change/close`: abandon an open change with a reason (`change:abandon`, its own sub-changes only for an agent).
3. `contracts/risk`: `kind`, `risks`, `change` on actions; `pocs_pending` / `pocs_concluded` in the `risks` library;
   the brief; `goap-change/risk`'s twin `goap-change/action` (an action is written today only as an item).
4. The criticality of a sub-change: today the default of the methodology is written only when the engine creates the
   change itself (`engine/engine/engine.go`); `goap-change/create` applies it too. A sub-change of another methodology
   starts from **its own methodology's** default (`poc`: C1), not its parent's level, without asking
   `change:lower-criticality`: it lands nothing.
5. Web: *Propose a POC* on the Risks & actions pane (a selection of risks), the POC action row linking its sub-change
   and its verdicts; the rest is what sub-changes and assessments already show.

### 8. Methodologies

- `poc` 0.1.0 (new), `sdlc` 0.10.0 (`integrates`, `derisking`, `build` waits for `pocs_concluded`, the prompt of
  `implement_component`), `risk-management` 1.2.0 (`plan_mitigation` may propose a POC).
- A test in `platform/e2e/engine` (`sdlc_poc_test.go`, on a local git repository like `sdlc_workspace_test.go`): a high
  risk, a POC action, opened, built, concluded refuted, the risk closed, the build of the parent released.

## Consequences

- Derisking by running code is part of the change: proposed on the register, done in a sub-change everyone can follow,
  concluded by a person, its verdict on the risk where the release gate reads it.
- The quality rules stay whole for what is delivered: what a POC waives is waived in a change that lands nothing, and
  its code reaches the delivery only as a reference.
- The pattern is the first use of a sub-change of another methodology opened by a methodology itself; the same shape
  serves other investigations (a spike, a benchmark, a migration rehearsal) with their own methodology.
- A POC costs a person twice (opening, concluding): deliberate, a POC spends a budget and moves a risk.

## Open questions

1. Does the build wait for the open POCs (proposed: yes, `pocs_concluded`), or only the release, through
   `risks_under_control`?
2. May the delivery start from the POC's branch (rebuilt under full rules) rather than only read it?
3. What else a person gives when opening a POC (budget in tokens, a target environment, the people consulted)?
