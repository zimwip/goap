# ADR 0098 — The change is a receptacle, the blackboard is the engine's view of it, requests are the origin of work

**Status**: accepted; phases 1 to 4 implemented, 5 and 6 begun (see Implementation) · **Date**: 2026-10 · Builds on ADR 0001 (the change as blackboard), 0024 / 0029 / 0030 (change
impacts, event-sourced, one log), 0027 (node editors), 0031 (deferred change binding), 0055 (attributes), 0058 (change
lifecycle and gates), 0065 / 0066 / 0067 (item kinds, facets, flow origin and decision policy out of the core), 0079
(drafts and versions at landing), 0096 (goal of a change). Partly supersedes ADR 0033 §1 (the request as an intake
process) and moves what ADR 0058, 0065, 0066, 0067 and 0096 placed in `pkg/graph` out of it.

## Context

The mental model has three independent parts that only meet at run time: the **graph** (what is changed, operable by
hand through changes), the **methodology** (how a change is performed on a domain) and the **engine** (agents applying
the methodology, with formal or LLM planning). The code keeps the graph free of the engine's packages (`pkg/layering`),
but the change, which lives in `pkg/graph`, still speaks the language of the methodology and of execution:

| Concern | Where it is today |
|---|---|
| Methodology, goal, lifecycle, state of a change | columns of `change` (`methodology`, `goal`, `lifecycle`, `state`), `domain.Change` fields |
| Default goal, lifecycle, CEL guards and gates of a change | `graph.ChangeLifecycles` (`DefaultGoal`, `Lifecycle`, `Guard`, `Gate`), implemented by the registry, called by `CreateChange` / `TransitionChange` |
| Landing floor and sub-change cascade | `Graph.LandingGate`, `Graph.SubChangeValidator` |
| Facts and their meaning | item kinds `decision`, `artifact`, `signal`, `transition`, `flow` and the registered ones (`risk`, `verification`, `review`...); statuses `candidate`, `stale`, `superseded` (the relaunch rule of ADR 0025) |
| Options, decision points | `pkg/graph/option.go`, `Graph.OpenDecision` / `RuleDecision`..., `Graph.DecisionPolicy`, facets |
| Execution identifiers | `change_log.process_id`, `execution`; `Flow.StaleRuns`, `Flow.Origin` |
| The blackboard | `domain.Blackboard`, built by `Graph.Blackboard` / `BlackboardIn`, `ValidateBoard`, `Graph.Facets` |
| What a change shows | fixed tabs and panes of the web change view, each knowing an item kind |

As a result the engine depends on the `pkg/graph` package itself (`engine.GraphPort` takes `graph.NewChange`,
`graph.NodeCreate`... and has some 45 methods), the graph API mixes the versioned graph with execution semantics
(about 90 RPCs in `graph.v1`), a change cannot exist, or be reasoned about, without the vocabulary of a methodology, and
every use case that adds something to a change (risks, verifications, derogations, reviews) adds code to the core, an
item kind registered in Go and a pane in the web.

The origin of the work is missing altogether. `Change.intent` is a free text, the requester is not recorded, a request
exists only once a change has been created for it (the assistant's `create_change`, `sdlc`'s `find_or_create_change`,
eager binding leaving orphan drafts, ADR 0033), and a change answering several requests, or a request split over
several changes, cannot be expressed.

## Decision

### 1. Four layers, each ignorant of the ones above

```
 Engine      runs, planners, agents; the BLACKBOARD is its execution view of a change
   │         knows: Change, Methodology (compiled), Graph (reads), Domain
 Change      the core: impacts (pre / post) and their review; the container of typed change objects; REQUESTS
   │         knows: Graph, Domain
 Graph       node versions, baselines, branches, tags
             knows: Domain
 Domain      node / link types, CHANGE OBJECT TYPES, attributes, lifecycles, algorithms (registry)
```

The methodology (registry) is consumed by the engine only. A person can act at two levels: on the **change** directly
(edit nodes, review, land, no methodology needed) or through the **engine** (advance a change governed by a
methodology).

### 2. The change is reduced to impacts and their review

The change keeps only what makes a change of the graph:

