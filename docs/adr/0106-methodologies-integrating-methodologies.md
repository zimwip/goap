# ADR 0106 — Methodologies integrating methodologies: the guardian chain, attach / detach, coupling by events

**Status**: accepted, implemented (see "Not done") · **Date**: 2026-10 · Extends ADR 0036 §3 (transverse
methodologies, `on:`), ADR 0058 / 0102 (the guardian of a change), ADR 0103 (sub-changes on their own branch), ADR
0087 / 0090 (assistant)

## Context

A methodology constrains one change. Above it, a project delivers in its own mode (waterfall, agile, SAFe...): it
screens the open changes for the candidates of a release, validates them, drives their integration and delivery.
Above a project, a portfolio may plan its releases the same way. Today `sdlc` does its release alone
(`release_train`, `application`), one change at a time.

The platform already has the two couplings this needs, each in one place: the **merge of a sub-change into its
parent** (ADR 0103: forked from the parent's branch, merged into it under its own guardian, only the root lands on
main) and **events** (ADR 0036 §3: a transverse methodology runs on the same change, woken by `on:` events). What was
missing is a way for a methodology to say, in its own definition, that its changes **integrate** the changes of other
methodologies, what it requires of them, and which events cross that relation. Hard-wiring "project" and "change"
levels would only move the problem one level up.

Separately, the assistant opened a new change without looking for one that already answers the question: the
screening a release does for its candidates has a counterpart at the opening of a change.

## Decision

### 1. `integrates:` — a methodology declares what its changes integrate

```yaml
name: release-based
integrates:
  - methodologies: [sdlc]          # or ["*"]: any methodology
    exclusive: true                # those changes merge only once attached to a change of this methodology
    attach: decision               # how a candidate becomes a sub-change: decision (default) | auto
guardian:
  merge: 'integrator.state == "integrating"'
  # transitions: {<transition of the integrated change's lifecycle>: <CEL guard>} (release-based declares none)
on:
  - {event: change.committed, from: integrated}
  - {event: change.transitioned, from: integrated}
  - {event: change.detached, from: integrated}
```

- **Levels are derived, never declared.** A methodology that integrates others is one level above them; the relation
  chains (a portfolio integrating `release-based`). There is no `level` field: a project lists its methodologies in
  `ProjectUnit.methodologies` as today, and an integrator frames the changes of a project only when it applies there.
- `pkg/methodology/integrate.go`: `Integration{Methodologies, Exclusive, Attach}`, `Guardian{Merge, Transitions}`,
  `IntegratesChangesOf`, `IntegrationOf`, `MergeGuard(of)` (the `merge` guard, prefixed by `integrator.attached` when
  the integration is exclusive), `TransitionGuard(name)`; the compiler (`checkIntegration`) refuses an integration
  naming no methodology or itself, an unknown `attach`, a `guardian` without `integrates`, a guard that does not
  compile, an unknown `on.from` and `from: integrated` without `integrates`. Attributes `integrates` / `guardian` of
  `methodology@MethodologyVersion`, proto `Methodology.integrates` / `guardian`.

### 2. The guardian chain — federated guardians, saved on the change

The guardian of a change is the **chain** of the methodologies that frame it, outermost first, its own last, stored on
the change (`Change.Guardians`, a list of `domain.GuardianRef{methodology, version}`; column `change.guardians` in both
dialects, migration `0002_change_guardians.sql`; proto `Change.guardians`). It is computed once:

- a sub-change created under a parent: the parent's chain (its methodology when it has none) plus its own methodology
  (`chainUnder`);
- a root change: `ChangeLifecycles.Guardians(methodology, project)`, the registry walking `integrates` upwards over
  the methodologies applicable to the project chain (`Service.Applicable`, nearest project first; `[portfolio,
  release-based, sdlc]`), at most 8 deep; none: `[sdlc]`, the behaviour before this ADR;
- attached to a parent: the parent's chain plus its own; detached: recomputed as for a root change.

Saved rather than recomputed, so a change keeps the rules it started with when a project's methodologies change; an
attachment or a detachment journals the old and new chain in a `change.updated` entry (`HeaderGuardians`).

**Combination.** Every guardian of the chain must agree, innermost first:

