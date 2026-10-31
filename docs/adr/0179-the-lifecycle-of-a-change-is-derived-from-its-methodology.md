# ADR 0179 — The lifecycle of a change is derived from its methodology: a maturity, and gates moving along it

**Status**: accepted, first step built · **Date**: 2026-10 · **Builds on**: ADR 0058 (change lifecycle and gates), ADR 0078
(committable states), ADR 0106 (guardians), ADR 0132 (process gates), ADR 0146 (the guardian judges commits), ADR 0168
(the guardian is an RPC call)

## Context

The guardian of a change (ADR 0146, 0168) authorises its moves and its commits from the lifecycle the change follows.
Until now that lifecycle was the one of a **domain** the methodology names (`lifecycle: maturity_of_change`, of `alm`),
and the methodology merged the criteria of its gates into it (`registrysvc.WithGates`, ADR 0132 §2).

For `sdlc` the two say the same thing twice. The states of `maturity_of_change` are the stretches of the delivery process
between two gates; its forward transitions are the gates; its backward ones the `back` of their decisions. Every new gate
touched two artefacts versioned apart, and a domain described the shape of a methodology, against rule 1 of the mental
model (a domain knows no methodology).

Two remarks settle the design:

- **The imposition belongs to the methodology.** Nothing about how a change matures is the domain's.
- **The guard of a transition is the entry of its gate.** A gate is an activity with a formal record of its result (a
  decision point and its ruling); its checklist and its entry say when it may be passed, its outcome says where the
  change goes.

## Decision

### 1. A methodology states its maturity

```yaml
maturity:
  description: How a delivery change matures
  phases:
    - {name: framing}
    - {name: analysing}
    - {name: delivered, final: true}
    - {name: abandoned, final: true, exit: abandon}
```

`Methodology.Maturity` (`engine/methodology/maturity.go`; proto `Methodology.maturity`, messages `Maturity` /
`MaturityPhase`; attribute `maturity` of `methodology@MethodologyVersion`, meta-domain 1.19.0): the phases in order, the
first the initial one; `final` (merge and close only there), `notCommittable` (the floor of the commit guardian, ADR
0078), `exit` (a transition into a final phase from every phase not final, taken with no gate: abandon). A methodology
names a lifecycle of a domain **or** states a maturity, not both (`checkMaturity`).

### 2. A gate moves the change between two phases

`gate: {enters, from, decision: {backTo, by, code}}` (`methodology.Gate`, proto `StepGate.enters` / `from`,
`GateDecision.back_to` / `by` / `code`):

- `enters` names the phase the go enters (required with a maturity); `from` the phase it leaves (default: the phase
  before `enters` in the list).
- `transition` only names the derived transition (default: the step's name); `decision.backTo` names the phase a back
  sends the change to, `decision.back` the name of that transition (default `back_<step>`).
- The **guard** of the transition is the entry of the gate (its `pre`, `GateInfo.Entry`, criteria `entry:<condition>`),
  its checklist (vetos and objectives, as before) and its decision (a decided go): `GateInfo.Criteria`.

### 3. The lifecycle is derived

`Compiled.Lifecycle()` (`buildLifecycle`): states = the phases; transitions = per gate its go (`from → enters`) and its
back (`from → backTo`), per `exit` one transition from every phase not final. Its name is the methodology's name, stable
across versions, so `UpgradeChange` keeps a change's state. `registrysvc.Service.Lifecycle` serves it with the criteria
and freezes of the gates (`WithGates`, unchanged), the guardian service answers the graph with it.

**The graph is unchanged**: it reads a `domain.Lifecycle` through `ChangeLifecycles.Lifecycle` as before and stores its
name and state on the change; it never knew where it came from.

### 4. A gate is human by default; a script may decide it

`decision.by: human` (default: the board, people in their seats) or `script`: the compiler generates `<path>:rule` as a
script action running `decision.code` (JavaScript, `ctx.param("gate")` names the gate, `ctx.decide(point, option,
confidence, justification)` rules the open point); the point opens with the policy of an agent (`decider: agent`, no
board), so the platform's decision policy (threshold, ratification) applies. A script has no board, no rule, and is
not combined with a decision method. Whether a gate may be decided so is a matter of setting, the engine stays generic.

### 5. Two representations

- **Level 1, the lifecycle**: the phases and the gates between them. The registry's `GetMethodologyView` answers it
  (`MethodologyView.lifecycle`, with the criteria of the gates); the change tab reads it there (`changeLifecycle` in
  `web/src/lib/changeTransition.ts`, before the domains), the methodology tab draws it under the *Maturity* field
  (`LifecyclePreview`).
- **The detail, the flow**: the steps, methods and runs of a phase (`ProcessFlowView`, `ChangeState.svelte`), unchanged.

## Consequences

- `sdlc` 0.12.0 states its maturity (same phases and transition names as `maturity_of_change`); `alm`'s
  `maturity_of_change` is no longer followed by a shipped methodology and can be deprecated with the next `alm` version.
- A methodology with a domain lifecycle keeps working as before (`release-based` and its `delivery`).
- Tests: `engine/methodology/maturity_test.go`, `platform/e2e/engine/maturity_test.go` (a gate ruled by a person, then
  one by a script), the sdlc e2e tests on the derived lifecycle.

## Next steps

- Outcomes beyond go / no_go / back (`outcomes: {name: {enters}}`), each naming its phase: the lifecycle as the graph of
  the outcomes of the gates.
- The checklist read as annotations of the entry (`veto`, `role`, `attest` on the conditions of `pre`).
- Deprecate `maturity_of_change`; a form editor of the maturity instead of JSON.