- **the impacts**, each a `pre` (the version read or checked out) and a `post` (the draft, then the version landed),
  with the whole mechanism of ADR 0076–0082: creation, checkout, edits, node transitions (node lifecycles come from the
  domain), merge and split, cancel and withdraw, rebase of sub-changes, commit and integration, still event-sourced
  through `emit` on the reserved `impact.*` stream (ADR 0029);
- **the review of the impacts**: explicit reviews, the gate of what a version must satisfy (`checkDraft`), the batch
  submission of ADR 0080, and `ReviewPolicy` (ADR 0075: the verifier is not the producer) as a generic seam;
- **workspaces**: the layers the drafts live in (today's flows stripped of their meaning: a parent, a fork point,
  adopted or discarded). Whether a workspace is an option or the relaunch of a step is the methodology's (§5);
- a **header** for identity, scope and storage: `id, title, intent, namespace, owner_org, project_id, parent_id,
  baseline_id, branch, result_baseline_id, status, guardian, created_at`. `status` is the storage lifecycle (`draft`,
  `active`, `committed`, `applied`, `abandoned`); `intent` the working formulation, editable (the words of whoever asked
  are in the requests, §7); `guardian` an opaque name (§4).

`methodology`, `goal`, `lifecycle`, `state` and the free-form `data` leave the header. **Everything else a change carries
is a change object**, typed by a domain (§3); every published change object type is available on every change, by
hand or by an agent, as node types are. The CEL the change evaluates is the domain's
only (node lifecycle guards, validators); it never evaluates a methodology's conditions.

`Submit(change, Batch)` applies change object writes and impact operations in one transaction, all or none: an action's
outputs are written together at the end of the action, and a run can be replayed safely (ADR 0031).

### 3. Change objects: types declared by domains, available by hand, added by methodologies

#### Change object types are domain definitions

A domain declares, next to its node and link types, **change object types**: the shape of what can be added to a change.
They reuse the definition machinery of node types (attributes, enums, `property_validator` instances, ADR 0055;
lifecycles, ADR 0014; `search`, ADR 0026; `editor`, ADR 0027) and are referenced qualified, `<namespace>@<ChangeObjectType>`.

```yaml
changeObjectTypes:
  - name: Risk
    description: A risk of the change, scored and followed until it is under control
    key: {kind: sequence, prefix: RISK}        # identity: RISK-1, RISK-2... allocated per change
    scope: change                              # change | workspace
    lifecycle: risk                            # open -> mitigated -> closed
    editor: risk-register                      # the tab that shows them (§6)
    attributes:
      - {code: title, type: string, required: true}
      - {code: probability, type: number}
      - {code: impact, type: number}
      - {code: owner, type: string}
```

#### The key type gives a change object its identity

A change object is a key and a value; the **key type** of its change object type says how the key is made and what is unique:

| `key.kind` | Identity | Example |
|---|---|---|
| `singleton` | one change object of the type per change (per workspace when `scope: workspace`) | the lifecycle state of the change |
| `sequence` | allocated by the change at the first write, `<prefix>-<n>`, never reused | `RISK-3`, `ACT-12` |
| `natural` | the values of the attributes named by `key.attributes`, joined | a decision point per `question`, a verification per `impact` |
| `ref` | a reference to another object: `key.ref` = `impact` \| `node` \| `request` \| `run` \| `<ns>@<ChangeObjectType>` | one review entry per impact, one run reference per run |

Every write of a key is a new version of the change object (the history is the log); the last value is the change object. A `ref` key
is checked against the change (the impact, request, run or change object must exist); a `natural` key is recomputed and must
not change across versions.

#### The change validates change objects, without knowing why they are there

The change knows the domain (the layer below it), so it validates every change object written against the type catalogue:
the type exists, the key matches its key type, the value its attributes (strict, as nodes), the state its lifecycle.
It does not know which methodology added the type nor what the change object means. Change objects are entries of `change_log`
(type `object.<ns>@<ChangeObjectType>`), whose `flow`, `process_id` and `execution` columns are replaced by `workspace` and
`labels` (opaque indexed labels: process, execution, step); a projection `change_object (change_id, type, key, workspace,
seq, value)` keeps the last version of each key, for fast reads and for filtering changes on a change object without knowing
its meaning. Writing a change object type is the ABAC permission `change-object:write` on the resource `change-object:<ns>@<ChangeObjectType>`.

#### A methodology declares the change objects it works with

