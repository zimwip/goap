# ADR 0075 — Independent verification, derogations with expiry, criticality

**Status**: accepted, implemented · **Date**: 2026-10 ·
Builds on ADR 0024 (change impacts and their review), ADR 0029/0030 (impact events, one change log), ADR 0036 §3
(waivers, transverse methodologies), ADR 0058 (change lifecycle and gates), ADR 0065 (item kinds, `Change.Data`),
ADR 0066 (hooks, facets), ADR 0067 (decision policy kept out of the graph).

## Context

A design note (the "operation sheet": an action with four slots, axes as control processes, gates, composites)
asked what GOAP lacks to run an *augmented* SDLC, where agents produce most of the work and a result produced is not
a result accepted. Mapped onto the concepts of GOAP, most of it is already there (action, method, change, transverse
methodology, decision point, lifecycle gate, journal). Three things are missing and each is small, because it extends
a mechanism that exists:

1. **Nothing says who may verify.** A change impact has a `review` (`proposed` / `accepted` / `rejected`,
   `Review.By`) and knows its producer (`ChangeImpact.ProducedBy`), but nothing states that the verifier must differ
   from the producer, nor which kind of verifier an effect needs (a tool, a person, another model). An agent can
   accept what it wrote. `Action.Expects` only checks that a node was written, not a property of it.
2. **A waiver is unbounded.** `KindWaiver` (ADR 0036 §3) unblocks one run with a reason and a `by`, never expires,
   names no rule and is not counted. Nothing separates "accepted with a documented gap" from "accepted".
3. **No weight of the effect.** Every change is treated alike: the same checks for a prototype and a payment flow.
   Which gate needs a person, which review may be sampled, which waiver needs a signature, has no input.

Constraints from the project rules: a methodology knows no organisation (rule 1), so who signs and how critical a
kind of change is cannot live in a methodology; every modification is a Change (rule 2); the graph keeps mechanisms
and names no use-case vocabulary (ADR 0066, 0067; `pkg/layering`).

## Decision

### 1. Verification is declared on the action, enforced as a review rule

**Declaration (HOW, methodology).** An action may declare how its effect is verified:

```yaml
verify:
  oracle: tool | human | model     # what kind of verifier the effect needs
  independent: true                # the verifier is not the producer (default true when verify is set)
```

The methodology says only the *kind* and the independence; it names no person and no model. Who can verify is the
ABAC roles of the action (`roles`, ADR 0043) and, for `model`, an alias distinct from the producer's (ADR 0021).

**Enforcement (graph mechanism, engine policy).** The graph keeps the mechanism, as for decisions (ADR 0067): an
optional hook `Graph.ReviewPolicy` (a `domain.ReviewPolicy`, nil: anyone with `node:review` may review) is called
by `ReviewNodeOn` with a `domain.ReviewRequest` (the impact with its producer, execution and flow, the reviewer, the
verdict, the facts of the change) and may refuse. The platform plugs
the rule `verifier ≠ producer` (principal, and model alias when both are models) from `pkg/verify`, wired explicitly
by `cmd/graph` and `cmd/goap-dev`; `pkg/graph` and `pkg/domain` do not import it (`pkg/layering`).
A refusal is an error, not a state: nothing is written.

**States are facts, not a new storage.** Per effect of an action run, `produced → verified → accepted |
accepted_with_reserve | rejected` are items of kind `verification` (`pkg/verify`, `verify.Register()`), entries
`fact.verification` of the change log (ADR 0030): `Graph.AppendLog` refuses the `fact` stream, so they are written
through `AddItems` like the other item kinds, and `verify.Subjects` folds them. The engine writes a `produced` entry
when the action ends, carrying the oracle, `independent`, the model alias of the producer and the impact ids it
covers (`impacts`); `verify.Policy` reads it and refuses a reviewer equal to the producer (principal; alias when
both are models). `verified` / `accepted*` / `rejected` entries state the later steps. `accepted_with_reserve` is
only reachable with a derogation in force (§2; the entry carries its key). `Expects` stays the cheap check inside
`produced`; it is not the oracle.

