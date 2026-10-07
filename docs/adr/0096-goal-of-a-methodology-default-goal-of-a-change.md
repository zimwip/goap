# ADR 0096 — The goal of a methodology, the default goal of a change, a methodology at creation

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0023 (methodologies are graph nodes), 0034
(processes reach the goal of their name), 0039 and 0091 (projects name their methodologies), 0058 (`ChangeLifecycles`).

## Context

`Change.Goal` was set by the engine each time a root run selected a target, so it meant "the goal of the last run", and a
change created by hand had none. Every change should work towards a goal of its own, the one its methodology states,
and that goal should drive the actions the change offers.

## Decision

### Who, why, how, what

The goal is the *why* of a change expressed with the *how*: it lives in the methodology (the main goal) and is copied to
the change when it is created. The graph still knows no methodology: it asks the same seam as for lifecycles.

### The goal of a methodology

- `goal:` is a methodology field (`Methodology.Goal`, registry proto `Methodology.goal`, attribute `goal` of
  `methodology@MethodologyVersion`). It names a declared goal or a process (a process reaches the goal of its name);
  anything else is a compile issue tied to the path `goal` (`checkMainGoal`).
- `Methodology.MainGoal()` (promoted to `Compiled`): the declared `goal`, else the first declared goal, else the first
  process, else "".
- Shipped methodologies: `sdlc` -> `deliver` (the end point of the process, which pulls the scheduler; `formulate` is its first stage),
  `methodology-improvement` -> `improve_methodology`, the examples their delivery goal. `risk-management` is transverse
  (no change of its own) and sets none.

### The default goal of a change

- `graph.ChangeLifecycles` gains `DefaultGoal(ctx, methodology) (string, error)` (registry: `Service.DefaultGoal`, read
  from the stored definition so a methodology that does not compile still gives its goal; "" when unknown).
- `NewChange.Goal` overrides it. `CreateChange` sets `Change.Goal` = `in.Goal`, else the default goal when a registry is
  plugged and a methodology is named. It is the initial value of the change, not a header edit: no `change.updated`.
- A sub-change (and the changes `SplitChange` makes) keeps its parent's current goal unless it names its own goal or
  another methodology (whose default applies).
- The engine creates its changes through `CreateChange` (`resolveChange`), so they get the same default.

### A stable goal

`Engine.selectTarget` no longer writes `Change.Goal`: the goal of a run lives in the `Process`. The goal of a change
changes only by an explicit edit (`ChangePatch.Goal`, logged as `change.updated` field `goal`). The readers (CEL
`change.goal`, trigger events, brief, index, PROV, sandbox `Ctx.Goal()`) read the stored value unchanged. When the
methodology is edited and no longer declares the stored goal, nothing is validated on read; the change tab shows
"unknown goal".

### A methodology at creation (web, assistant)

Every change made by hand has a methodology: `NewChangeForm` offers the methodologies applicable to the chosen project
(`projectRoles.applicableMethodologies`), preselects the only one, requires it, takes the namespace from it and sends it
(`changeProject.methodologyOffer`). A project naming none cannot hold a new change from the form, which says so and
links to the project tab. The assistant has the `set_methodology` effect tool on that screen (the assistant's
`create_change` already requires a methodology). The graph and RPC creation paths stay free of the requirement
(administrative and internal changes exist). A sub-change made in the form inherits its parent's methodology.

The methodology editor lets the author choose the main goal among its goals and processes (`goalChoices`); the change
tab shows the goal with the description the methodology gives it.

## Not done

Starting points (suggestions by the scheduler); the RPC `CreateChange` request has no `goal` field (the Go API has).