```yaml
additions:
  tabs:
    - {title: Risks, editor: risk-register, objects: [risks@Risk, risks@Action]}
    - {title: Decisions, editor: decision-board, objects: [decisions@DecisionPoint]}
```

There is no activation: like node types, every change object type of a published domain is available on every change,
with or without a methodology, and a person adds, edits and moves change objects by hand through the change API, like
impacts, within the ABAC permission of their type. A methodology names the types it works with where it uses them (its
conditions, its actions, its `expects`, the tabs above), checked at save and publish as its node type references are;
the guardian may refuse to land a change whose change objects break the rules of its methodology (§4). The use cases of today
(`pkg/risk`, `pkg/verify`, `pkg/review`, `pkg/criticality`, decision points, options, the lifecycle state) become change object
types of built-in or library domains, and `domain.RegisterItemKind` disappears: a new use case is a domain addition and,
if needed, an editor, never a change to the core.

#### Sub-changes

A sub-change follows its **own** methodology: the tabs, conditions and rules that apply to its change objects are
those of its methodology, never its parent's. When it is integrated (ADR 0081), its change objects are installed in the
parent's main workspace with its drafts.

### 4. The guardian

When a change is governed by a methodology, a manual operation on the change must not skip its gates. The change keeps
one generic port and does not know what is checked:

```go
type Guardian interface {
    // decided replaces the floor of the landable states (ADR 0078) by ok; not decided leaves it in force
    MayCommit(ctx context.Context, c Change, bb Blackboard) (decided, ok bool, err error)
    MayCreateChild(ctx context.Context, parent, child Change) error
    MayMove(ctx context.Context, family []Change, to string) error
}
```

- The change asks the guardian named in its header; an unknown or unreachable guardian refuses (fail closed).
- A change with no guardian is free (manual editing, seeds, administration).
- The engine is the guardian `"engine"`: it sets the name in the same `Submit` as the first methodology change object of the
  change, and implements the port with the landing floor and gates of ADR 0058 / 0078, the sub-activity cascade
  (`SubChangeValidator`), the project move rules (`ProjectMoveGate`, ADR 0091) and the change object types a methodology
  requires (a request, a closed risk register...).
- `Graph.LandingGate` and `Graph.SubChangeValidator` are replaced by it; `Graph.ProjectMoveGate` keeps the
  authorization of a move (who may move it, on both projects), the guardian judges what governs the change.

### 5. The blackboard is the engine's execution view of a change

The blackboard is not an object of its own: it is the change, read and written with the semantics of its
methodologies. Its identity is the change's id, it stores nothing elsewhere, and the dependency goes one way: the
blackboard knows the change, the change knows nothing of the blackboard. It lives in the engine component
(`pkg/engine/blackboard`), stateless over the change, cached per change and validated by the last `seq` of the log (as the
drafts cache, `pkg/graph/draftcache.go`).

The engine's own change objects are of the types of a built-in domain `execution`:

| Change object type (`execution@...`) | Key | Replaces |
|---|---|---|
| `Methodology` | `natural` (name) = `{version, role: primary\|companion, goal}`, one change object per methodology | `Change.Methodology`, `Change.Goal`, `ChangeLifecycles.DefaultGoal` (ADR 0096), the companions of ADR 0036 |
| `State` | `singleton` = `{lifecycle, state}`; `Transition`, `sequence` | `Change.Lifecycle` / `State`, `KindTransition`, `TransitionChange` (ADR 0058) |
| `Fact` | `sequence` (kind, status, payload) | items `decision`, `artifact`, `signal`, `flow`, `merge` |
| `Option` | `ref` (workspace) | options (ADR 0032 §6), the stale runs and origin of a relaunch (ADR 0067) |
| `Run` | `ref` (run) = `{methodology, agent, goal, status, startedAt}` | the link from a change to its runs (§6) |
| `Journal`, `ModelCall` | `sequence` | the journal (ADR 0011) and the prompts (ADR 0059) |

Decision points (`decisions@DecisionPoint`, the `DecisionPolicy` of ADR 0067), risks, verifications and reviews are
change object types of library domains a methodology adds (§3).

