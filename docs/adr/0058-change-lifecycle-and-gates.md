# ADR 0058 — A change may have a lifecycle; its transitions are gated by decisions and expected world state

**Status**: accepted, mechanism implemented · **Date**: 2026-10 · Builds on ADR 0009, 0014, 0034, 0035, 0051, 0056.

## Context

A change only has a technical status (`draft`, `active`, `committed`, `applied`, `abandoned`): where it is in its
integration, not how mature it is. Nothing says that a change was proposed, is being analysed, then implemented, and
nothing makes the move from one phase to the next a formal act. A methodology has steps with exit conditions
(`done`, ADR 0034) and decision points (ADR 0009 §4), but no way to say "this work belongs to the analysis phase, and
the phase is over only when its expected effects hold and someone has decided so".

The platform must stay generic: the phases of a given methodology (proposed, analysing, implementing, or anything else)
are a use case, not a platform concept.

## Decision

A principle, in five rules. Rules 1 to 3 are the mechanism; what states and gates exist is left to domains and
methodologies.

1. **A change may have a lifecycle.** The **Domain** defines lifecycles (`domain.Lifecycle`, ADR 0014), unchanged and
   not specialised for changes. The **Methodology** says which one its changes follow (`lifecycle: <name>`; none: the
   change has no state and behaves as before). The **Change** holds the current state (`Change.State`) and the lifecycle
   it follows. The lifecycle is orthogonal to `ChangeStatus`, which stays the technical machine of branch, commit and
   integrate (ADR 0056). Who / why / how / what stay separate: the domain says what states exist, the methodology how
   a change goes through them, the change where it is.

2. **A transition is guarded by a CEL guard, a gate is a convention of that guard.** The guard model does not change.
   For a change lifecycle the guard runs in the condition environment (`change`, `decisionPoints`, the world
   state, ADR 0002), not the node one. A *gate* is a guard that requires:
   - a **decision**: a decision point of the transition exists and is `decided` in favour (ADR 0009 §4);
   - the **expected world state**: the conditions that must hold, which are the expected effects reached at the end of
     the steps and methods of the state being left (their `done`, or `step:<path>`, ADR 0034). The gate owns no
     checklist of its own; the text `checklist` of a step stays guidance, what is verified is its `done`.
   - **The decision prevails over the world state** once actions are taken to fill the gap: the guard reads the
     actions of the change (`actions`, an action's `for` being the decision point it answers, ADR 0036 §1), so that
     `decided && (world["analysed"] || actions.exists(a, a.for == change.decision))` lets the change move with a
     gap that someone has committed to close. The gap stays visible: the actions remain in the register.

3. **The methodology couples states and steps.** A step names the state it is active in through its entry: the
   condition `state:<name>` (`change.state == "<name>"`) is generated whenever a step names it in its `pre`, no field of
   its own. The lifecycle tracks the maturity, the process does the work; a gate reads what the process established.
   Conditions follow ADR 0051 (a condition is never both an input and an output of a step).

4. **A gate freezes, it does not land.** Leaving a non-editable state freezes the impacts written in it, as a node in
   a non-editable state is frozen (blackboard `Frozen`). Landing is unchanged: the change lands once, at its end
   (`CommitChange` / `IntegrateChange`); when the change has a lifecycle, committing is refused while its state is not
   a final state. Going back (implementing → analysing) is a transition like any other, whose guard asks for a new
   decision; it unfreezes the phase it returns to and marks the impacts of the later phases `stale`, so that they are
   reviewed again before the change can leave analysis a second time.

5. **A transition is journaled and authorised.** `Graph.TransitionChange` (RPC `TransitionChange`) checks the
   transition out of the current state, the permission (the transition's own, by default `change:transition`, ABAC,
   ADR 0020), the guard, then writes a `transition` fact (kind `KindTransition`, data transition / from / to /
   decision, never written through `AddItems`) to the log of the change (ADR 0030), moves `Change.State`, exported by
   PROV-O (ADR 0057) and published as `change.updated` (ADR 0053).

## Mechanism

- The registry resolves what the graph does not know (`graph.ChangeLifecycles`, implemented by `registrysvc.Service`,
  wired in `goap-dev`; the separate `graph` service wires it when it gets a registry client): the lifecycle of a
  methodology (`Methodology.Lifecycle`, a lifecycle of the domain of its namespace) and the guard evaluation, with the
  world state of the compiled methodology. `CreateChange` sets `Change.Lifecycle` / `State` (initial state); a
  methodology the registry does not know, or naming none, leaves the change without state.
- The guard is the node one (`pkg/guard`), its environment widened with `decisionPoints` and `world` (condition name
  to bool); `condition.CheckGuard` builds it from the blackboard. It sees `change.transition` and `change.decision`,
  the decision point the caller names. A decision point gates one transition only: the ones already named by a
  transition are left out of what a guard sees. Example:
  `world["analysed"] && decisionPoints.exists(d, d.id == change.decision && d.status == "decided" && d.option == "go")`.
- Freeze: the state an impact was last written in is derived from the log (the order of its `written` events against
  the `transition` facts); `WriteNode` refuses an impact written in another state than the current one. Going back to
  a state already occupied puts the impacts written since in `proposed` again (an impact review event).
- `CommitChange` / `Apply` refuse a change whose state is not final, checked before their transaction.
- Default policy `onProject` for `change:transition` (new graphs; an existing graph adds it).

## Out of the principle

What belongs to the use case, handled by domains and methodologies and added later: the states themselves
(`proposed`, `analysing`, `implementing`, ...), their gates in `sdlc`, the lifecycle of sub-changes and administrative
changes, the web stepper of a change.

## Consequences

- A methodology without `lifecycle` is untouched.
- No new node type, guard language or checklist structure: a lifecycle, a CEL guard, decision points and step
  conditions already exist.
- Freeze is a state of the change, not of the branch: nothing lands before the end. If partial landing of frozen
  phases is wanted later, it is a separate decision on top of this one.
- Open: impacts written while the change has no state (never frozen); whether a sub-change inherits the lifecycle of its parent.
