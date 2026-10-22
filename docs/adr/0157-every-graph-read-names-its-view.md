# ADR 0157 — Every graph read names its view

**Status**: accepted; implemented · **Date**: 2026-10

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
   - `{namespace, branch, baseline_id}`: a lasting branch at one of its baselines. The namespace and the branch are
     both required: no branch is assumed, the caller (the front) names main when it means main. An empty baseline
     means the head of the branch when the read starts, the only default. A baseline is read on its own branch only
     (one of another branch is refused). The answer names the baseline it read, so the next read can pin it.
   - `{change_id, flow}`: a change on an option (`main` or an option id). It resolves to the change's branch and its
     pending work: its fork, its commits and the drafts of that flow (`ChangeWorld`, ADR 0140). The namespace is the
     change's.

   The web builds the view from the header (`contextView.ts`):
   - the working change on the option selected there, for the namespace it acts on;
   - for every other namespace, and when no change is selected, the head of main.

2. **Every read of the current graph takes a view, and refuses a request without one** (`InvalidArgument`, "no view"):
   a view names a branch (namespace and name) or a change, never neither.
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

## Implementation

- `graphapi.View` (`Check`, `ErrNoView`), proto `goap.graph.v1.View`, `pbconv.GraphViewFromPB` / `GraphViewToPB`.
- `graph/graph/view.go`: `openWorld` and the `world` reader; `Graph.ViewNode`, `ViewNeighbourhood`, `ViewNodes`.
  `NodeNeighbourhood` and `ChangeNodeNeighbourhood` delegate to it. A page of a change view merges the page of its fork
  (the nodes it changed left out) with the nodes it wrote; `NodeQuery.Change` there is the nodes its impacts act on as
  the fork has them.
- The router and the personal scope read the change and the namespace of a request's `view` too.
- Tests: `view_test.go` (`TestBranchView`), `checkChangeHoods` / `checkViewPages` in `changeview_test.go`, on every
  repository.

- Index (step 3): `SearchRequest.views` (one per namespace) or `history` (every version, refined by `main`, which
  alone is refused); a search of nodes naming neither is refused, a search of changes needs none. The namespaces are
  the views'; when every view is the head of main the index's own flag selects, else `index.Query.Select` keeps a
  hit whose version is the one its view holds (`Graph.ViewVersions`, RPC `ResolveView`, `graphclient.ViewVersions`;
  port `indexersvc.Views`). The drafts of a change view, which the index has no document of, come from the graph
  (`Graph.ViewDrafts`, `index.Query.Extra`). Web: the search overlay searches *In the context* (the views of the
  context per namespace) or the history.

- Engine and tools (step 4): `graphapi.Port` reads views itself, `ViewNode` / `ViewNeighbourhood` / `ViewNodes`
  (the browse shapes `NodeQuery`, `NodePage`, `Neighbourhood` moved to `contracts/graphapi`, aliases in the graph;
  `graphclient` over `GetNode`, `GetNodeNeighbourhood`, `ListBaselineNodes`), instead of one `Read(view)` object.
  A tool call carries the flow of its process (`mcp.CallContext.Flow`, `InvokeRequest.flow`, `CallToolRequest.flow`,
  `engine.ProcessRef.Flow`). The `goap-graph` tools take `change`, `flow`, `branch` and `baseline`: the change named,
  else the calling process's when it acts on the namespace, else the branch (default main) at the baseline (default
  its head); `glob` / `grep` page the view with a text filter taken from the pattern (`maxScan` nodes at most, then
  `truncated`). `goap-change/tag` reads a branch. The engine reads the process's view (`viewOf`): the DSL host
  (`ctx.node`, `ctx.nodes`, `ctx.links`), `graph.propagate` (a neighbourhood per node it reaches) and the prompt
  templates (`.Baseline.Nodes`, read page by page only when a template asks, `PromptGraph`); the reads of the fork
  (the pre version of an impact, the nodes a form names, `goap-change` `base`) go by key through
  `graphapi.ForkNode`. `readGraph` is gone. Whole reads left, explicit: the self-improvement observer (the
  methodology namespace) and `goap-admin`'s listings of the organisation.

- Defaults removed (step 5): `Graph.NodesOfType` is gone; `Graph.ViewNodesOfType(view, type)` reads a whole type
  in a view in one read of the state (port `ViewNodesOfType`, RPC `ListViewNodes`; paging it cost a read of the state
  per page: 3.5 s for 8421 nodes on SQLite, 0.29 s in one read, `BenchmarkViewReads`) (the sources of `Graph.Lnk`, the units of the sign-in bookkeeping, a script's `ctx.nodes`, the prompt templates; the
  first-user check counts a page of one);
  validators keep the `ValidatorQuery` of the commit they judge. `TagVersion` at version 0 needs a `view`
  (`TagVersionRequest.view`) and tags the version it holds. `Graph.NodeByKey` stays for identity checks of the seeds
  (does a key exist), never to read a node's content.

## Steps

1. `graphapi.View`, proto `View`, and `Graph.ViewOf` with the reader. Its tests run against every view kind on
   memory, SQLite and PostgreSQL, comparing it with `ChangeWorld` and `BaselineGraph`.
2. RPCs: `GetNode` (by key or at version 0), `GetNodeNeighbourhood` and `ListBaselineNodes` take `view` and refuse a
   missing one; `GetBlackboard`, `GetChangeGraph`, `GetGraphStats` and `ExportGraph` already name a change or a branch.
   Then graphclient and the web transport (`contextView.ts` builds the `View`, `graphIndex.ts` `branchView` /
   `forkView`). Done; the RPCs kept their names.
3. Index `Search` by view.
4. Engine port `Read`, the `goap-graph` and `goap-change` tools.
5. Remove the latest-on-main defaults (`GetNode` by key, `TagVersion` at version 0, `NodesOfType`).