- `Observe(change, workspace, at)` gives the world of a run: the change header, its impacts and drafts, its change objects,
  the hydrated nodes and the conditions of its methodologies evaluated. `pkg/condition` (the CEL environment) belongs to
  the engine side, and CEL sees change objects by type (`objects["risks@Risk"]`); `Explain` is the existing `ExplainCondition`.
- The web and the assistant go through the engine API for the blackboard (transitions of a change, decisions,
  options, change objects a methodology governs); the change API stays the way to edit and review nodes by hand.

### 6. Runs are the engine's, referenced by the change

The engine stays stateful: a run (`Process`) is stored by the engine. The change references each run working on it by
an `execution@Run` change object, kept up to date by the engine: a pointer, not a copy. A run may also reference a request (§7)
before any change exists, which is what deferred binding (ADR 0031) becomes.

### 7. Requests: the origin of work

A **request** is the origin of a piece of work: who asks for what, from where, when. It is an object of the change
component, not of the graph, and it may exist before any change. Every entry of work becomes one: an assistant message,
an external ticket, a trigger event, a need found while working on another change, a manual entry.

```
request:        id, title, text, requester, project_id (nullable until triage), origin_kind, origin_ref,
                status, created_at
change_request: change_id, request_id, role, linked_by, linked_at
request_log:    seq, request_id, type, by_whom, at, payload
```

- `text` is the voice of the requester and never rewritten; `origin` is `{kind: conversation | trigger | external |
  change | manual, ref}`.
- **Many to many, typed links**: `origin` (the change was created for it), `amends` (a later request changing the intent
  of a change in progress), `covers` (the change answers it, wholly or in part). A request can be split over several
  changes, and a change can consolidate several requests (duplicates).
- **Status**, with no method: `open` → `triaged` (project known and at least one link) → `delivered` → `closed`, or
  `rejected` / `withdrawn`. `delivered` is derived (every linked change `applied`); `closed` is explicit (the requester, or
  a triager, confirms the need is met): delivering is not satisfying.
- **Triage**: a new platform role `triage` (`access.BuiltinRoles`, ADR 0046 / 0047) reads and triages the requests with
  no project yet; once a request has a project, the roles of the project apply.
- **Project**: a change linked to a request is in the request's project; linking an untriaged request sets its project
  to the change's; moving a change (ADR 0091) moves the requests linked to it alone, and is refused when one is also
  linked to a change staying behind.
- **Sub-changes**: the links stay on the parent change; a sub-change is a part of its parent's answer (ADR 0081).
- **A change needs no request**, personal changes (ADR 0037) included: the storage does not require one (seeds,
  administration, quick manual edits). A methodology may require one; its guardian checks it.
- The links and status moves are entries of `request_log` and, for each linked change, of its log
  (`change.request.linked` / `unlinked`).
- Requests are indexed (ADR 0095, a third document kind) so that duplicates and the change a request belongs to can be
  found.
- Paths that change: the assistant's `create_change` creates a request then proposes a change linked to it; `sdlc`'s
  `find_or_create_change` links the request to an existing or a new change; a trigger creates a request with
  `origin: trigger`; nothing creates a change before a request is linked to it, so the orphans of ADR 0033 disappear.

### 8. The change view in the web is declared

The change view keeps the panes of the core: the header, the impacts (`pre` / `post`, drafts, diff) and their review,
the requests, the log. Every other tab is **declared**, in editor mode, as node types name their editor (ADR 0027): one
tab per change object type the change holds change objects of (the `editor` of the type), and the tabs the
methodologies of the change declare (`additions.tabs`, grouping the types they work with, shown even while empty); an
"Add" menu offers every change object type of the published domains:

- a tab names an `editor` and the change object types it shows; the web opens it through one entry point
  (`openChangeTab`) and the editors register themselves (`registerChangeTab(name, component)`, next to
  `registerNodeEditor`);
- an editor unknown to the web, or none, gives the **default object editor**: a table of the change objects of each type and
  a form generated from their attributes (`attributes.ts`, `AttributeEditor.svelte`), with the transitions of their
  lifecycle;
- the existing panes that show a use case (decisions, options, risks, review) become registered editors of their change object
  types;
- a change with no methodology shows the core panes and a tab per type of the change objects it holds.

### 9. APIs

**`change.v1`** (split out of `graph.v1`, which keeps the reads of nodes, baselines, branches and tags):

