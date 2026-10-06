# ADR 0079 — Drafts and versions at landing: a node has no version while a change works on it

**Status**: accepted, implemented (graph, services, engine, DSL, connectors, web) · **Date**: 2026-10 · Supersedes ADR
0076's storage of the working version in `node_version` (`checked_out`, `Tx.FreezeVersion`, `Tx.SetNodeState`,
`SetNodeProps`, `DropWorkingVersion`, in-place edits of links) and the version-writing parts of ADR 0077 ("A transition on a
working version is taken in place", "A user is one version" as a storage detail) and ADR 0025 §5 (flow branches that carry
versions); refines ADR 0029 / 0030 (event-sourced impacts, one log).

## Context

ADR 0076 made every node write a change operation and gave a node a **working version**: a row of `node_version` flagged
`checked_out`, edited in place until the change lands, which freezes it (ADR 0077). It worked, but the model had three
costs.

- A version row existed for something that is not a version: a version is the state of a node that a change *landed*, it
  has a place in the history and is immutable. The working version was neither, so the storage needed a flag, an
  exception to immutability in the guard (`editable`, `FreezeVersion`, `DropWorkingVersion`, `SetNodeState`,
  `SetLinkProps`, `DeleteLink`), a retargeting of the links other working versions held (`followVersion`), and a
  version number consumed by a change that might be cancelled.
- A transition from a frozen version wrote a version of its own, so one change produced several versions of a node and
  the guard of a transition could only see the node as stored.
- Flows (options, relaunched steps) forked *branches* of versions of a node for what is a private working state.

The decision of the user: the backend creates a node version **only when the change lands**. During the change the node
is checked out once (per change and flow) and stays checked out until landing; the change log tracks every modification;
the guard of a transition sees the change, the impact and the draft.

## Decision

### 1. The draft

During a change a node has **no `node_version` row**. Its working state in the change is a `domain.Draft`, one per
change impact and flow: the node id (allocated when the draft is created, stable), its key and type, its state, its
owner unit, its properties, its origins (merge and split), its `Base` (the version it was checked out from; none for a
creation), the run (`Execution`) that checked it out and its outgoing links. A link goes to an exact node version or, with
`Version` 0, to the draft of another node of the change.

`NodeRef{ID, Version: 0}` is the **draft reference** (`NodeRef.IsDraft()`, `domain.DraftRef`). The post of a change impact is
the draft reference while the change works on the node (`ChangeImpact.Drafted()`); once the change landed it is the
version written. Outside a change a reference with version 0 keeps its meaning (the latest version on main): a draft
reference is read through its change (`Graph.ChangeNodeView` / `ChangeNode` / `ChangeNodeViewByKey`, the RPC `GetNode` with
`change_id` and `flow`, the blackboard, `ChangeView`, `ChangeGraph`, `CompareOptions`). A draft read as a `Node` has
`Version` 0 and `Branch` = the branch its flow would write (`graph.IsWorking` keeps its meaning: the draft is the flow's
own). The proto `Node` loses `checked_out` for `draft`.

### 2. The draft is derived from the log, and cached

There is **no draft table** (a `change_draft` projection was written first and dropped: user decision). The change log
(ADR 0029, 0030) is the truth, and a second projection beside `change_impact` would be a second copy to keep equal to it
(and to a landing, a rollback, a replay). A draft is **the fold of the impact events** of its change
(`domain.FoldDrafts`: `ApplyImpactEvent` then `ApplyDraftEvent`, one event at a time; `DraftSeenBy` for the reads that
honour stale runs). The events carry what is needed:

- `created` and `checkedOut`: the initial state, `ImpactEvent.Draft` (state, owner, properties, origins, base, links with
  their ids); `Post` is the draft reference. They are **snapshots**: the fold of a draft starts at the latest one;
- `updated`: the patch (`props`, `unset`, `owner` / `ownerId`, `addLink` {id, type, to, toId, toVersion, props},
  `updateLink`, `removeLink`); `transitioned`: `{"state": {"from", "to"}}` and the properties the transition's actions set;
- `cancelled` drops the draft of its flow, `withdrawn` and `landed` drop the drafts of the impact, `adopted` drops the
  flow's and the ones of the impacts it supersedes.

