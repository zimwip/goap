# ADR 0107 — Effects on impacts and items of the blackboard

**Status**: accepted, implemented (the blackboard side; the methodology side is to come) · **Date**: 2026-10

## Context

The world state a methodology plans over is computed from the blackboard: its conditions are CEL over the change, its
change impacts and its items. What the work establishes *about* a subject had no place of its own there. "The
requirement REQ-9 is formulated", "the risk RSK-1 is assessed", "this report was read by the architect" were either
left implicit (a step's effect, true for the whole change, not for one subject), or written into the data of an item by
convention (a `status` field one methodology reads and another ignores), or not expressible at all for a change impact,
whose state is its draft and its review.

A condition such as "every requirement of the change is formulated" or "every live risk is assessed" needs a fact per
subject, set by whoever established it, readable the same way whatever the subject is.

## Decision

1. **An effect is a named value attached to a subject of the change**: a change impact (by its id) or an item (by its
   id). The value is a scalar (bool, string, number). Effects are set by an item of the built-in kind `effect`
   (`domain.KindEffect`, `domain.EffectMark`: `data {subject: impact | item, id, effects: {name: value}}`); a `null`
   value clears an effect. Names are identifiers (`[A-Za-z_][A-Za-z0-9_]*`, at most 64), so CEL reads them as fields.
2. **Effects are blackboard facts like any item**: append-only, with their provenance (`producedBy`, `execution`), on a
   flow (a candidate of an open flow is not in force until the flow is adopted, a superseded one never again), in the
   log of the change (`fact.effect`), in the PROV-O export (`goap:qualifies` the impact or item). No table, no column,
   no new RPC: `AddItems` writes them, and refuses a subject the change does not have (an unknown impact or item, or
   another effect item: `ErrInvalid`).
3. **The fold**: `Change.Effects()` (`domain.EffectIndex`) replays the effect items in force in their order, the latest
   value of a name winning; `OfImpact(id)`, `OfItem(item)`, `OfRecord(kind, key)`. An item that is a version of a
   keyed record (`data.key`: a risk, an action, a waiver, a derogation) carries its effects **for the record**: every
   version of RSK-1 sees "assessed", and the effect remembers the version it was set on (`Effect.On`), so a later rule
   can tell an assessment made on an older version.
4. **Read side**: the CEL variables of a condition carry `effects` (a map name → value, empty when none) on every entry
   of `changeImpacts`, `items` (so `artifacts`, `decisions`, `merges`), `risks` and `actions`:
   `changeImpacts.all(i, i.effects.formulated == true)`, `risks.all(r, has(r.effects.assessed))`. The brief every LLM
   action gets (`pkg/brief`) appends ` effects=formulated,assessed=high` to the impacts, risks, actions and artifacts
   that have some.
5. **Write side for tools**: `goap-change/effect {impact | item, effects}` (an impact by node key or impact id, an item
   by id or by the key of a risk / action, resolved to its latest version in force).

Effects stay attached when their subject changes afterwards: an edit of the draft does not clear "formulated", a new
version of a risk does not clear "assessed". Whether a given effect must be re-established after a change of its subject
is a rule of the methodology that reads it (it has `Effect.On` and the record's versions to compare), not of the
blackboard.

## Not done (the second step)

The use by methodologies: an action or a step declaring the effects it sets on the subjects it works on (per subject,
next to its world-state `effects:`), the planners and `StartingPoints` reasoning per subject, a DSL call to set one from
a script (`ctx.effect`), the engine writing them from an action's result, and the web showing them on the change
(effect items fall today in the debug list of the Artifacts pane).