```go
type Changes interface {
    Create(ctx, NewChange) (Change, error)                 // no methodology, no goal
    Get, List(ctx, ChangeFilter{..., Objects, Request}), Update, Move, Purge

    ImpactNodeCreate, Checkout, Update, Transition, Cancel, Merge, Split, ImpactLink*, Withdraw
    Review, ReviewBatch, Rebase, Resolve
    View(ctx, id, workspace, level) (Baseline, error)
    OpenWorkspace, AdoptWorkspace, DiscardWorkspace

    PutObjects(ctx, id, []ObjectWrite) ([]ChangeObject, error)  // validated against the change object types
    Objects(ctx, id, ObjectFilter{Types, KeyPrefix, Workspace, Labels, AtSeq}) ([]ChangeObject, error) // last version per key
    Log(ctx, id, LogFilter) ([]Entry, error)

    Submit(ctx, id, Batch) (BatchResult, error)            // change objects + impact operations, one transaction
    Commit, Integrate, Apply
}

type Requests interface {
    Create(ctx, NewRequest) (Request, error)
    Get, List(ctx, RequestFilter{Requester, Project, Status, Change}), Update(title), Withdraw, Reject, Close
    Link(ctx, request, change, role) error
    Unlink(ctx, request, change) error
    Log(ctx, request) ([]RequestEntry, error)
}
```

**`registry.v1`**: change object types are part of a domain (`ListTypes` returns them with their key type, attributes,
lifecycle and editor); a methodology's `additions` are checked at save and publish (the change object types resolve, the
editors are names).

**`engine.v1`** keeps the runs (`StartProcess`, which can take a `request` instead of a change, `SubmitHumanInput`,
`ApproveAction`, `UnblockProcess`, `GetProcess`, `GetProcessProgress`, `ListStartingPoints`...) and gains the
blackboard: `Observe`, `ExplainCondition`, `PutObjects`,
`ListTransitions` / `Transition`, options, decision points, and `DeclareMethodology` (writes `execution@Methodology` and sets the guardian, in one `Submit`).

**Ports of the engine**: `engine.GraphPort` is split into `ChangePort` (the subset of `Changes` and `Requests` it uses)
and `GraphReadPort` (baselines, node reads, structures), both over neutral types in `pkg/domain` (or a contract package),
never `pkg/graph`'s; `MethodologyPort`, `Scope` and the model client are unchanged.

### 10. Layering rules

`pkg/layering` gains: the change (`pkg/change`, today the change half of `pkg/graph`) imports neither `pkg/engine`,
`pkg/methodology` nor `pkg/condition`, and names no change object type; `pkg/engine` does not import `pkg/graph` or
`pkg/change` (only their ports' types); the registry does not import `pkg/engine` (`PreviewPlan` moves to a planning
package both use); the use-case packages (`pkg/risk`, `pkg/verify`, `pkg/review`, `pkg/criticality`) are imported by no
core package and shrink to the algorithms of their change object types.

## Migration

Each phase keeps the suites green and the platform usable.

1. **Change object types**: in `pkg/domain/def` and `pkg/typecat` (key types, scope, editor), in the registry and `ListTypes`;
   the built-in domain `execution`.
2. **Change objects**: `PutObjects` / `Objects`, the `change_object` projection, `workspace` + `labels` on `change_log` (both
   dialects, `TestSchemasAligned`), the `change-object:write` permission; `Submit`.
3. **Guardian**: the port in the change, implemented first over the current registry seams (`LandingGate`,
   `SubChangeValidator`, `ProjectMoveGate`), then moved to the engine.
4. **Requests**: tables, `Requests` API, `request_log`, the `triage` role, index kind, assistant and trigger paths;
   `sdlc` intake on requests.
5. **Blackboard in the engine**: `pkg/engine/blackboard`; the methodology, goal, lifecycle and state columns become
   `execution@` change objects (a data migration writes them for existing changes); items, options, decision points, risks,
   verifications and reviews become change objects of their types; methodologies declare their tabs (`additions.tabs`); the engine API
   serves them.
6. **Web**: `openChangeTab` / `registerChangeTab`, the default object editor, the existing panes moved behind it.
7. **API split**: `change.v1` out of `graph.v1`; `GraphPort` split; the `pkg/layering` rules of §10.

## Implementation

**Phase 1 (done)**: change object types are part of the domain model.