- **Merge** (`checkMerge`, `pkg/graph/mergecheck.go`): the own methodology as before (final state of its lifecycle,
  its Activity goal, else the landable states), then `checkGuardians`: each outer methodology's `MergeGuard` of the
  methodology just inside it. The first refusal is reported (`ErrConflict`) with the methodology and the guard.
- **Transitions** (`TransitionChange`): the change follows the lifecycle of its own methodology; each outer guardian
  may add a guard to a transition by name (`guardian.transitions`), checked after the lifecycle's own guard, vetos and
  objectives.
- `ChangeLifecycles.CheckGuardians(bb, transition, ancestors)` evaluates the chain in the registry
  (`internal/registrysvc/guardians.go`); `pkg/graph` stores the chain, collects the ancestor changes and names no
  methodology. Each guard is CEL in the condition environment of the change's own methodology plus `integrator`
  (`condition.CheckGuardian`, `guard.Facts.Integrator`): `{attached, id, methodology, state, status}` of the nearest
  ancestor change of that guardian's methodology, `attached` false and the rest empty when there is none
  (`guard.NoIntegrator`). A guardian the registry no longer knows says nothing; its version saved in the chain is
  read when still stored, else the latest.

### 3. Attach and detach

`Graph.AttachChange(id, parent)` (operation `change.attach`, asked on the parent) makes an open root change a
sub-change of an open change whose methodology integrates its own (`ChangeLifecycles.Integrates`): one namespace, the
change in the parent's project or below, held by the parent's unit or below, no cycle. Its branch is forked again from
the head of the parent's branch, what it committed rebased there (`rebaseCommittedTx`; a conflict stops it and
nothing changes), its chain replaced. `Graph.DetachChange(id)` (operation `change.detach`, asked on the parent: the
release defers or rejects a candidate) makes it a root change again, forked from the head of the branch its root
ancestor merges into. RPCs `AttachChange` / `DetachChange`, `graphsvc.Client`, `engine.GraphPort`. The integration
itself is the existing merge of a sub-change into its parent (ADR 0103).

**Where a person attaches.** `goap-change` tools `attach` (`{change, parent}`) and `detach`; the change tab's *Attach
to change…* (the open changes of another methodology in its namespace, `web/src/lib/changeAttach.ts`,
`AttachChange.svelte`) and *Detach*, and its *Guardians* row showing the chain; the assistant's proposals (§6).

**Validation and moves.** The registry refuses a cycle of integrations at validation (`integrationIssues`: a
methodology integrating, directly or through others, one that integrates it; the wildcard is not followed).
`MoveChange` computes the chain of the root change again for its new project, and of its sub-changes from it, journaled
in the same `change.updated` entry (`guardians` field). `cmd/registry` gets `Applicable` from the organisation snapshot
like `goap-dev`.

**Composition checks.** A methodology compiles alone, but what it expects of the ones it is composed with holds only
against their definitions. The registry's validation adds `compositionIssues` (`methodology.CompositionIssues`,
`pkg/methodology/composition.go`: the latest version of every other stored methodology, the lifecycles of the domains
in force), **warnings** each carrying a fix (`Issue.Hint`, proto `Issue.hint`), never an error, since the others change
on their own:

- an event a subscription waits for that its source cannot emit: `change.transitioned` of a methodology naming no
  lifecycle, `from: integrator` while nothing integrates the methodology, `change.attached` of changes nothing
  integrates, process events of a methodology running no process, `methodology.published` from another change;
- a value a filter or a guard compares (`sel == "x"`, `sel in [...]`) that its source does not declare: a state
  (`event.change.state`, `change.state`, `integrator.state`), a process, a step or step path, a methodology, a
  condition of the world (`world.x`); a `guardian.transitions` key no transition of the integrated lifecycles has; an
  `integrates` name no methodology has — the hint names the closest, else the list;
- a guard that never holds: `MergeGuard` (with the exclusive `integrator.attached`) and each transition guard, tried
  over every state the integrated change may be in (for a transition, the states it leaves), every integrator (none,
  or a change of the guardian in each state of its lifecycle) and the world conditions it reads (at most 4); a guard
  reading anything else is not judged;
- a **composite** contradiction: the guardians of a chain (the guardian, the ones between it and the change, the
  integrators above it) each satisfiable alone but never together on the same change — the change is shared, each
  guardian sees its own integrator. A guardian frames every change below it, down the integrations.

