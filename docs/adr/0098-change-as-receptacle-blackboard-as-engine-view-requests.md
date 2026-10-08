# ADR 0098 — The change is a receptacle, the blackboard is the engine's view of it, requests are the origin of work

**Status**: proposed · **Date**: 2026-10 · Builds on ADR 0001 (the change as blackboard), 0024 / 0029 / 0030 (change
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

Nothing is enabled: like node types, every change object type of a published domain is available on every change,
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
    MayCommit(ctx context.Context, c Change) error
    MayCreateChild(ctx context.Context, parent, child Change) error
    MayMove(ctx context.Context, c Change, project string) error
}
```

- The change asks the guardian named in its header; an unknown or unreachable guardian refuses (fail closed).
- A change with no guardian is free (manual editing, seeds, administration).
- The engine is the guardian `"engine"`: it sets the name in the same `Submit` as the first methodology change object of the
  change, and implements the port with the landing floor and gates of ADR 0058 / 0078, the sub-activity cascade
  (`SubChangeValidator`), the project move rules (`ProjectMoveGate`, ADR 0091) and the change object types a methodology
  requires (a request, a closed risk register...).
- `Graph.LandingGate`, `Graph.SubChangeValidator` and `Graph.ProjectMoveGate` are replaced by it.

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
