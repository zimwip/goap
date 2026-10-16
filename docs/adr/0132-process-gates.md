# ADR 0132 — Process gates: milestones of a change, a checklist, a go decision, a move of its lifecycle

**Status**: accepted, built (see *As built*) · **Date**: 2026-10 · Builds on ADR 0009 §4 (decision points), 0034 (processes and steps), 0035 /
0050 (methods, roles), 0058 (change lifecycle and gates), 0075 (vetos, objectives, derogations, criticality), 0097
(starting points), 0106 (guardians), 0109 (conditions derived from the history), 0117 (what a change waits for from
people), 0130 (reviewing steps), 0131 (proofs of concept)

## Context

### The layers of a methodology

A methodology is read in three layers:

| Layer | Element | What it says | Acted on? |
|---|---|---|---|
| Abstract | **process**, **process step** | the overview of the work, its logical subdivision, *who* is expected (RACI roles) | no: it is organisation |
| Concrete | **method**, **method step** | *how* the activity is performed in a context, with tools; specialised by `when` / priority (ADR 0050 §3) | through its actions |
| Unitary | **action** | one thing, not splittable: pre, effects, cost, kind (`llm`, `tool`, `human`, `builtin`, `script`) | yes |

The code follows this mostly: a process compiles to an agent and a goal, a step naming a capability (`method:`) runs the
applicable method as a fresh sub-agent acting as its responsible role (ADR 0050 §2), methods own their actions. Three
places blur it, and the third matters here:

1. A process step may name an `action:` directly (`sdlc`: `analysis/specification/*`, `application`, the steps of
   `release_train`), so the abstract layer also executes.
2. A step that names nothing (`MethodManual`, `engine/methodology/process.go`) compiles to a generated **human action**:
   the engine waits for a person to "do the step". `sdlc`'s `framing/confirmation` is such a step: it exists only so that a
   person says "go" before the analysis.
3. Nothing in the methodology says **when the change is mature enough to go on**. ADR 0058 gave changes a lifecycle and
   made a transition a gate (a decision plus the expected world state), but:
   - the criteria of the gate are written on the **domain's** transition (`guard`, `vetos`, `objectives` of
     `domain.Transition`), naming conditions of a methodology (`world["analysed"]`): the domain then knows a methodology,
     against rule 1 of the mental model;
   - the gate is not visible in the process: a reader of `software_delivery` sees steps, never the milestone between
     the analysis and the design;
   - nobody opens the decision: a person must open a decision point, rule it, then move the change by hand
     (*Move* on the change tab); the engine has no builtin for the move (ADR 0058, "Out of the principle");
   - what is frozen is fixed: the impacts drafted in the state being left (`Graph.frozen`); the description, a
     baseline of requirements, are never frozen;
   - `sdlc` has no lifecycle at all, so none of it is used.

Human in the loop is then obtained the wrong way: by a manual step the engine waits on (point 2), or by an approval the
engine asks when the initiator lacks the roles of an action (`awaitApproval`, `action:run`), which is an authorization,
not a checkpoint of maturity.

## Decision

A **gate** is a milestone of a process: a **step** that declares a checklist on the change, **split among the roles**
that answer for it, a **go decision**, what the go **freezes**, and the **transition** of the change's lifecycle the go
takes. The gate itself is abstract (organisation: what must hold, who answers for what, who decides). **How** the
decision is reached is a **method**, like any other activity: by default one person of the accountable role rules; a
methodology may name a decision method instead (a collegial board, a vote, a light review for a low criticality),
specialised by context and run by the engine as any method is. The engine opens the decision and carries out the move;
people take it.

### 1. A step declares a gate: `gate: {transition, checklist, decision, freezes}`

