# Operation sheet: a minimal model for an augmented SDLC, mapped onto GOAP

Rewrite of `fiche_operation_minimale.md` (French, 1128 lines). Goal: keep what decides, controls or traces; map every
notion to a GOAP concept; drop what GOAP already has under another name; mark what is implemented, partial, or missing.

**Status legend**: ✅ implemented · 🟡 partial · ⬜ missing. "ADR n" refers to `docs/adr/`.

## 0. How to read this

The original mixes three layers. They are separated here.

| Layer | Content | Where it goes |
|---|---|---|
| **Model** | Action, effect, verification, derogation, gate, composite frame, capability, stale | Sections 2 to 7 |
| **Practice** | The list of axes, criticality table C1-C3, arbitration order, metrics | Section 8 (defaults an organisation may adopt, not the platform) |
| **Rationale** | ISA-88, SPEM, RAMI, APQP, Thompson, Simon, Galbraith, TOC | Section 9 (references only) |

## 1. Principles (10 in the original, 7 kept)

1. **The effect is first-class.** An action exists to produce a *verifiable* effect.
2. **A result produced is not a result accepted.** Verification is a distinct step, done by a verifier independent of the producer.
3. **Constraint ≠ method.** Rules say what is forbidden or required; a method says how. Change the method without changing the guard-rails.
4. **The mechanism is chosen late.** An action states a required capability; who or what executes it is resolved at run time.
5. **A change is the sum of contributions.** Each control process contributes its part and may reference the others' productions.
6. **Sizing follows processing capacity.** A contribution must fit within the reliable capacity of whoever executes it.
7. **The model stays minimal.** Every field must serve to decide, control or trace, otherwise it goes.

Dropped as redundant: "axes meet at explicit points" (follows from 5 and the gate/composite of section 5), "the nominal flow
is autonomous" (a property of the engine, not a principle), "four slots" (a reading aid, section 2).

## 2. The four slots are a reading aid, not a data model

The original splits an action into **flow** (consumed/produced), **resource** (what persists: mechanism, context),
**recipe** (steps) and **frame** (control and constraints). Useful to ask *who drives each part*, but GOAP already splits the same
things by *concern*, and a slot that pulled organisation rules into the action would break GOAP rule 1 (concepts stay independent).

| Slot | Original content | GOAP home | Owner concept |
|---|---|---|---|
| Flow | inputs consumed, outputs produced | Action `pre` / `effects`; items and impacts on the change blackboard | Methodology (declares) + Change (holds) |
| Resource, mechanism | human, agent, tool | Action `mcps`, `model`, `roles`; resolved through Adapter and model gateway | Organisation + Adapter (late binding, rule 5) |
| Resource, context | reference documents, history | Brief (`pkg/brief`), references of processes, facets | Change |
| Recipe | steps | Method / process (ADR 0034, 0035, 0050) | Methodology |
| Frame, postconditions and oracle | acceptance criteria | `Expects`, `verify` (section 3) | Methodology |
| Frame, rules, budget, forbidden | policies | `Policy` nodes (ABAC), adapter restrictions, quotas, criticality policy | **Organisation** |
| Frame, gates | blocking criteria | lifecycle gate, decision point (section 5) | Change + Methodology |

Consequence: **there is no "frame" object stored on the action.** The frame is *computed* for a change from the methodology
(what to verify) and the organisation (what is required at this criticality). See the composite, section 5.3.

## 3. Verification: produced ≠ accepted

### 3.1 Model

An action may declare how its effect is verified:

```yaml
verify:
  oracle: tool | human | model     # kind of verifier; names no person, no model
  independent: true                # verifier ≠ producer (default when verify is set)
```

Per action run, states are facts of the change: `produced → verified → accepted | accepted_with_reserve | rejected`.
Only a verifier different from the producer (principal, and model alias for a `model` oracle) can move *verified → accepted*.

Rejected results go back through the normal retry / escalation path (decision point, human task). `Held`, `Escalated` and
`Aborted` of the original state machine map to the existing process states `waiting`, `stuck` and `abandon` (ADR 0036 §3).

### 3.2 Mapping and status

| Notion | GOAP | Status |
|---|---|---|
| Postcondition | `Action.Expects` (checks a node was written, not a property of it) | 🟡 |
| Oracle kind, independence | `Action.Verify`, `Graph.ReviewPolicy`, `pkg/verify.Policy` | ✅ ADR 0075 |
| Verification states as facts | `verification` items, `verifications` in CEL, library `verification` | ✅ |
| Accepted with reserve | requires an open derogation (section 4) | ✅ |
| Verifier = the engine review operation only | a human review through the graph handler writes no verification fact | 🟡 |
| Independence *proof* (not only distinct identity) | principal and alias differ; correlated errors under two aliases of one base model not detected | ⬜ needs the capability profile (section 6) |