Reads go through `Graph.drafts` (`pkg/graph/draftcache.go`). The graph keeps, per change, the fold up to a log position:
the drafts and the change impacts they depend on (a superseded impact loses its drafts), stamped with the `seq` of the
last event folded. A read asks the log for the impact events **after** that `seq` (an indexed `Tx.Log` with `AfterSeq`,
empty when nothing happened) and folds only those on top of the cached state: a read after one edit folds one event, not
the whole log. The cache is an LRU of `Graph.DraftCacheSize` changes (`DefaultDraftCacheSize`, 128) behind a mutex; its
entries are immutable and what a read returns is a deep copy.

Why it stays correct:

- **The log is the truth, the cache a validated copy.** An entry is only ever "the log up to `seq`"; every read catches up
  from the shared log, so a second graph process, or an entry that fell behind, converges on the next read.
- **Nothing uncommitted is published.** A transaction that appended to a change's log (the operations that write an event
  and then read the draft) folds the cached prefix and its own newer events directly, so it reads its own writes, and
  publishes nothing: a rollback leaves no trace. Only a transaction that wrote nothing to that change's log publishes
  what it read, which is committed. (`guardTx` records the changes it appended to; a transaction that cannot say is
  taken to have written.)
- Writers of one change are assumed serialized (the `change_impact` projection already needed it): with seqs handed out
  in commit order, "up to `seq`" is a stable prefix. A purged change drops its entry.

`checkImpactLogs`, which every graph test runs on both repositories, compares the drafts a cold graph reads with a
from-scratch `FoldDrafts`; `draftcache_test.go` covers the incremental fold, a rolled-back write, two graphs on one store
and the copies.

### 3. Operations

- `ImpactNodeCreate` and `ImpactNodeCheckout` give the node its draft, **once per change and flow**. A second checkout on
  the flow is `ErrConflict` ("already checked out"). A checkout copies what the flow sees: the stored version for a
  node no flow holds, else the **draft of the parent flow** (the nearest, a new copy of its links); a flow's edits and its
  parent's after the fork do not reach each other, and the parent's draft is the parent's to edit.
- `ImpactNodeUpdate`, `ImpactLinkCreate` / `Update` / `Delete` edit the draft of the flow (`updated` events). A link is
  deleted by the id of a draft link, or by the id of the stored link of the version the draft was checked out from. A link
  to the version a node of the change was checked out from, or to its draft reference, is a link to that node's draft.
- `ImpactNodeTransition` sets the state of the draft; a node with no draft is **checked out first** (`checkedOut` then
  `transitioned`, the review kept: a move is not an edit). The permission (`Graph.Authorizer`) is asked before the
  transaction; the guard sees `node` (the node in the state it goes to), `children`, `change` (id, title, intent,
  methodology, goal, state, status), `impact` and `draft` (`key`, `type`, `state` it goes to, `props`, `owner`, `links`,
  `base` and `new`); the guard and action algorithms (`dsl.AlgorithmInput`) run on the draft, and the properties an action
  sets are part of the `transitioned` event.
- `ImpactNodeCancel` drops the draft of the flow (the impact goes back to the parent flow's draft, else to none; a creation
  cancelled removes its impact: no node was ever written). `WithdrawImpact` takes the impact and its drafts out; it is no
  longer refused once a version of it exists, for none does until landing.
- Review is unchanged except that an accepted review checks the **draft** (`checkDraft`: attributes, validators, required
  links, link attributes; the origins gate) and writes only the `reviewed` event. An edit or a link edit after an
  acceptance sends the review back to `proposed`; a transition does not.
