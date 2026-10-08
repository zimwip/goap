# ADR 0098 — The change is a receptacle, the blackboard is the engine's view of it, requests are the origin of work

**Status**: proposed · **Date**: 2026-10 · Builds on ADR 0001 (the change as blackboard), 0024 / 0029 / 0030 (change
impacts, event-sourced, one log), 0031 (deferred change binding), 0058 (change lifecycle and gates), 0066 / 0067
(activity, facets, flow origin and decision policy out of the core), 0079 (drafts and versions at landing), 0096 (goal
of a change). Partly supersedes ADR 0033 §1 (the request as an intake process) and moves what ADR 0058, 0066, 0067 and
0096 placed in `pkg/graph` out of it.

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
| Facts and their meaning | item kinds `decision`, `artifact`, `signal`, `transition`, `flow`; statuses `candidate`, `stale`, `superseded` (the relaunch rule of ADR 0025) |
| Options, decision points | `pkg/graph/option.go`, `Graph.OpenDecision` / `RuleDecision`..., `Graph.DecisionPolicy`, facets |
| Execution identifiers | `change_log.process_id`, `execution`; `Flow.StaleRuns`, `Flow.Origin` |
| The blackboard | `domain.Blackboard`, built by `Graph.Blackboard` / `BlackboardIn`, `ValidateBoard`, `Graph.Facets` |

As a result the engine depends on the `pkg/graph` package itself (`engine.GraphPort` takes `graph.NewChange`,
`graph.NodeCreate`... and has some 45 methods), the graph API mixes the versioned graph with execution semantics
(about 90 RPCs in `graph.v1`), and a change cannot exist, or be reasoned about, without the vocabulary of a methodology.

The origin of the work is missing altogether. `Change.intent` is a free text, the requester is not recorded, a request
exists only once a change has been created for it (the assistant's `create_change`, `sdlc`'s `find_or_create_change`,
eager binding leaving orphan drafts, ADR 0033), and a change answering several requests, or a request split over
several changes, cannot be expressed.

## Decision

### 1. Four layers, each ignorant of the ones above

```
 Engine      runs, planners, agents; the BLACKBOARD is its execution view of a change
   │         knows: Change, Methodology (compiled), Graph (reads)
 Change      the receptacle: header, key/value records, impacts and drafts, workspaces, landing; REQUESTS
   │         knows: Graph, Domain
 Graph       node versions, baselines, branches, tags
             knows: Domain
 Domain      node / link types, attributes, node lifecycles, algorithms (registry)
```

The methodology (registry) is consumed by the engine only. A person can act at two levels: on the **change** directly
(edit nodes, land, no methodology needed) or through the **engine** (advance a change governed by a methodology).

### 2. The change is a receptacle

#### Header

```
change: id, title, intent, namespace, owner_org, project_id, parent_id, baseline_id, branch,
        result_baseline_id, status, guardian, created_at
```

- `methodology`, `goal`, `lifecycle`, `state` and the free-form `data` are removed: they become records (§2.2).
- `status` stays the storage lifecycle (`draft`, `active`, `committed`, `applied`, `abandoned`): mechanism, not method.
- `intent` stays the working formulation of the change, editable; the words of whoever asked are in its requests (§6).
- `guardian` is an opaque name (§3).

#### Key/value records, appended to the change log

```go
type Record struct {
    Stream    string            // the owner of the record: "bb", "engine", "review", "risk"...; reserved: "change", "impact", "request"
    Key       string            // unique within the stream; each append is a new version of the key
    Value     json.RawMessage   // opaque to the change
    Workspace string            // the draft layer it belongs to, "" = the main one
    Labels    map[string]string // opaque indexed labels: process, execution, step...
}
```

- `change_log` stays the one ordered log of the change (`seq`, ADR 0030). Its `flow`, `process_id` and `execution`
  columns are replaced by `workspace` and `labels` (`jsonb` with a GIN index in PostgreSQL; a JSON column and an
  expression index in SQLite): the change no longer names a process or an execution.
- A projection `change_record (change_id, stream, key, workspace, seq, value)` keeps the last value of each key, for fast
  reads and for filtering changes on a record (`ListChanges{Records: {"bb/methodology:sdlc": ...}}`) without the change
  knowing what a methodology is.
- **Stream ownership**: writing a stream is the ABAC permission `change-record:write` on the resource
  `change-record:<stream>`. The default policies give `bb` and `engine` to the engine's service identity only; the
  reserved streams are written by the change itself.
- Records are the only extension point of the change: a use case (risks, verification, reviews...) adds a stream,
  never a column or an item kind.

#### Impacts stay in the change