## 4. Derogations: accepting a gap, with an expiry

A derogation is an item of the change: `rule`, `target`, `reason`, `signatory`, **mandatory `expires`**. No expiry, no derogation.
Expiry sends the covered impacts back to `proposed` through the normal review path (never a clock inside the graph).
Compare the **waiver** (ADR 0036 §3), which only unblocks one run and stays.

| Notion | GOAP | Status |
|---|---|---|
| Derogation with signatory and expiry | `pkg/risk` kind `derogation`, `derogation:sign`, `derogation:sign-role` | ✅ |
| Debt threshold | `derogation_debt` (≥ 3, `condition.DerogationDebtLimit`) | ✅ (constant, not yet policy) |
| Expiry sweep | builtin `derogation.expire`, example trigger `methodologies/examples/derogation-expiry.yaml` (not seeded) | 🟡 not tested through the trigger manager |
| Signatory is a named person other than the writer | only the writer's authority is checked | 🟡 |

## 5. Gates and composites: where control processes meet

Two *n-ary* meeting points, dual of each other:

- **Gate** (feedback, detection): several control processes give verdicts, someone decides.
- **Composite** (feedforward, prevention): several control processes' rules are woven into one object before execution.

Principle: **prevent by construction what can be automated; decide at a gate what needs judgement or independence.**

### 5.1 Gate

Criteria split in **vetos** (one unmet blocks, never compensated) and **objectives** (evaluated; an unmet one passes only
under a derogation naming it). Outcomes: go, go with reserve, no-go, escalate.

| Notion | GOAP | Status |
|---|---|---|
| Gate on a state change | lifecycle `Transition` with guard (ADR 0058) | ✅ |
| Vetos and objectives | `Transition.Vetos` / `Objectives`, `condition.CheckGate`; reserve recorded as `unmet` / `reserve` on the transition item | ✅ |
| Escalate | decision point status `escalated`, `humanOnly` (ADR 0067) | ✅ |
| "Go with reserve" as a ruling of a decision point carrying a derogation key | recorded on the transition item only | 🟡 |
| Evidence dossier (generated pack of verdicts) | none; the change log and PROV-O hold the raw material | ⬜ |
| Decider independent of the producer | `humanOnly`, ABAC roles; no independence rule | 🟡 |
| Gate types (readiness, acceptance, derogation, replan, release, frame evolution) | **not new types**: all are lifecycle transitions or decision points; frame evolution is a Change (5.4) | ✅ by design |

### 5.2 Axes are transverse methodologies, not a new concept

The original lists 11 axes (production, methods, quality, security, compliance, cost, flow, capacity, context, configuration,
integration). Its own test: an axis exists only with **its own method, an owner and its own indicators**. In GOAP that is exactly a
**transverse methodology** (`appliesTo`, `on: [{event, filter}]`, ADR 0036 §3) with its roles and policies. No `Axe` node type.

A *contribution* is a step or item of the companion run attached to the change. The **integration contribution** (coherence of
the parts) is a condition required by the release gate, like `risks_under_control`.

| Notion | GOAP | Status |
|---|---|---|
| Control process running alongside a change | transverse methodology | ✅ one example (`risk-management`) |
| Quality / oracle axis, integration axis, flow-capacity axis | not written | ⬜ suggested first three |
| Security, cost, compliance | rules = organisation policies and quotas; a methodology only where the process itself needs steps | 🟡 |

### 5.3 Composite frame

A *compositor* applies the layers of several control processes to an action in priority order and must: trace the provenance
of every element, detect conflicts (and escalate rather than decide silently), deduplicate, and regenerate when a layer changes.
It also counts **active constraints**, which feeds the capacity check (section 6).

| Notion | GOAP | Status |
|---|---|---|
| Compiled brief per LLM action | `pkg/brief` (intent, step, risks, decisions, actions; no rules from other axes) | 🟡 |
| Layers, priority, provenance, conflicts | policy chain (ABAC along unit chain, nearest wins), adapter restrictions, method priority/specificity: mechanisms exist, nothing compiles them into one object | ⬜ |

### 5.4 Evolution of the frame is a Change