- `def.ChangeObjectType` / `def.KeyType` (`pkg/domain/def/changeobject.go`): `Schema.ChangeObjectTypes` (YAML
  `changeObjectTypes:`), checked by `Domain.Validate` (`checkChangeObjectTypes`: unique name, nor a node or link type of
  the domain; key kinds `singleton` / `sequence` (`prefix`, uppercase) / `natural` (`attributes` of the type) / `ref`
  (`impact`, `node`, `request`, `run`, `workspace` or a change object type); `scope` `change` | `workspace`; lifecycle,
  editor, search; attributes and their validators as for node types). A domain may declare change object types only.
- `typecat.ObjectType`, `Catalog.ObjectType` / `ObjectTypes` / `HasObjectType` / `ObjectAttributeChecks`: a bare ref key
  qualified, enum values and the lifecycle resolved; an unknown foreign ref or a reference naming a node or link type
  too is refused by `typecat.New`.
- The built-in domain `execution` (`domains/builtin/execution.yaml`, frozen like the other built-in domains): `Methodology`,
  `State`, `Transition`, `Fact`, `Option`, `Run`, `Journal`, `ModelCall` (§5).
- Registry: `registry.v1` `ChangeObjectType` / `KeyType` on `Domain.change_object_types`,
  `DomainSummary.change_object_type_count`, `ListTypesResponse.change_object_types` (`ChangeObjectTypeInfo`);
  `DomainToPB` now also carries the enums of a domain (they were dropped over RPC). Web: the types of
  `web/src/lib/api/types/registry.ts`; the domain form keeps the change object types as they are (no editor of them yet).

**Phase 2 (done)**: change objects are stored on the change.

- `domain.ChangeObject` / `ObjectWrite` / `ObjectFilter` (`pkg/domain/changeobject.go`); a version is an
  `object.<type>` entry of the log (`domain.LogObject`, a stream the graph alone writes: `AppendLog` refuses it), its
  workspace as flow and its key as subject; `domain.FoldObjects` replays them.
- `Graph.PutObjects` / `Graph.Objects` (`pkg/graph/changeobject.go`): the type from the catalogue in force (an untyped
  graph uses the built-in domains), the key made by its key type (a `sequence` key allocated as `<prefix>-<n>` on the
  first write and required afterwards, a `natural` key made of its attributes, a `ref` key checked against the impacts,
  nodes, workspaces or change objects of the change; a run or a request is taken as given), the value checked as node
  properties (strict attributes unless open, type and enum, property validators), `merge` over the last version, the
  first write in the initial state of the lifecycle and `transition` checked (from the current state, required
  attributes, CEL guard over `node` and `change`; transition permissions and algorithm guards are not run for change
  objects). A committed, applied or abandoned change takes none; a draft change becomes active. `Objects` reads the
  `change_object` projection, or folds the log up to `AtSeq`.
- Storage: `change_object` (both dialects, `TestSchemasAligned`; deleted with its change by `DeleteChange`), queries in
  `sqlbuild.go`; the log gains a `labels` column (`domain.LogEntry.Labels`, `LogFilter.Labels`: `jsonb @>` in
  PostgreSQL, `json_extract` per label in SQLite). The `flow`, `process_id` and `execution` columns stay until phase 5,
  when the engine takes the execution vocabulary out of the change. The repository tests replay the object entries of
  every change against the projection (`checkObjectLogs`).
- `Graph.Submit(change, Batch)` (`pkg/graph/submit.go`): node creations, checkouts, draft updates, links, items and
  change objects in one transaction, in that order (`createTx`, `checkoutTx`, `updateTx`, `linkCreateTx`, `addItemsTx`,
  `putObjectsTx`). Transitions, reviews and merges are not part of a batch.
- RPCs `PutChangeObjects`, `ListChangeObjects`, `SubmitBatch` (`graph.v1` for now; `change.v1` is phase 7), labels on
  `LogEntry` and `ListChangeLogRequest`; `graphsvc.Handler` checks `change-object:write` per type
  (`graphsvc.ResourceChangeObject`; a default policy grants it to the members of the project, stored policies of an
  existing install need the rule added), each part of a batch as its own RPC; `graphsvc.Client.PutObjects` / `Objects` /
  `Submit`; PROV-O maps a version to a `goap:ChangeObject` entity, revision of the previous one; web
  `graph.putChangeObjects` / `listChangeObjects`.