An impact is the write of the graph, pure mechanism: drafts, checkout, edits, node transitions (node lifecycles come from
the domain), merge and split, explicit reviews, rebase of sub-changes, commit and integration (ADR 0076–0082), still
event-sourced through `emit` on the reserved `impact.*` stream (ADR 0029). The CEL the change evaluates is the domain's
only (node lifecycle guards, validators); it never evaluates a methodology's conditions. `ReviewPolicy` (ADR 0075: the
verifier is not the producer) stays a generic seam of the change: it judges impact reviews, whoever performs them.

#### Workspaces

A workspace is today's flow stripped of its meaning: a layer of drafts and records with a parent and a fork point,
opened, adopted or discarded. Whether it is an **option** or the **relaunch** of a step is said by the engine in a
record; `Flow.StaleRuns` and `Flow.Origin` leave the change, and so does the `candidate` / `stale` / `superseded`
rule of items (ADR 0025), which becomes the engine's reading of its own records.

#### Atomicity

`Submit(change, Batch)` applies records and impact operations in one transaction, all or none: an action's outputs are
written together at the end of the action, and a run can be replayed safely (ADR 0031).

### 3. The guardian

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
- The engine is the guardian `"engine"`: it sets the name in the same `Submit` as the first methodology record of the
  change, and implements the port with the landing floor and gates of ADR 0058 / 0078, the sub-activity cascade
  (`SubChangeValidator`) and the project move rules (`ProjectMoveGate`, ADR 0091).
- `Graph.LandingGate`, `Graph.SubChangeValidator` and `Graph.ProjectMoveGate` are replaced by it.

### 4. The blackboard is the engine's execution view of a change

The blackboard is not an object of its own: it is the change, read and written with the semantics of its
methodologies. Its identity is the change's id, it stores nothing elsewhere, and the dependency goes one way: the
blackboard knows the change, the change knows nothing of the blackboard. It lives in the engine component
(`pkg/engine/blackboard`), stateless over the change, cached per change and validated by the last `seq` of the log (as the
drafts cache, `pkg/graph/draftcache.go`).

| Records (stream `bb`) | Replaces |
|---|---|
| `methodology:<name>` = `{version, role: primary\|companion, goal}`, one record per methodology | `Change.Methodology`, `Change.Goal`, `ChangeLifecycles.DefaultGoal` (ADR 0096), the companions of ADR 0036 |
| `state` = `{lifecycle, state}`, `transition:<seq>` | `Change.Lifecycle` / `State`, `KindTransition`, `TransitionChange` (ADR 0058) |
| `fact:<key>` (`kind`, `status`, payload) | items `decision`, `artifact`, `signal`, `flow`, `merge` and the registered kinds |
| `option:<workspace>`, `decision:<id>` | options (ADR 0032 §6), decision points (ADR 0009 §4), `DecisionPolicy` |

- `Observe(change, workspace, at)` gives the world of a run: the change header, its impacts and drafts, its records, the
  hydrated nodes and the conditions of its methodologies evaluated. `pkg/condition` (the CEL environment) belongs to the
  engine side; `Explain` is the existing `ExplainCondition`.
- The validation of facts (`ItemPolicy`, `ItemAuthorizer`, `domain.RegisterItemKind`) happens when the engine writes
  them, not in the change.
- The web and the assistant go through the engine API for the blackboard: transitions of a change, decisions, options,
  facts. The change API stays the way to edit nodes by hand.
- The journal of the engine (ADR 0011) and the prompts (ADR 0059) are records of the `engine` stream.

### 5. Runs are the engine's, referenced by the change

The engine stays stateful: a run (`Process`) is stored by the engine. The change references each run working on it by
a record `engine/run:<id>` = `{methodology, agent, goal, status, startedAt}`, kept up to date by the engine: a pointer,
not a copy. A run may also reference a request (§6) before any change exists, which is what deferred binding
(ADR 0031) becomes.

### 6. Requests: the origin of work

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
  whoever may triage the project, confirms the need is met): delivering is not satisfying.
- **Project**: a request may have no project until it is triaged. A change linked to a request is in the request's
  project; linking an untriaged request sets its project to the change's; moving a change (ADR 0091) moves the
  requests it holds only when they are linked to it alone, else it is refused.
- **A change needs no request**: the storage does not require one (seeds, administration, quick manual edits). A
  methodology may require one; its guardian checks it.
- The links and status moves are entries of `request_log` and, for each linked change, of its log
  (`change.request.linked` / `unlinked`).
- Requests are indexed (ADR 0095, a third document kind) so that duplicates and the change a request belongs to can be
  found.
