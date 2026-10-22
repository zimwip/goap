# ADR 0157 — Every graph read names its view

**Status**: proposed · **Date**: 2026-10

## Context

People and agents work on a view of the graph, the way a git user works in a working tree: the final state of their
pending change, on top of the branch it forked from. Versions exist for traceability and audit. A person picks a view,
and does not pick versions, except to look at the history or a diff.

The reads of the domain graph do not say so today. Each RPC picks its own scope:

- `GetNode` by key or at version 0, `TagVersion` at version 0 and `graph.Graph.NodesOfType` read the **latest on
  main**, whatever the caller works in.
- `GetBaselineGraph`, `ListBaselineNodes`, `ListBaselineLinks` and `GetNodeNeighbourhood` read a baseline.
  `GetNodeNeighbourhood` alone also takes a change and a flow (since the bounded node tab).
- `GetBlackboard`, `GetChangeGraph`, `GetChangeView` and `ValidateBoard` read a change on a flow.
- `GetGraphStats`, `ExportGraph` and `PlanMerge` read a branch head.
- The index `Search` filters on `main` (heads of main / not on main / every version) and knows no baseline and no
  change. Its `similar_to` names a node without a version.
- The `goap-graph` MCP tools read the head of main or a baseline. They load it whole and have no change argument. The
  `goap-change` tools read the change on its main flow.
- `engine.GraphPort` has no node read of its own. The engine reads `ChangeGraph(change, flow)`, else
  `BaselineGraph(baseline)`.

So the same question (what is node X now, what links to it, which nodes match "payment") gets different answers
depending on the endpoint. Several endpoints answer "main" when the caller works in a change or on an option.

The methodology side is already pinned. A change keeps the methodology version it started with, and a process runs the
version it started with (ADR 0113).

## Decision

1. **A view is a branch and a baseline.** `graphapi.View` (proto `goap.graph.v1.View`) is one of:
   - `{namespace, branch, baseline_id}`: a lasting branch at one of its baselines. An empty baseline means the head of
     the branch when the read starts. The answer names the baseline it read, so the next read can pin it.
   - `{change_id, flow}`: a change on an option (`main` or an option id). It resolves to the change's branch and its
     pending work: its fork, its commits and the drafts of that flow (`ChangeWorld`, ADR 0140). The namespace is the
     change's.

   The web builds the view from the header (`contextView.ts`):
   - the working change on the option selected there, for the namespace it acts on;
   - for every other namespace, and when no change is selected, the head of main.

2. **Every read of the current graph takes a view, and refuses a request without one** (`InvalidArgument`, "no view").
   - The reads concerned: node by key or id, neighbourhood, search page, links page, blackboard, change graph, stats,
     export and the index search.
   - Reads at an explicit version stay as they are, because they are history: `ListNodeVersions`, `GetNode` with a
     version, `ListBaselines`, `DiffBaselines`, `GetChangeStack`, `ListNodeChanges`, `DiffFlows`, and the Baseline
     explorer and tab, which pick a commit.
   - "Latest on main" is no longer a default anywhere. A caller that means it says so (`{namespace, branch: main}`).

3. **One read model behind every view.** `Graph.ViewOf(ctx, view)` opens a reader with the same operations whatever
   the view: `Node(id | key)`, `Hood(id)`, `Search(query, type, page)`, `Links(type, query, page)` and `Nodes(ids)`.
   - Over a baseline it reads the baseline.
   - Over a change it lays the impacts of the flow over the branch or the fork, for the nodes asked only.
   - It never reads a namespace whole. A whole read (export, `GetBaselineGraph`) is a separate, explicit operation
     taken by a job or a history view.

   The existing pairs become one each:
   - `NodeNeighbourhood` / `ChangeNodeNeighbourhood`;
   - `ListBaselineNodes` / the change-created overlay of the web's `searchView`;
   - `GetNode` / `GetNode` with `change_id`.

4. **The index searches a view.** `SearchRequest.view` replaces `main`:
   - a baseline view selects the versions in force at that baseline;
   - a change view selects the head of its fork, overlays the change's drafts and versions of that flow (read
     through `ViewOf`), and drops what the change withdrew.

   `similar_to` names a node in the view.

5. **Agents read their process's view.**
   - `graphapi.Port` gains `Read(ctx, view) Reader`.
   - The engine passes `{change_id, flow}` of the process.
   - The `goap-graph` tools take an optional `view` argument, defaulting to the calling process's change and flow,
     else the head of main. They read through the reader instead of loading a baseline whole.
   - `graph.Graph.NodesOfType` without a view goes away; validators keep the `ValidatorQuery` of the commit they judge.

6. **Writes are unchanged.** A write names its change (and flow). Only the reads it makes to find what to write are
   in the change's view (`TagVersion` at version 0 tags the version the view holds).

## Consequences

- Each screen, agent and tool sees the same thing for the same view. "Main" is never assumed.
- A client that sent no scope now gets "no view". Every caller in the repository is migrated with this ADR: the web
  transport fills the view from the context, and graphclient, the MCP connectors and the engine pass theirs.
- The index needs the change overlay. Until it has it, a change view searches its fork and adds the drafts read
  through `ViewOf`, which is what the web does today.
- History stays version-based, and it is the only place where versions are chosen.

## Steps

1. `graphapi.View`, proto `View`, and `Graph.ViewOf` with the reader. Its tests run against every view kind on
   memory, SQLite and PostgreSQL, comparing it with `ChangeWorld` and `BaselineGraph`.
2. RPCs: `GetNode`, `GetNodeNeighbourhood`, `ListBaselineNodes` / `ListBaselineLinks` (renamed `SearchNodes` /
   `SearchLinks`, view-scoped), `GetBlackboard`, `GetChangeGraph`, `GetGraphStats`, `ExportGraph`; a missing view is
   refused. Then graphclient and the web transport (`contextView.ts` builds the `View`).
3. Index `Search` by view.
4. Engine port `Read`, the `goap-graph` and `goap-change` tools.
5. Remove the latest-on-main defaults (`GetNode` by key, `TagVersion` at version 0, `NodesOfType`).