```yaml
lifecycle: maturity_of_change            # of the alm domain (§6)
processes:
  - name: software_delivery
    steps:
      - name: framing
        steps:
          - {name: review, method: description_rating, review: {subject: description, automatic: true}}
      - name: framing_gate                 # replaces the manual step framing/confirmation
        roles: {accountable: product_owner, consulted: [business_analyst]}
        gate:
          transition: analyse              # framing -> analysing
          checklist:
            - {name: description_accepted, veto: true}
            - {name: risks_identified, description: "the first risks of the change are recorded"}
          freezes: {fields: [title, intent]}
      - name: analysis
        pre: {"gate:software_delivery/framing_gate": true}
        steps: [...]
      - name: analysis_gate
        roles: {accountable: tech_lead, consulted: [solution_architect, product_owner]}
        method: gate_decision                # the decision workflow, a capability (§3bis); none: one ruling
        gate:
          transition: design
          checklist:
            - {name: impact_analysis_validated, role: solution_architect, veto: true}
            - {name: requirements_specified, role: business_analyst, veto: true}
            - {name: requirements_traced, role: business_analyst}
            - {name: pocs_concluded, role: tech_lead}           # ADR 0131: a POC still running goes with reserve
            - {name: risks_under_control, role: tech_lead, when: 'change.criticality in ["high", "critical"]', veto: true}
            - {name: architecture_compliant, role: solution_architect, attest: true}   # a person attests it
          decision:
            board: [tech_lead, solution_architect, product_owner]   # collegial: who sits on the decision
            rule: consent                                           # no member objects; unanimity | majority | consent
          freezes: {types: [alm@Requirement, alm@Function]}
      - name: design
        pre: {"gate:software_delivery/analysis_gate": true}
        method: design
```

`Step.Gate` (`methodology.Gate`, `engine/methodology/gate.go`; proto `Step.gate` / `StepGate`; attribute `gate` of
`methodology@Step`; also on `ViewStep`, `GraphStep`, `LevelNode`):

- **`transition`**: a transition of the change's lifecycle (`Methodology.Lifecycle`). The compiler refuses a gate when
  the methodology has no lifecycle, an unknown transition, two gates on one transition, and a gate inside a method
  (`methods[].steps[].gate`): a gate is organisation, it belongs to the abstract layer.
- **`checklist`**: items, each a **condition** of the methodology (declared, of an imported library, or generated:
  `step:<path>`, `state:<name>`), with an optional `description` (else the condition's), `veto: true` (one unmet blocks,
  never compensated) or not (an **objective**: unmet, it lets the gate go only *with reserve*, covered by a derogation in
  force whose rule is the item's name, ADR 0075 §3), and an optional `when` (CEL over the change: the item applies only
  then, e.g. by criticality, ADR 0075 §4). An item is never an inline CEL expression: a criterion worth checking is worth
  naming as a condition, which the planner, the current state and the brief then read the same way (ADR 0109).
  - **`role`**: the role that **answers for the item** (a role of the methodology; default the gate's accountable role).
    The checklist is so split among responsibilities: each role sees its own items in *Waiting for you* (ADR 0117), the
    brief and the current state group them by role, and a derogation of an objective is signed by its role
    (`derogation:sign-role`, ADR 0075).
  - **`attest: true`**: the item is not derived from the work but **attested by a person** of its role (what no condition
    can see: "the architecture complies with the standards"). The compiler generates the condition `attested:<path>/<item>`
    (an `attestation` item of the change, `data.key` = `<path>/<item>`, `verdict` `met` / `not_met`, `comment`, `by`;
    a new item type of the `methodology` meta-domain, ADR 0128) and a generated **human action**
    `attest:<path>/<item>` (`roles: [<role>]`, a form with the verdict and a comment, ADR 0115) that establishes it. An
    attestation is a person's (ADR 0129: an llm or a script may not write one), and stands while what it was given on
    has not moved (its `rated` fingerprint of the checklist's other items, as an assessment, ADR 0110 §6): a rework of
    the analysis after the attestation makes it stale.
- **`decision`** (optional): `options` (default `[go, no_go]`; `back: <transition>` adds an option that takes that
  transition instead, e.g. back to framing), `question` (default "Go for <step label>?"), and for a **collegial**
  decision `board` (the roles that sit on it; default the accountable role alone) and `rule` (`unanimity`, `majority`,
  `consent`: no member objects; default `unanimity` with a board, a single ruling without). The step's **accountable**
  role (`roles.accountable`), required by the compiler on a gate step, sits on every board and is the one who ratifies
  when the rule is not met in time (§3bis).
- **`freezes`** (§4).
- The step's **`method:`** (optional) names the **decision method** (a capability, §3bis); without it the default
  decision applies (the engine opens the point, the board rules it).

The gate's **exit** is the generated condition `gate:<path>`: the change took the gate's transition and has not gone back
to its `from` state since (a fold of the `transition` facts, ADR 0109: `history.gates["<path>"]`). The steps after the
milestone name it in their `pre`, as today they name `step:<path>`. The gate's **entry** is its `pre` like any step;
by default the generated `state:<from>` of the transition.

### 2. The checklist is the methodology's, the state machine the domain's

The compiler turns each gate into the criteria of its transition at the methodology level: the guardian of the change
(ADR 0106, `Guardian.Transitions`) gains, per transition, the vetos and objectives of its gate (`methodology.GateCriteria`;
`Guardian.Transitions` becomes `map[string]TransitionGuard{Guard, Vetos, Objectives}`, a plain string staying accepted as
`Guard`). `ChangeLifecycles.CheckGuardians` evaluates them with `condition.CheckGate` against the blackboard and the
methodology's conditions, with derogations and reserves as for a domain transition (the reserve recorded on the
`transition` fact). An integrating methodology adds its own gates' criteria along the chain, innermost first.

The domain keeps the states, the transitions, their permission, and the guards that are about the **change as data**
(`decisionPoints`, `change.*`). Its `vetos` / `objectives` stay for what holds whatever the methodology; the ones naming
`world[...]` should move to the methodology (a compile warning on a domain transition whose criteria read `world`).

### 3. The decision is a person's; the engine prepares it and carries out the move

- **Opening.** When the gate's entry holds and every applicable **veto** holds, the engine opens the gate's decision
  point (`Graph.OpenDecision`, question and options of the gate, `Policy` reserved to a person, `gate: <path>` and the
  transition stored on the point; one open point per gate). The checklist, with each item met / unmet / reserve and the
  derogations that cover them, is the point's context. The gate step is a reactive step (ADR 0110): it is re-evaluated
  on `change.updated`, `step.completed` and `change.item_added`, so the point opens as soon as the checklist allows, and
  a point opened while the checklist then breaks again (a veto turned false) is marked stale, not ruled.
- **Asking.** The point is a decision only a person settles, so it is listed by `Engine.ChangeAsks` (ADR 0117) to the
  holders of the roles of the board who have not ruled yet, with the checklist grouped by role; the other roles see it
  in *Waiting for others*.
- **Collegial ruling.** Each member of the board rules the point (`Graph.RuleDecision`, `by` and the role it sat as);
  the decision policy folds the rulings by the gate's `rule` (`contracts/decision`: policy keys `board`, `rule`, stored on
  the point; `Fold` settles it when the rule holds: every role of the board ruled `go` for `unanimity`, a majority of
  them for `majority`, no `no_go` once the deadline `maxDuration` passes for `consent`), a `no_go` of a member under
  `unanimity` settles it `no_go` at once. When the rule cannot settle it (a split vote, the deadline passed under
  `unanimity`), the point is **escalated** to the accountable role, whose ruling settles it (`Reserve`, the existing
  escalation). The rulings stay on the point with their justifications: the record of the board.