Two more seams on the graph, all nil by default and plugged explicitly: `Graph.ItemAuthorizer` (asks a permission
before `AddItems` stores an item of a kind that has one: `derogation:sign`, and `derogation:sign-role` for the role
a criticality level asks of the signatory) and `Graph.ItemPolicy` (judges any item about to be stored; the
criticality policy of the oracle kinds). `Graph.ReopenImpacts` (RPC `ReopenChangeImpacts`) sends accepted impacts
of the main flow back to `proposed`; the expiry uses it.

CEL: the variable `verifications` (`{execution, action, impact, oracle, independent, state, producer, by, open}`)
and the conditions of the library `verification` (imported like `decisions` and `risks`, ADR 0064):
`all_verified`, `unverified_effects`, `reserves_open`. The DSL gets `ctx.reviewNodeWithReserve(node, derogation,
comment)` (`NodeOp.Reserve`).

### 2. A derogation is a waiver with a rule, a signatory and an expiry

`KindWaiver` stays as it is (unblock a run). A second kind, `derogation`, covers accepting with a gap:

| Field | Meaning |
|---|---|
| `rule` | what is waived (a postcondition, a gate criterion, a policy rule id) |
| `target` | the impact, action run or gate it applies to |
| `reason` | required, as for a waiver |
| `signatory` | the person answering for it (a principal, checked by ABAC `derogation:sign`, and `derogation:sign-role` when the criticality policy names a role; checked as the writer of the item) |
| `expires` | required; no expiry, no derogation (`validateDerogation` refuses it) |

- It is an item of the change, versioned by key like a risk (ADR 0036 §1); closing it adds a version.
- `risk.Derogations` folds the register; CEL `derogations` and, in a library of its own `derogations` (not part of
  `risks`: a methodology that signs none imports none, ADR 0064), the conditions `no_expired_derogation` (a gate
  guard: a landed change cannot stand on an expired one) and `derogation_debt` (true from
  `condition.DerogationDebtLimit` = 3 open ones; a gate guard or a decision point reads it).
- Expiry: the builtin `derogation.expire` (`pkg/builtins`) closes the derogations that ran out and calls
  `ReopenImpacts` for what they covered, as going back in a lifecycle does (ADR 0058); the clock is the trigger's,
  never the graph's. `methodologies/examples/derogation-expiry.yaml` runs it from a schedule trigger (an example, not
  seeded).
- Where it lives: `pkg/risk` is the use case of risks and actions; derogations are the same family, so they go in
  that package (`pkg/risk/derogation.go`) and `risk.Register()` registers the kind.

### 3. Criticality is data of the change, the weight of it is policy of the organisation

- **WHY.** A change carries a `criticality` (`C1` low, `C2` current, `C3` critical) in `Change.Data`
  (`domain.DataCriticality`, ADR 0065, `pkg/criticality`): the graph never reads it. The methodology gives a default
  (`Methodology.Criticality`, else `C2`), the requester may raise it freely, lowering it needs the permission
  `change:lower-criticality` (administrators by default).
- **WHO.** What each level *requires* is organisation policy: `organisation@CriticalityPolicy` nodes (organisation
  domain 1.12.0, `adminOnly`), one per level, resolved along the unit chain, the nearest unit winning, the whole
  policy and not field by field (rule 3); the compiled-in `criticality.Defaults()` apply where none exists. A policy
  holds the accepted oracle kinds, whether sampling is allowed, the role of a derogation signatory and the maximum
  derogation lifetime. The methodology does not know the table.
- **Reading it.** CEL sees `change.criticality` and `criticalityPolicy` (`{oracles, sampling, signatoryRole,
  maxDerogationHours}`; not `policy`, which a decision point already uses), so a guard or a decision point reads
  them with no new mechanism: `change.criticality == "C3" ? human_signed : tool_verified`. The resolved policy
  reaches the blackboard as a facet; without it (split deployments, where the engine and the registry see no
  organisation) the compiled-in table is read.