No methodology references a change object type yet (phase 5).

**Phase 3 (done)**: the guardian port.

- `graph.Guardian` (`pkg/graph/guardian.go`) with `MayCommit(c, bb) (decided, ok)`, `MayCreateChild`, `MayMove(family,
  to)`; `Change.Guardian` (column `guardian` in both dialects, `graph.v1` `Change.guardian`, web type), set by
  `NewChange.Guardian`, else the parent's for a sub-change, else `Graph.DefaultGuardian` for a root change.
  `Graph.Guardians` resolves the name; a name it cannot resolve refuses the landing, the sub-changes and the moves of the
  change (`ErrConflict`); a change naming none is free and asks nothing.
- `Graph.LandingGate` and `Graph.SubChangeValidator` are gone: `askLandingGate` asks `MayCommit` of the guardian of the
  change (the rolled-back pass building the blackboard is unchanged), `CreateChange` asks `MayCreateChild` of the
  parent's, `MoveChange` asks `MayMove` of the root change's after `ProjectMoveGate`, which now only authorizes.
- `registrysvc.Guardian` (name `registry`, `registrysvc.GuardianName`) holds the rules of the registry until the engine
  takes them (phase 5): `MayCommit` is `Service.LandingGate`, `MayCreateChild` `Service.SubChangeValidator`, `MayMove`
  the methodology rule of a move moved out of `graphsvc.ProjectMoveGate` (`Directory` reads the organisation).
  `goap-dev` wires it as the default guardian; `cmd/graph`, with no registry in process, has none: its changes are free
  (`graphsvc.Boot` warns), as they had no landing gate before.

**Phase 4 (done)**: requests.

- `domain.Request` / `RequestLink` / `RequestEntry` / `RequestFilter` (`pkg/domain/request.go`); tables `request`,
  `change_request`, `request_log` (both dialects; the links of a purged change go with it, logged `unlinked` with the
  reason `purged` on each request); `Tx.PutRequest`, `Request`, `Requests`, `PutRequestLink`, `DeleteRequestLink`,
  `RequestLinks`, `AppendRequestLog`, `RequestLog`.
- `Graph.CreateRequest` (open; requester: the caller), `Request` / `Requests` (with their links; `RequestFilter` by
  requester, projects, effective status, change), `UpdateRequest` (the title; the text is never rewritten),
  `SetRequestStatus` (closed, rejected, withdrawn: final), `LinkRequest` (a root change of the request's project; an
  untriaged request takes the change's project and is triaged), `UnlinkRequest` (a triaged request left with no link is
  open again), `RequestLog` (`pkg/graph/request.go`). `Request.Effective` derives `delivered` (every linked change
  applied). `MoveChange` takes the requests linked to the family alone and refuses a move leaving one behind; the links
  are entries `change.request_linked` / `change.request_unlinked` of the log of the change; `ChangesFilter.Request`
  lists the changes of a request.
- RPCs `CreateRequest`, `GetRequest`, `ListRequests`, `UpdateRequest`, `SetRequestStatus`, `LinkRequest`,
  `UnlinkRequest`, `ListRequestLog` (`graph.v1` until phase 7), ABAC resource `request` (`graphsvc.ResourceRequest`)
  with the actions `create`, `view`, `update`, `link`, `close`, `reject`, `withdraw` ("view", not "read", so the generic
  read rule does not open the untriaged ones): anyone signed in creates, the requester does everything on their own,
  the platform role `triage` (`access.RoleTriage`, a built-in role) on any, the members of the project of a triaged one
  view, update, link and reject it; a request the caller may not view is not found. `graphsvc.Client` and the web API
  (`graph.createRequest`...).
- Index: a third document kind, `request` (`index.KindRequest`, `Indexer.OnRequest`, `domain.RequestDocEvent` on
  `goap.requestindex.<id>`, published when a request or its links are written and when a change it is linked to is,
  republished by `Republish`), authorized as `request:view`; `indexv1.Hit.request`.
- Paths: the assistant's `create_change` and the `newChange` of `start_agent` record the request of the person (their
  words, origin `conversation`) and link it as the origin of the change (`assistantsvc.Graph` gained `CreateRequest` /
  `LinkRequest`, as `engine.GraphPort`); a trigger opening a change records a request of origin `trigger` (requester
  its service identity) linked as its origin.