- **Ruling.** `go`: the engine takes the transition as the person who ruled (`Principal.For`, ADR 0114) with the point as
  `change.decision` (a builtin `change.transition`, the one ADR 0058 left for later, `contracts/builtins`); the guard
  and the gate criteria are checked again by `TransitionChange`, so a checklist that broke in between refuses the move
  and the point is reopened. `no_go`: nothing moves; the point is closed with its justification, and a new point opens
  when the checklist next changes. `back`: the engine takes the back transition (ADR 0058 rule 4: later phases reopened).
- **No other waiting.** Human in the loop is ensured by **human actions** (a method's `human` action, a form, ADR 0115)
  and by **gate decisions**; the engine never waits for a person to accept a process step. A gate replaces the manual
  "confirmation" steps; a manual process step (a step with no method, action or sub-steps) becomes a compile **warning**
  ("a process step does nothing by itself: name a method, or make it a gate"), its generated human action kept for the
  methodologies already published.

### 3bis. The decision workflow is a method

A gate step may name a method (`method: <capability>`), as a reviewing step does (ADR 0130): the method is the
**workflow of the decision**, run by the engine like any method step (a fresh sub-agent acting as its responsible role,
the most specific method chosen by `when` / priority, ADR 0050 §3; `foreach` over the board's roles to run the members'
parts in parallel). Its exit is the generated condition `gate_decided:<path>` (the gate's point is settled), so its
planner works towards the ruling; the engine still opens the point (§3) and carries out the move.

```yaml
methods:
  - name: decision_board                 # high criticality: each member examines, then the board rules
    for: gate_decision
    when: 'change.criticality in ["high", "critical"]'
    roles: {responsible: tech_lead}
    steps:
      - name: examine                    # each member reads the checklist items of its role, comments, attests
        foreach: 'gate.board'
        action: examine_gate_items       # human, roles from vars.item, a form: comments on its items, attestations
      - name: advise                     # an llm reads the change and the checklist and advises (an assessment of
        action: advise_gate              # subject gate:<path>, never a ruling: ADR 0129, 0130)
      - name: rule                       # each member rules through a decision field (form widget decision, ADR 0115)
        foreach: 'gate.board'
        action: rule_gate
        pre: {"step:decision_board/examine": true}
  - name: single_go                      # otherwise: the accountable rules, nothing more
    for: gate_decision
    actions: [rule_gate]
```

The variables of the run carry the gate (`vars.gate`: path, transition, board, rule, the point, the checklist evaluated
with each item's role), so one decision method serves every gate of the methodology, and a gate specialises it only by
its `board`, `rule` and checklist. A human action of the method rules the point through the `decision` form widget
(`form.Rulings`): the people of the board decide, the method only lays out who examines what and in which order. A
decision method that writes impacts is refused by the compiler (a decision decides; the work is done by the phases).

Without a method, the default is the method `gate_decision.single` built in: every role of the board rules once through
its ask; nothing else runs.

### 4. What a go freezes

Leaving a state already freezes the impacts drafted in it (ADR 0058 rule 4). A gate may freeze more, for every later
state, until the change goes back through the gate's `from` state:

- `fields: [title, intent, description]`: `UpdateChange` refuses to edit them (`ErrConflict`, "frozen by the gate
  <label>"); a comment and an assessment stay possible.
- `types: [<namespace>@<NodeType>]`: no checkout, creation or edit of a node of these types (or a subtype) in the change
  (`ImpactNodeCreate` / `Checkout` / `Update` / link edits); a transition of such a node stays possible only if the gate
  declares it (`transitions: true`, for an *approve* after the freeze).
- `phase: false`: the impacts of the state left are **not** frozen (a gate that marks maturity without freezing).

The freeze is stored on the `transition` fact (`data.freezes`), so the graph enforces it from its own log and names no
methodology (`Graph.frozen` widened; the graph keeps the mechanism, ADR 0067). Going back unfreezes what that gate froze.

### 5. In the views

- The view and the flow draw a gate as a milestone (`ViewStep.Gate`, a diamond in `ProcessFlowView`), between the
  phases it separates; the current state of the change (`GetChangeState`) gives each gate its checklist evaluated
  (`GateState{items: [{name, role, met, veto, attest, reserve, description, producedBy}], decision: {point, board,
  rule, rulings}, transition}`) next to the steps'
  `Needs` (ADR 0112), and the change tab shows the checklist under the lifecycle diagram (`ChangeTransitions.svelte`:
  the transition named by a gate shows the gate's checklist instead of the raw guard).
- Starting points (ADR 0097): a gate is never proposed as a step to start; an unmet gate is *what blocks* the steps
  after it, with its unmet items.
- The editor: a *Gate* section in `StepEditor.svelte` (transition picker from the lifecycle, checklist rows with the
  condition picker, veto / when, freezes), a *gate* badge on `StepNode.svelte`.
- The brief (`lib/brief`): the next gate and its unmet items, so an llm action knows what the change is being matured
  towards.

### 6. `sdlc` 0.10.0 and `alm`

`alm` gains a lifecycle of the changes of `sdlc`, `maturity_of_change`: `framing` (initial) → `analysing` → `designing`
→ `building` → `releasing` → `delivered` (final), `abandoned` (final), with back transitions `reframe`, `reanalyse`,
`redesign`. `sdlc` names it, places a gate between each phase, and its steps name the gate before them:

| Gate | Transition | Vetos | Objectives | Freezes |
|---|---|---|---|---|
| `framing_gate` (product owner) | `analyse` | `description_accepted` (ADR 0130) | `risks_identified` (new: a risk register was opened) | title, intent |
| `analysis_gate` (board: tech lead, solution architect, product owner; consent; method `gate_decision`) | `design` | `impact_analysis_validated`, `requirements_specified` | `requirements_traced`, `pocs_concluded` (ADR 0131), attested `architecture_compliant` | `alm@Requirement`, `alm@Function` |
| `design_gate` (tech lead) | `build` | `design_checked` | `tests_planned` | `alm@Application`, `alm@Interface` |
| `build_gate` (change approver) | `release` | `built`, `requirements_approved` | `risks_under_control` (veto when high criticality) | `alm@Component` |
| `delivery` | `deliver` | `rolled_out` | — | — |

`framing/confirmation` is removed: its "go" is the framing gate's decision. A POC sub-change (ADR 0131) is opened during
the analysis and its conclusion is read by the analysis gate; a POC still running lets the analysis gate go only with
reserve (a derogation of `pocs_concluded` signed by the tech lead). The release methodology (ADR 0106) keeps its own
lifecycle; its `merge` guardian may then read `integrated.state == "releasing"`.

## As built

- The gate of a step compiles as described, with these names: conditions `gate:<path>`, `gate_open:<path>`, `gate_go:<path>`
  (a decided go not consumed by a move yet, `change.consumed`), `attested:<path>/<item>`; actions `<path>:open` (builtin
  `gate.open`), `<path>:rule` (the default decision workflow: one human action for the board, run again seat by seat
  until the rule settles it) or `<path>:decide` (a call to the decision method the step names), `<path>` (builtin
  `gate.pass`), `attest:<path>/<item>`.
- An attestation is an artifact `attestation:<path>/<item>` (`verdict` met / not_met, `comment`), read by CEL
  `attestations`; no new item type.
- The choices of the point are plain labels (`OpenDecisionRequest.Choices`: go, no_go, back), selecting no flow.
- No entry by default on the `from` state of the transition: the methodology does not know its lifecycle at compile
  time, so a gate names the gate before it in its `pre` (`sdlc` does), and the graph refuses a move from another state.
- A no go opens a new decision at once (the board is asked again); a back takes the back transition with the point.
- `sdlc` 0.10.0: no `risks_identified` objective (a register to open would block every change without a derogation);
  the analysis gate is ruled by the default workflow (no `gate_decision` method yet); the build gate vetoes
  `build_complete` and `risks_under_control`, the delivery gate `rolled_out` and `requirements_approved`; every gate
  keeps the impacts of its phase editable (`keepPhase`), since the release approves the requirements of the analysis
  and the build edits the components it found.
- The change tab shows a gate through its steps (needs, asks of the board and the attestations); a checklist view of
  the gate on the change tab is still to come.

## Consequences

- A gate is one more declared property of a step, like a review (ADR 0130): no new node type, no new language, no
  checklist structure outside the conditions. What is new: the methodology-level gate criteria, the items' roles and
  attestations, the opening of the decision by the engine, the collegial rules of the decision policy, the builtin move,
  the declared freezes.
- The decision workflow is ordinary methodology (a method and human actions), so it is specialised by context, edited,
  versioned and observed (ADR 0011) like the rest; the platform only adds the collegial fold of a decision point.
- The domain stops naming a methodology's conditions; the methodology says what maturity means, the domain only what
  states exist.
- Human in the loop reads in the methodology: human actions do, gate decisions decide; the engine waits for nothing else.
- A methodology without a lifecycle, or without gates, is untouched.
- The decision is still a decision point: the policy (who rules, ratification, rounds), the audit (PROV-O), the reserve
  and the derogations are the existing ones.

## Not decided here

- **The abstract layer, strictly.** Whether a process step may still name an `action:` directly (point 1 of the
  context): a warning "a process step names how: put the action in a method" would complete the layering; `sdlc`'s
  specification and release-train steps would then become methods. Left to a separate decision, since it changes every
  methodology.
- Whether a member of a board may delegate its ruling (a deputy holding the role elsewhere), and a quorum distinct from
  the board.
- Notifications to the board's role holders when a point opens (ADR 0117's next steps).
- Freezing items (risks, artifacts) and a partial landing of frozen phases (ADR 0058, still out).

## Phasing

1. Model and compiler: `methodology.Gate` (items with `role` / `attest`, `decision` with `board` / `rule`), checks,
   `gate:<path>` / `gate_decided:<path>` / `attested:` conditions, the generated attest actions, item type
   `attestation`, gate criteria in the guardian, view / proto / attribute (meta-domain), warnings (manual step, domain
   criteria reading `world`).
2. Graph: freezes on the `transition` fact (`fields`, `types`), `Graph.frozen` widened, tests on both repositories.
3. Decision policy: `board` / `rule` folded by `contracts/decision`, escalation to the accountable role.
4. Engine: the gate step as a reactive step opening its decision, the decision method run with `vars.gate`, the builtin
   `change.transition`, `GateState` in `ChangeState`, the asks per role in `ChangeAsks`.
5. Web: milestone in the flow, gate checklist on the change tab, *Gate* section of the step editor.
6. `alm` lifecycle `maturity_of_change`, `sdlc` 0.10.0 with its gates, `platform/e2e/engine/sdlc_test.go` through the
   gates.