The original adds a "frame evolution" gate and a four-loop model. In GOAP rule 2 settles it: **modifying a methodology, a policy or a
qualification is a Change** (graph data, versioned, reviewed, journaled), through the existing lifecycle and gates. No special gate.
Rules of the original worth keeping as policy of that Change: evidence before modification (aggregated, not one failure),
exclusive owner per rule, a review date and a retirement path (rule accretion consumes the mechanism's capacity).

| Notion | GOAP | Status |
|---|---|---|
| Observer turns runs into proposed methodology changes | `methodology-improvement.yaml`, `pkg/observe`, `pkg/selfimprove` (ADR 0011) | ✅ |
| Aggregated-evidence threshold across runs | thresholds are per run | 🟡 |
| Rule owner, review date, retirement | none | ⬜ |
| Urgent frame change (andon) marking running work stale | none | ⬜ |

## 6. Capability, qualification, capacity

### 6.1 Capability (late allocation)

An action requires an *abstract capability*; a mechanism *provides* a measured profile. Splitting the two keeps concepts independent:

| Side | Content | GOAP home |
|---|---|---|
| Required | capability names, minimum autonomy | **Action / Method** (Methodology) |
| Provided | `provides`, measured profile (success rate, sample, validity domain, date), allowed autonomy level | **AdapterDef / model node** (the Adapter is where organisation, MCP and connector meet) |

Autonomy levels: L0 human alone, L1 agent proposes, L2 agent executes and human validates each output, L3 automatic validation with
sampling, L4 autonomous with after-the-fact audit. The allowed level depends on the **measured profile and the criticality** of the
effect (section 7). A new model version or prompt version **invalidates the qualification**.

| Notion | GOAP | Status |
|---|---|---|
| Schedulable only where MCPs resolve | rule 5, adapters, restrictions along the unit chain | ✅ |
| Model choice, quotas, roles per model | modelgw, `platform@LlmAlias` (ADR 0021) | ✅ |
| Capability profile, autonomy levels, requalification | none | ⬜ |
| Raw material for the profile | observer statistics, `model.call` log entries (ADR 0059) | 🟡 |

### 6.2 Capacity-based decomposition

Core idea: splitting exists because no mechanism holds all context and constraints reliably. Reliability is a **decreasing curve of
load**, measured by evals. Condition: `load(action) ≤ reliable_capacity(mechanism) × (1 − margin)`, with
`load = context + active constraints + inputs to resolve + step complexity`. Too coarse: overload. Too fine: coordination cost.
Pick the coarsest granularity within capacity; treat it as a parameter reviewed at each requalification, not an architectural constant.

Levers when the condition fails: reduce load (split), raise capacity (other mechanism, orchestrator), escalate.
Verification capacity and human attention are finite resources too; bound unverified output with a WIP limit.

| Notion | GOAP | Status |
|---|---|---|
| Load vs reliable capacity, active-constraint count | none | ⬜ |
| WIP limit on unverified output, human attention budget | none (`HumanTask` is the only human queue) | ⬜ |
| First measurement step | extend the observer (`FindLLMHeavy`): success vs constraints per model | ⬜ proposed |

## 7. Criticality, floors and objectives

Criticality (C1 low, C2 current, C3 critical) is **data of the change**; what each level *requires* is **organisation policy**.
Floors (non-negotiable) are never traded against objectives (negotiable).

| Notion | GOAP | Status |
|---|---|---|
| Level on the change | `Change.Data` `criticality` (graph never reads it); methodology default, requester may raise, lowering needs `change:lower-criticality` | ✅ |
| Policy per level (oracle kinds, sampling, signatory role, max derogation lifetime) | `organisation@CriticalityPolicy`, resolved along the unit chain, whole policy nearest wins; compiled-in `Defaults()` | ✅ |
| Readable in conditions and guards | CEL `change.criticality`, `criticalityPolicy` (not `policy`: taken by decision points) | ✅ |
| Enforcement | `Graph.ItemPolicy`: oracle kind accepted for the level, derogation lifetime ≤ max | ✅ |
| Sampling | policy data only, not enforced | ⬜ |
| Split deployments (engine/registry over RPC) | see the compiled-in table, not the organisation's | 🟡 |

## 8. Practice defaults an organisation may adopt (not the platform)

These are *content*, written as methodologies and policies, not code.

**Axes to start with**: Quality (oracle, acceptance), Flow-capacity, Configuration (versions), Integration; then Security, Cost,
Methods. Rule: one owner, one method, own indicators; otherwise it is a role of another axis.

**Arbitration by criticality** (order of precedence when objectives conflict):

| Criticality | Order | Latitude |
|---|---|---|
| C3 | Security = Compliance > Quality > Cost > Flow | no derogation without a human signature |
| C2 | Security = Compliance > Quality ≈ Flow > Cost | sampled verification if the verification bottleneck exceeds a threshold |
| C1 | Flow > Cost > Quality | automatic oracle alone; accepted with reserve allowed |

**Metrics** (watch for Goodhart): rework rate per method, rejection rate at verification, cost per accepted effect, verification
delay, derogation count and age, stale rate, load/capacity ratio per mechanism.

## 9. Version pinning and stale

An action starts with pinned versions of the frame (methodology, policy), recipe and resource (adapter, model). A change of any of
them marks dependent work **stale** and sends it to revalidation. A change is complete when: required contributions are accepted
(or accepted with reserve), pinned versions are still current, no constraint is violated, no derogation is expired, and the
**integration contribution** is accepted (parts correct does not mean whole correct: systems are only quasi-decomposable).

| Notion | GOAP | Status |
|---|---|---|
| Versions exist | methodology versions (`MV:`), node versions, baselines as states of changes (ADR 0056) | ✅ |
| Pin at start: record methodology, policy, adapter/model version on the process | none | ⬜ |
| Stale on version change | only flow-level stale after a relaunch (ADR 0025) | 🟡 |
| Proposed design | pin = baseline id + versions recorded on the process; stale = comparison at the gate; **no new versioning** | ⬜ next ADR |

## 10. Traceability

Record per action run: mechanism, model version, method, inputs@version, outputs@version, verdict, cost.

| Notion | GOAP | Status |
|---|---|---|
| One log per change | `change_log` (ADR 0030), journal (ADR 0011) | ✅ |
| Prompts and answers | `model.call` entries, `prompt:inspect` (ADR 0059) | ✅ |
| Provenance export | PROV-O (ADR 0057), including verification and derogation entries | ✅ |
| Trace of an item | `goap-change/trace` | ✅ |
| Method and prompt version pinned per record, verdict per run | verdict via `verification` items; versions not pinned | 🟡 |

## 11. Variability

The original wants `contributes / extends / replaces` to adapt a sheet per project without duplication.
GOAP has `Action.Specializes`, method priority and `when`, and projects naming applicable methodologies (inherited by
sub-projects, ADR 0039). Missing: cross-methodology `contributes` / `extends` / `replaces`. 🟡. Low priority until a second
methodology needs it.

## 12. Concept placement (who, why, how, what, with what)

| Concern | Concept | Holds |
|---|---|---|
| WHO | Organisation | criticality policies, derogation signatories, review and signing permissions, quotas, the provided capability profile (on the adapter instance) |
| WHY | Change | criticality, verification and derogation facts, pinned versions, the contributions of control processes |
| HOW | Methodology | `verify`, required capability, vetos and objectives of a gate, transverse control processes, the `verification` and `derogations` condition libraries |
| WHAT | Domain | nothing of this model: domains stay unaware |
| WITH WHAT | MCP, Connector, Adapter, model | late binding; measured capability and autonomy; the model alias that a `model` oracle must not share with the producer |

## 13. What is implemented vs what is next

**Done (ADR 0075)**: independent verification, derogations with expiry, criticality and its policy, gate vetos/objectives, web panes.

**Next, by value and cost**

1. **Version pinning and stale** (section 9): record versions at process start, compare at gates. Reuses ADR 0056 and 0025.
2. **Axes as transverse methodologies** (5.2): write Quality/oracle and Integration; the integration contribution as a release condition.
3. **Capability profile and qualification** (6.1): requires/provides split, autonomy per adapter instance, requalify on alias target change.
4. **Composite frame** (5.3): compile policies and axis rules with provenance and conflict detection; conflict opens a decision point.
5. **Capacity and WIP** (6.2): measure first with the observer, then limit.
6. **Metrics, rule ownership and retirement** (5.4, 8).

## 14. Open points kept from the original

1. Reliable-capacity protocol: which evals, which load levels, which target success rate per criticality.
2. Proof of independence of an oracle: another model, another method, a deterministic tool.
3. Language of composition rules: structured text, CEL, policy-as-code. GOAP already uses CEL for conditions and guards: default to it.
4. Who is the compositor and the integrator: orchestrator, integration axis, or a deterministic tool.
5. Requalification frequency and automatic downgrade threshold of autonomy.
6. Number of piloting levels: two plus human governance is the working hypothesis.

## 15. References

ISA-88 (recipe/instance, late allocation, state machine), SPEM 2.0 (method content vs process, variability), RAMI 4.0 / AAS
(capability → skill), HTN (several methods per effect), IDEF0/ICOM (output feeds any slot), ISA-95 (production, quality,
maintenance, inventory), APQP/PFMEA (control plan, detection vs prevention), ISO 9001 §4.4, Lean (jidoka, andon), Theory of
Constraints, Thompson (interdependence), Simon (bounded rationality, near-decomposability), Galbraith (reduce need or raise
capacity of information processing), Deming (tampering), Argyris (double loop), Beer (viable system).
