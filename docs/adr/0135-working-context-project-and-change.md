# ADR 0135 — The working context: a project chosen, then a working change

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0039 (projects), 0052 (front location), 0091
(no change without a project), 0092 (contextual assistant), 0096 (the methodology of a change)

## Context

The header showed the active project as "Root project" before anyone chose one: an empty project claim meant the root
project, so a person who never picked a project worked in the root without knowing it, while the assistant (since its
frame, ADR 0092 and PR #36) treated the same empty claim as "outside any project". The change editor opens as many
changes as one likes, which is right to look around, but nothing said which one the person works in: opening a change
switched the active project to its own as a side effect, and the assistant took "the change of the tab" as the change it
works on, so moving between tabs moved its frame.

A change created by a path that names no methodology (an RPC, a tool) had no way to get one afterwards, although every
change is performed by a methodology (ADR 0096).

## Decision

### 1. No project at first

The web's active project is empty until the person (header selector) or the assistant (`select_project`) picks one; the
root project is a project like the others, picked by its key, never implied. The selector shows *Select a project* and
offers *Work outside any project* to clear it. With no project, the changes explorer lists every project's changes and
the form of a new change asks for its project. The server is unchanged: an RPC naming no project still resolves the
root (ADR 0091, `graphsvc.Handler.projectOf`), for the tools and scripts that call it.

### 2. A working change, chosen

The working change is the change the person works in, within the active project
(`web/src/lib/stores/workingChange.svelte.ts`). It is chosen explicitly: the header's selector next to the project (the
open changes of the project), *Work in this change* on the change editor, or the assistant (`open_change`,
`create_change`); a change created by hand (new change form, a node edit) becomes it too. Opening a change only shows
it: the project switch on opening is gone. Choosing a change of another project switches project first; switching
project leaves the working change and takes back the one last chosen in the new project. A change that lands or is
abandoned is left. It is not graph data and not a token claim: the preferences service keeps the last working change
of each project (`workingChanges`, ADR 0038), so a reload or another device finds it. The node editor edits in the
working change when it acts on the node's namespace and the person picked no other.

### 3. The assistant works on the working change

The context of a turn carries it (`AppContext.change`, `assistantsvc.App.Change`, rendered `workingChange`, and in the
description kept on the user message). The frame of a turn is the change `open_change` opened, else the working change;
a change tab that is not the working change is only named in the prompt ("looking at change X, which is not the working
change"), and `open_change` makes it the working change. `select_project` to another project than the working change's
leaves it for the rest of the turn.

### 4. A change with no methodology takes one

`Graph.SetChangeMethodology` (`//goap:operation change.methodology doer`, RPC `SetChangeMethodology`) gives an open
change that follows none the methodology named, as a creation would have: its latest version, its main goal when the
change has none, its lifecycle in its initial state, its guardian chain; journaled in a `change.updated` entry
(`methodology`, `methodologyVersion`, `guardians`). `Graph.MethodologyGate` (`graphsvc.MethodologyGate`, plugged by
`platform/cmd/graph` and `goap-dev`) checks that the methodology applies to the project of the change. The change tab
offers the methodologies of the change's project where the methodology would be shown.

## Consequences

- The person always sees where they work: a project, then a change, both in the header.
- The assistant's frame no longer follows the tabs; it follows the person's choice.
- `PutChange` updates the `methodology` column (both dialects): a change keeps its methodology once it has one, the
  graph refuses a second.