- **Gate criteria.** A transition (ADR 0058) may split its criteria into `vetos` (one unmet blocks, never
  compensated) and `objectives` (evaluated; an unmet one is covered by an open derogation in force whose rule is the
  name of the objective). `domain.Transition.Vetos` / `Objectives`, evaluated by `condition.CheckGate`; two lists in
  the guard, not a new gate type. The reserve is recorded as `unmet` / `reserve` (objective to derogation key) on
  the transition item; `go_with_reserve` is not a ruling of a decision point.

### 4. Where it lives

| Question | Concept | What |
|---|---|---|
| WHO | Organisation | criticality policies, derogation signatories, `derogation:sign`, `node:review` rules |
| WHY | Change | `criticality` in `Change.Data`; the derogation and verification facts |
| HOW | Methodology | `verify` on the action; the `verification` library of conditions |
| WHAT | Domain | nothing: the domains stay unaware |
| WITH WHAT | Adapter / model alias | the alias of a `model` oracle must differ from the producer's |

Concepts stay independent (rule 1): the methodology names a kind of verifier, the organisation names who and how
strict, the change is where they meet at run time.

## Consequences

- An agent can no longer accept its own output where `verify.independent` holds, and the reviewer of every
  accepted effect is traceable (PROV-O export gets the `fact.verification` entries, ADR 0057).
- "Accepted with reserve" becomes visible and countable; debt has an expiry instead of accumulating.
- One field (`criticality`) and one policy table give gates and reviews their weight without a gate per level.
- The graph gains four seams (`ReviewPolicy`, `ItemAuthorizer`, `ItemPolicy`, `ReopenImpacts`) and no vocabulary;
  `pkg/layering` keeps `pkg/graph` and `pkg/domain` free of `pkg/verify`, `pkg/criticality` and the derogations of
  `pkg/risk`.
- Greenfield: no migration, no shim; existing methodologies without `verify` behave as today.

## Not decided (open)

1. **Proof of independence.** Principal and alias differ; this does not show uncorrelated errors (same base model
   under two aliases). Left to the policy; a measured criterion needs the capability profile (a later ADR).
2. **Sampling.** `C2` allows sampled human review; the sampling rule (rate, non-announced) is policy data, its
   shape is not fixed here.
3. **Default criticality** by methodology or by project (ADR 0039): both are possible, pick on the first use case.
4. **Per-action state vs impact review.** Keep the two folds separate (action run, impact) or one; start separate,
   merge if the web view shows duplication.

## Not done

- No web editor for the methodology criticality, the vetos / objectives of a transition, nor the
  `CriticalityPolicy` nodes (the change header and the Verification / Derogations panes exist).
- Sampling is policy data only; nothing enforces it.
- No decision-point ruling carries a derogation key.
- Split deployments: the engine and the registry see the compiled-in table through the facet, not the organisation's.
- The signatory is checked as the writer of the item only, not as the person named in `signatory`.
- The expiry trigger is not tested end to end through `TriggerManager`.
- Actions generated for process steps get no `verify`.
- A human review through the graph handler writes no verification fact.
- A model alias is only checked against the producer's recorded alias.

## Plan (phases, one fresh sub-agent each; all done)

1. `Action.Verify`, `pkg/verify`, `Graph.ReviewPolicy`, verification items, layering rule.
2. `derogation` item kind, `risk.Derogations`, CEL `derogations`, library, `derogation.expire`, ABAC `derogation:sign`.
3. Criticality in `Change.Data`, `pkg/criticality`, `CriticalityPolicy`, CEL, vetos / objectives, web panes.
4. Docs: `docs/architecture.md`, `docs/dsl.md`, the `CLAUDE.md` note. The example in `methodologies/sdlc.yaml` was not
   added: its actions have no review step through the engine, so `verify` there would only make the tests drift.