- Not done (phase 4): `sdlc`'s intake (`find_or_create_change`) still creates a change and does not look up the requests, and no
  `goap-change` tool reads or links requests yet; the web has no request view (the API only).

**Phase 5, first steps (done)**: the engine's view and records.

- 5a: a methodology declares `additions.tabs` (`methodology.Additions`, `ChangeTab{title, editor, objects}`), checked at
  compile (`checkAdditions`: a title, an editor name, qualified and known change object types, `def.ObjectTypeSet`),
  stored as an attribute of `methodology@MethodologyVersion`, carried by `registry.v1` (`Additions`, `ChangeTab`) and kept
  by the web methodology form; `sdlc` adds a Facts tab.
- 5b: `pkg/engine/blackboard`: the execution view of a change (`View` from a blackboard: `Methodologies`, `Runs`,
  `OfType`) and the writes of the engine (`DeclareMethodology`, `RecordRun`) over the types of the built-in domain
  `execution`. Every run records itself on its change from `Engine.save` (`Engine.recordRun`, best effort,
  `Process.Recorded`): the methodology the change carries (`execution@Methodology`, primary with its main goal, or
  companion for a transverse run; never rewritten by another run) and the run (`execution@Run`, keyed by the run,
  labelled `process`, a new version when its status changes). The blackboard of the graph carries the change objects the
  flow sees (`domain.FacetObjects`, `GetBlackboardResponse.objects`), the conditions read them as `objects` (by type).
  `engine.GraphPort` gained `PutObjects` / `Objects`.
- Still on the header of the change, to move in the next steps of phase 5: the methodology, goal, lifecycle and state
  columns (the `execution@Methodology` records are written alongside, not read yet as the reference), the lifecycle
  transitions and gates (`Graph.TransitionChange`, `ChangeLifecycles`), the items (`decision`, `artifact`, `signal`,
  `flow`, `merge` and the registered kinds) and the options and decision points of `pkg/graph`; the guardian is still the
  registry's.

**Phase 6, first step (done)**: the declared change view.

- The type catalogue of the web holds the change object types (`TypeCatalog.objects`, `objectType`, `objectNames`, from
  `ListTypes`). `web/src/lib/changeTabs.ts`: `registerChangeTab(name, component)`, `changeTabEditor`, `openChangeTab`,
  the props of a tab (`ChangeTabProps`: change, types, change objects, read-only, workspace, `onchanged`).
  `web/src/lib/changeObjects.ts` (pure, tested): the tabs of a change (`objectTabs`: the tabs the methodology declares,
  shown while empty, then one per type of the change objects it holds or the person added, grouped by editor), keys,
  writes (`createWrite`, `editWrite`), transitions of a type's lifecycle.
- `ChangeObjects.svelte` is the default object editor: a table per type (key, state, the first attributes, version) and
  the attribute form (`NodePropertyForm`, `submitLabel`) to add or edit one, the transitions of its lifecycle as buttons;
  every save is `PutChangeObjects`. `ChangeTab.svelte` loads the change objects with the change, adds the object tabs to
  its panes and an "Add a change object" menu offering every change object type.
- Not done (phase 6): the existing use case panes (decisions, options, risks, verification, derogations, review) are
  still the change view's own panes, not editors registered for change object types (they move with their data in
  phase 5); the requests have no pane yet.

## Consequences

- A change can be created, edited, reviewed and landed by hand with no methodology; governance is opt-in through the
  guardian and holds against manual operations.
- Adding a use case to changes is a domain addition (change object types) and a methodology declaration, plus an editor when
  the default one is not enough; the core does not move.
- The why of a change is traceable to the words and identity of whoever asked, across several changes, and the request
  outlives a change abandoned or split.
- The graph API shrinks to the versioned graph, the change API to impacts, reviews, change objects and requests; the execution
  semantics live in the engine.
- Cost: a large migration across `pkg/graph`, `pkg/engine`, the registry, the domains, the RPCs and the web; one more
  runtime dependency (the change asks the engine as guardian, refusing while it is unreachable); change object types become part
  of the domain compatibility rules (a domain version that breaks a published methodology's additions is refused, as
  for node types).