- **Reviews are always explicit** (user decision, with this ADR): no edit, no transition, no link edit reviews. The web
  edit flow, the engine and the DSL writes, the `goap-change` tools no longer accept after writing: the impact stays
  `proposed` until someone runs `ImpactNodeReview`, and the audit trail of an edit reads `proposed`, `checkedOut`,
  `updated`, `updated`, without a `reviewed` line. The bulk system paths — `Graph.Commit` / `CommitEdits`, the seeds,
  `EnsureUser`, `graphtest` — keep reviewing as the system principal: they are producers of a complete state, not edits (and
  the settings dialog's Save of a personal change, ADR 0037, is an explicit user action: accept and apply).

### 4. Landing writes the versions

`CommitChange` (so `Apply`; `prepareChangeImpacts`) validates every draft again and **writes the node versions from the
drafts**, in the commit transaction, on the change's own branch: version number = the next version of the node; `Parents` =
the version the draft was checked out from; `Reason` `create` / `revise` / `derive`; owner, origins, comment (the last
review comment, else the rationale), state and properties of the draft; `Project` and the owner stamping by the guard;
then the outgoing links, with their targets resolved to exact versions: a draft reference, or the version a draft was
checked out from, of a node the change lands, becomes the version this landing writes for it (what `retarget` and
`followVersion` did, now one rule); a link to the draft of a node that does not land is an error. A node that changed on the
change's branch since the draft was checked out (a sub-change merged a version) is a conflict (ADR 0081: a sub-change no
longer merges a version, it lands in its parent's log). The `landed` events then
name the versions (the impact's post becomes the landed ref, `ImpactEvent` `landed` drops the drafts), the node index events
are published for them. Branches, `IntegrateChange` (fast-forward, 3-way merge when a node changed on both sides,
`Tx.JoinBranch`), baselines and `Apply` = commit + integrate are unchanged: **the commit step is where versions appear on
the change's branch**. A landing conflict (the node moved on main meanwhile) merges or waits as before.

A draft equal to the version it was checked out from still lands a version of its own: the reviewer accepted a checkout, the
graph does not compare content (simple, and a version carries its origin comment). A creation's key is only reserved at
landing (the node row is written then): two changes creating the same key conflict when the second lands.

The guard keeps one rule: **a persisted version is immutable**. `Tx` has no edit, freeze or drop of a version
(`SetNodeProps`, `SetNodeOwner`, `SetNodeState`, `FreezeVersion`, `DropWorkingVersion`, `DeleteLink`, `SetLinkProps` are
gone, `node_version.checked_out` too); `PutLink` is accepted for a version written in the same transaction only.
`change_impact.node_id` loses its foreign key (a creation has no node row during the change).

### 5. Flows and options

A flow keeps **no branch of versions** any more (`ensureFlowBranch` is gone; `flowBranchName` only names the branch of a
flow's drafts for `IsWorking`). A flow's drafts are its own drafts; what a flow sees is the draft of the innermost flow of its
chain that holds one (`seenDrafts`, `DraftSeenBy`), the main flow last, the events of the runs that are stale on the chain
left out (ADR 0025). Adopting a flow (`adoptNodes`) installs its drafts on the main flow, by `transitioned` events carrying
the `Draft` and `{"adopted": flow}`: a node whose main draft changed since the flow forked, by a run that is not stale, is a
conflict; the main drafts the stale runs wrote are reset to what the other runs left (or dropped), and the impacts the flow
supersedes lose their drafts. A flow's draft must be accepted on that flow to be adopted. Selecting an option is adopting its
flow; rejecting it discards it (its drafts stay in the log, nothing sees them).

### 6. Phases, merge and split, the log

The lifecycle phases of ADR 0058 follow the events that start a draft (`ImpactEvent.StartsDraft`: created, checkedOut, an
adoption's install); every transition is now an event, so `walkTransitions` replays the recorded moves of the draft from
the state it started in and checks each is a move of the lifecycle and that the chain ends in the state of the version.
Merge and split (ADR 0077) work on drafts: the parent's draft loses the links to the sources and gains the links to the
successors, whose drafts carry the origins; the origins gate is checked on the drafts at acceptance and at landing, the
`created` event keeps `patch.origins`. The PROV-O export maps a draft to a `goap:NodeDraft` entity (the version written at
landing is derived from it).

## Consequences

- A node has exactly one version per change that landed it: no version is consumed by a cancelled checkout, and the history
  reads as the history of what was decided.
- The change log tells every modification of a node: created / checkedOut, updated, transitioned, then landed.
- Reads inside a change go through the draft; reads outside it see the last landed version, as before.
- `domain.Node.CheckedOut`, `ImpactEvent.WritesVersion`, the proto `checked_out`, the DSL `ChangeImpact.checkedOut` (now
  `drafted`) and the web logic that tested `checkedOut` are gone: the draft state of the impact says it.
- Greenfield: no migration; development databases are reset (the initial schema has no `checked_out` column and no draft table).
- Not done: a comparison of a draft with its base to skip an unchanged version; the draft of a change that is committed is
  gone with its landing (the audit reads the log).