- Paths that change: the assistant's `create_change` creates a request then proposes a change linked to it; `sdlc`'s
  `find_or_create_change` links the request to an existing or a new change; a trigger creates a request with
  `origin: trigger`; nothing creates a change before a request is linked to it, so the orphans of ADR 0033 disappear.

### 7. APIs

**`change.v1`** (split out of `graph.v1`, which keeps the reads of nodes, baselines, branches and tags):

```go
type Changes interface {
    Create(ctx, NewChange) (Change, error)                 // no methodology, no goal
    Get, List(ctx, ChangeFilter{..., Records, Request}), Update, Move, Purge

    Append(ctx, id, []Record) ([]Entry, error)
    Records(ctx, id, RecordFilter{Stream, KeyPrefix, Workspace, Labels, AtSeq}) ([]Entry, error) // last value per key
    Log(ctx, id, LogFilter) ([]Entry, error)

    OpenWorkspace, AdoptWorkspace, DiscardWorkspace

    ImpactNodeCreate, Checkout, Update, Transition, Cancel, Merge, Split, ImpactLink*, Withdraw
    Review, ReviewBatch, Rebase, Resolve
    View(ctx, id, workspace, level) (Baseline, error)

    Submit(ctx, id, Batch) (BatchResult, error)            // records + impact operations, one transaction
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

**`engine.v1`** keeps the runs (`StartProcess`, which can take a `request` instead of a change, `SubmitHumanInput`,
`ApproveAction`, `UnblockProcess`, `GetProcess`, `GetProcessProgress`, `ListStartingPoints`...) and gains the
blackboard: `Observe`, `ExplainCondition`, `PutFacts`, `ListTransitions` / `Transition`, options (`Open`, `Activate`,
`Evaluate`, `Select`, `Reject`, `Compare`), decision points (`Open`, `Rule`, `Answer`, `Ratify`), and `DeclareMethodology`
(writes `bb/methodology:<name>` and the guardian).

**Ports of the engine**: `engine.GraphPort` is split into `ChangePort` (the subset of `Changes` and `Requests` it uses)
and `GraphReadPort` (baselines, node reads, structures), both over neutral types in `pkg/domain` (or a contract package),
never `pkg/graph`'s; `MethodologyPort`, `Scope` and the model client are unchanged.

### 8. Layering rules

`pkg/layering` gains: the change (`pkg/change`, today the change half of `pkg/graph`) imports neither `pkg/engine`,
`pkg/methodology` nor `pkg/condition`; `pkg/engine` does not import `pkg/graph` or `pkg/change` (only their ports' types);
the registry does not import `pkg/engine` (`PreviewPlan` moves to a planning package both use).

## Migration

Each phase keeps the suites green and the platform usable.

1. **Records**: `Append` / `Records`, the `change_record` projection, `workspace` + `labels` on `change_log` (both
   dialects, `TestSchemasAligned`), stream ownership in the default policies; `Submit`.
2. **Guardian**: the port in the change, implemented over the current registry seams (`LandingGate`,
   `SubChangeValidator`, `ProjectMoveGate`), then moved to the engine.
3. **Requests**: tables, `Requests` API, `request_log`, index kind, assistant and trigger paths; `sdlc` intake on
   requests.
4. **Blackboard in the engine**: `pkg/engine/blackboard`; the methodology, goal, lifecycle and state columns become
   `bb` records (a data migration writes them for existing changes); items, options, decision points and facets move;
   the engine API serves them; the web follows.
5. **API split**: `change.v1` out of `graph.v1`; `GraphPort` split; the `pkg/layering` rules of §8.

## Consequences

- A change can be created, edited and landed by hand with no methodology; governance is opt-in through the guardian
  and holds against manual operations.
- The why of a change is traceable to the words and identity of whoever asked, across several changes, and the request
  outlives a change abandoned or split.
- The graph API shrinks to the versioned graph; the change API to storage and graph writes; the execution semantics
  live in one component.
- Cost: a large migration across `pkg/graph`, `pkg/engine`, the registry, the RPCs and the web; one more runtime
  dependency (the change asks the engine as guardian, refusing while it is unreachable); records are opaque JSON, so the
  engine owns their schemas and their evolution.

## Open questions

1. **Triage**: who may triage a request with no project yet: a platform role (`reader` / `admin` today, ADR 0047) or a
   new one? Proposal: a platform role `triage`, then the project's roles once it is known.
2. **Sub-changes**: do the links of a request stay on the parent change, or are they propagated to the sub-changes with
   `covers`? Proposal: they stay on the parent; a sub-change is a part of its parent's answer (ADR 0081).
3. **Personal changes** (ADR 0037): a request held only by its requester, linked to a personal change, may stay without
   a project?