What a version of a methodology breaks for the ones following it is reported on it too (`lifecycle`, or the
methodology): removing the lifecycle `release-based` follows the moves of. The web keeps the composition in the
methodology form (lifecycle, integrates, guardian, `on[].from`, edited in the *Composition* card of the overview) and
shows each issue with its fix in the Problems console, the header of the element and under the field
(`IssueHints.svelte`). The check found the first gap at once: `release-based` follows `change.transitioned` of `sdlc`,
which has no lifecycle yet.

### 4. `on:` crosses the relation — coupling by events

`on:` (ADR 0036 §3) gets a `from`: `self` (default: the events of the change the methodology runs on), `integrated`
(the events of its sub-changes), `integrator` (the events of its parent). A methodology with an `on.from` other than
`self` needs no `appliesTo`. The engine relays an event of a change to its parent (`from: integrated`) and to its open
sub-changes (`from: integrator`) and wakes the processes of their methodologies that subscribe to it on **their own**
change (`Engine.relay`, `pkg/engine/companion.go`), with `vars.event.relation` saying which side it came from; the
runs waiting for conditions there are retried. New events, constants of `pkg/events` checked against the web's
`TRIGGER_EVENTS`: `change.transitioned`, `change.committed`, `change.attached`, `change.detached` (published by the
graph handler and the engine's `EventingGraph`). The merge of a sub-change into its parent is its `change.applied`.

A methodology reads another change only through its events and its lifecycle state; it never names the steps or
conditions of another methodology (design rule 1).

### 5. First integrator: `release-based`

`methodologies/release-based.yaml` (namespace `alm`, lifecycle `delivery` added to the `alm` domain, 1.3.0:
`planning` → `screening` → `integrating` → `delivered`, `cancelled`) integrates `sdlc` exclusively: a release screens
the open changes of its project for candidates, the release manager selects them (attached as sub-changes), they merge
into the release while it is `integrating`, then the release lands on main. Its `candidates` process follows the
moves of the integrated changes. `release_train` and the `application` step of `sdlc` stay in `sdlc` for projects that
do not use an integrator; moving them out (with `agile` / `safe`) is a follow-up.

### 6. The assistant works in a frame

Each turn computes its frame from the context of the turn and the history (`internal/assistantsvc/frame.go`): level
`change` when the person looks at a change they can read (its methodology with it), else level `project`. Tools and
prompt follow the frame. At level project, `create_change` (and `start_agent` with a new change) is offered only once
`search_changes` (the open changes of the active project matching the intent, `searchtool.go`) ran in this
conversation, so the assistant proposes an existing change before opening a new one.
At level change it may propose to attach the change to a change integrating it (`attach_change {parent}`) or to
detach it (`detach_change`): proposals the person confirms like `start_agent` (`ConfirmAction` re-validates, then calls `AttachChange` / `DetachChange` as them;
`internal/assistantsvc/relate.go`), shown as cards with *Attach* / *Detach* (`web/src/lib/assistant/proposal.ts`).

## Consequences

- One generic mechanism, declared in the methodology: `integrates`, `guardian`, `on.from`, the new events; no
  hard-coded level; the guardian of a change is the combination of the chain saved on it.
- Sub-changes keep their parent's namespace: an integrator spanning namespaces needs one change per namespace.
- A project using an exclusive integrator holds the merge of the changes it integrates until they are attached:
  enabling `release-based` on a project changes how its `sdlc` changes land.

## Not done

- `attach: auto` is declared and compiled but the engine attaches nothing on its own: which open change of the
  integrator receives a new change is a policy not decided yet. `release-based` attaches through its human step (the
  change tab's *Attach to change…*, `goap-change/attach`, or the assistant's proposal).
- `cmd/graph` (distributed) wires no `ChangeLifecycles` (no registry in process), so a change gets no chain and no
  outer guardian there, as for its lifecycle (ADR 0058).
- Dependencies between changes are not declared: only the order of the merges into an integrator expresses them (a
  `depends_on` relation checked by the integrator's guardian is the likely form).
- Moving a change from one integrator to another is a detach then an attach while it has not merged; once merged, its
  work belongs to that integrator's change and leaves it only by withdrawing those impacts there.
- Deliverables (a `project` built-in domain) are left to the follow-up.
