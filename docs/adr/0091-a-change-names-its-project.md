# ADR 0091 — A change names its project, and may move to another

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0039 (project), 0043 (project roles), 0054
(structures, guard), 0066 and 0069 (the core names no structure), 0081 (sub-changes), 0058 (change lifecycle).
Supersedes the default project of ADR 0054 §2 and the `default` flag of ADR 0069.

## Context

A change acted in the *default project* when its creator named none: `scopeChange` resolved it to the project flagged
`structure.default` (the root when none was). It hid mistakes (a change opened with no active project landed its nodes
in a project nobody chose), and once created the project of a change could not be corrected, while the project of a node
never changes.

## Decision

1. **No change without a project.** `Graph.CreateChange` (and `Commit`, `MergeBranch`, the splits) refuses a root change
   with an empty `ProjectID` (`graph.ErrInvalid`, "a change names the project it acts in"); a sub-change keeps
   inheriting its parent's. There is no default project: the `default` property of the `structure` tag
   (`def.StructureTag.Default`, `domain.Structure.Default`, proto `Structure.default_property`, `typecat`, the schema
   check), the `default` attribute of `ProjectUnit`, `Graph.DefaultProject`, the web's `defaultProject` /
   `defaultProjectProp` and the Project tab's "Make default project" are deleted. (The *waiting unit* of the
   organisation is another property, `waiting`, and stays.)
   The **edges resolve** the caller's project, an empty claim meaning the root project, never `pkg/graph`: the RPC
   handlers (`Handler.projectOf`: the request's, else the caller's active project, else `Structure(project).Root`),
   `graphsvc.Client.CreateChange` (it did not send the project: fixed), the engine (`resolveChange`: the request's
   project, else the run's, else the root of `Structures`), `goap-change/create` (argument `project`), the assistant
   (`Projects.RootProject`), the seeds (registry, `graphsvc.SeedChange`, `devseed`, alias stubs: the root project).
   A run started on an *existing* change takes **the project of that change** (`Engine.Start`, and the authorization of
   `StartProcess`), not the caller's token. The web's new-change form requires a project (default: the active one,
   chosen among the projects of the head graph; a sub-change has its parent's). `change.project_id` was already
   `NOT NULL CHECK (project_id <> '')` in both dialects.

2. **`MoveChange`.** `Graph.MoveChange(ctx, id, project)`, RPC `MoveChange` (`graph.v1`, field `change_id`, so
   `PersonalScope` applies), `engine.GraphPort.MoveChange`, `graphsvc.Client.MoveChange`, `goap-change/move`, web
   "Move to project…" on the change overview (`MoveChange.svelte`, `web/src/lib/changeProject.ts`). Rules of the graph:
   only a **root** change (a sub-change's project is its parent's: the family moves with its root), only while `draft`
   or `active` (a `committed`, `applied` or `abandoned` change is `ErrConflict`), the target an existing project other
   than the current one (`ErrInvalid`). The change and its open sub-changes get the new project in **one
   transaction** (the parent/sub project invariant of `prepareSubChange` holds trivially: they are equal). The row is
   updated by `PutChange` (the `ON CONFLICT` clauses of both repositories now include `project_id`).

3. **The methodology rule.** The change keeps its methodology, so the methodology must be applicable (own or inherited
   from the ancestors of the project) to the project the change leaves **and** to the one it reaches, for each change of
   the family; a change with no methodology is held to no such rule. The graph names no methodology (ADR 0066), so the
   check is a seam: `Graph.ProjectMoveGate func(ctx, family []domain.Change, to string) error`, asked outside the
   transaction (it reads the graph) and checked again for consistency inside (the family and its projects must not have
   changed, else `ErrConflict`). `graphsvc.ProjectMoveGate(authorizer, directory)` is plugged explicitly by `cmd/graph`
   and `goap-dev` (nil: no check beyond the graph's own rules, as for the other seams). It reads
   `access.Snapshot.ApplicableMethodologies` and returns an error naming the methodology and the project that does not
   list it.

4. **Authorization.** New ABAC action `change:move` (default policy `onProject(r.sub)`, like `change:transition`),
   asked on the project of each change and on the target (the authorizer merges the roles held on the resource's
   project), together with `Snapshot.MayAccessProject` for both; platform administrators pass, a platform service
   (`authz.Principal.System`) is not asked. A personal change of someone else is not found (`PersonalScope`).

5. **Nodes keep their projects.** The project of a node version never changes (`guard.go`): the nodes a change checks
   out keep theirs. A draft stores no project, so no draft is rewritten; the nodes the change creates (before or after
   the move) take the project **of the change at landing** (`guard` resolves it from the change record read in the
   commit transaction). The confirmation text of the web says so.

6. **Running processes follow the change.** The roles of a process are checked on its `ProcessRef.Project`;
   `Engine.observe` already sets `p.Project = bb.Change.ProjectID` at every cycle, so a process of a moved change checks
   its roles on the new project from its next cycle. No move is refused for a running process, and no port to the
   engine is needed (so `cmd/graph` without an engine behaves the same). The administrative / criticality marks the
   engine put in `Change.Data` at creation are kept by a move; the criticality policy resolved along the unit chain is
   unaffected (the owner unit does not change).

7. **The log.** Each moved change appends a `change.updated` entry (the header entry of `UpdateChange`) with the field
   `projectId` `{from, to}`, `Subject` `projectId`, `By` the caller, in the move's transaction; the write publishes the
   usual `change.updated` event. The web audit trail reads it "moved — project: A → B".

## Consequences

- `scopeChange` keeps resolving the owner unit (an unset one is the root unit), not the project.
- Callers that relied on the silent default now pass a project; tests and fixtures pass one explicitly (`graphtest`
  uses the root of `Structure(project)`).
- A web caller cannot tell from the interface which projects it may access: the move dialog offers the projects whose
  methodologies include the change's, and the server refuses the rest (`change:move`, access).
- Not done: moving a sub-change on its own, moving a committed change, and moving the nodes of a change.
