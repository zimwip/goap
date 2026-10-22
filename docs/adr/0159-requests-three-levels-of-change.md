# ADR 0159 — Requests: three levels of change, from the spark to the system

**Status**: proposed · **Date**: 2026-10 · Supersedes ADR 0033 §3 (the `request` fact, never built) · Builds on ADR
0036 §3 (events between methodologies), ADR 0106 (integrates, guardian chain), ADR 0110 (declared events, `emits`),
ADR 0132 (gates and freezes), ADR 0154 (logical URLs), ADR 0157 (views)

## Context

A request is the first spark: an idea or a need that comes from outside (a person, a mail, a ticket, a regulation).
It pushes the system until a project or a change acts on it. Today the platform has no place for it:

- the assistant goes straight from a message to a change (ADR 0087, 0090), and the request is the change's intent;
- `alm@Need` is a need already qualified, held by the delivery domain;
- ADR 0033 §3 proposed a `request` fact in the log of the change it shaped. It was never built.

Request management is a methodology too. It acts on the system: qualifying a request, accepting it and closing it
are modifications, so each one goes through a change (rule 2). A request is then two things at once: **the content**
(what was asked, by whom, where it stands) and **the change** that works on it. Above it, a project does not act on
one request at a time: it gathers several accepted requests into what it agrees to do, then runs the changes that act
on the system.

## Decision

Three levels. At each level a change produces content, and that content is the input of the change one level up.
Verification happens on the way up and validation on the way down (the V&V of the delivery).

```text
 level 1  intake           Request ──(qualified, accepted)──────────────────────────► closed
                             ▲                                                         ▲
                             │ reframes (Need → lnk URL ?v=, part, coverage)           │ request.fulfilled
 level 2  project           Need ∈ Scope ──(agreed: gate, freeze)──► released ────────┘
                             │ integrates (sub-changes)          ▲
                             ▼                                    │ change.applied (from: integrated)
 level 3  system            sdlc change ──(verified: build, tests, gates)──► merged into the project change
```

### 1. Level 1: the request, a node and its own change

- A domain `intake` (shipped under `domains/` like `alm`, not built in) declares `intake@Request`:
  `title`, `text`, `requester`, `source` (`assistant`, `mail`, `ticket`, `import`…), `receivedAt`, and a lifecycle
  `received → qualified → accepted | declined → planned → fulfilled → closed` (`declined` and `closed` final;
  `withdrawn` reachable from any open state).
- The methodology `request-management` acts on namespace `intake`. **One root change per request** (rule 2: the
  change is the work on the request, the node its lasting record). Its process: register, qualify (completeness,
  duplicates through the index, ADR 0095), decide (a gate, ADR 0132: `accepted` or `declined`, the requester told).
  The change commits at registration so the request has a version, and is merged into main once decided, so the
  project level reads it at the head of main (ADR 0157).
- Every node belongs to a project. A request that arrives before any project is held by an intake project (an
  ordinary `ProjectUnit` listing `request-management`), else the root project.
- The sparks from outside all start the same change: the assistant (a screen tool or a server tool
  `register_request`, a proposal like `start_agent`), an event trigger, an importer of `intake` (ADR 0148: a mail
  box, a ticket system).

### 2. Level 2: the project change, needs reframing request versions

- A project methodology (the role `release-based` plays today, ADR 0106) acts on the namespace of the system the
  project changes (`alm`).
- **Taking a request in reframes it as a Need.** The project does not aggregate the raw Request: its change creates
  an `alm@Need` (the project's words for what it will do) linked to the Request version it reframes by
  `alm@reframes`, whose target is the logical URL of the version, `lnk:intake/Request/REQ-12?v=3` (ADR 0154: a node of
  this graph naming its version). The link's attributes (ADR 0055) say **what is scoped from here**: `part` (the part
  of the request this need takes), `coverage` (`full` | `partial`), `rationale`. One request may be reframed by
  several needs (split across releases or projects), one need may reframe several requests (duplicates merged). The
  link names the version that was taken in: a request edited later is a new version, seen as a change of what was
  asked, never a silent change of the scope.
- **The project pulls, the request never points to a project.** The links go from the project's Needs to the Request
  versions, so they belong to the project's versions (outgoing links belong to the source, `apply.go`) and are
  written by the project change: scoping is a decision of the project, made in its change and reviewed there. A
  Request holds no link to a project or a release; it learns it was taken in through `request.planned` (§4), and a
  request taken in by several projects is reframed by a need in each, nothing written on it.
- **Backlog and release scope aggregate Needs.** The backlog (`alm@Backlog`) and each release scope (`alm@Scope`, or
  a release / initiative: the name is a domain choice) link the Needs they hold (`alm@aggregates`). A Need is **not
  scoped yet** (in the backlog only) or **scoped** in a release (the scope links it, the backlog link dropped in the
  same change). The trace reads Requirement → Need → Request version (`alm@satisfies`, then `alm@reframes`).
- **What a project takes in is the project methodology's choice, stated as conditions on the Request** (its state,
  its attributes, the version), never a rule of the platform. Example, automatic backlog feeding (still the
  project's act, by its own trigger): an event trigger of the project methodology on the applied changes of `intake`
  (`change.merged` into main, filter `event.nodes.exists(n, n.type == "intake@Request" && n.state ==
  "accepted")`) drafts a Need reframing each accepted Request of the project's chain (`coverage: full`, to be
  reworded) into the backlog. The steps of the project process carry the same kind of conditions (a scoped condition
  over the Needs and the Requests they reframe, ADR 0109: every reframed Request `accepted` before the scope's gate),
  so only a request in the right state starts a task at the project level. Another methodology may take in
  `qualified` requests into a provisional scope: same mechanism, other conditions.
- The agreement is a gate (ADR 0132) with a board, whose `freezes` lock the scope and its Needs (`types: [alm@Scope,
  alm@Need]`). The project change never writes the `intake` namespace itself (one namespace per change).
- `integrates: [sdlc]` (ADR 0106, `exclusive` when the project wants every delivery to land through it): its process
  creates the level-3 changes as sub-changes, one per piece of work it plans, and its guardian governs them. They
  write the Requirements satisfying the Needs, so the trace goes from a requirement back to the words of the
  requester.

### 3. Level 3: the changes acting on the system

The `sdlc` changes, unchanged. They are sub-changes of the project change, merge into it under their own guardian
(verification: build, tests, gates), and only the project change lands on main.

### 4. Events close the loop, no level writes another's namespace

- A level-3 change merged into the project change is `change.applied` seen `from: integrated` (ADR 0106 `on:`). The
  project methodology reacts: it marks what that sub-change delivered on its scope.
- The project change, merged into main (released), raises its declared event `request.fulfilled` (ADR 0110: an
  `events:` declaration, `emits` on the releasing action), a signal item whose data lists, per Request version
  reframed by the Needs of the scope, the Needs delivered and their `coverage`. Likewise `request.planned` at the
  agreement.
- `request-management` declares an event trigger on `request.fulfilled` (and `request.planned`), `target:
  new_change`: a short change of namespace `intake` that moves to `fulfilled` each Request whose reframing Needs are
  all delivered (a `partial` coverage keeps it `planned` until the rest is), then runs **validation**
  with the requester (a human step: does what was delivered answer the request?) before `closed`. A refusal reopens
  the request (`qualified`), which a project takes in again as a new version. Event triggers are global
  (`TriggerManager.Handle`, `DeclaredEvents`), so the two methodologies know each other only by an event name and a
  URL, never by their definitions (rule 1).

### 5. Nothing of this is platform code

The three levels are a use of the generic approach, not a feature: the platform names no request, no scope and no
level.

- **Levels are derived**, as in ADR 0106: what makes a level is that one change's content is the input of another
  change (links to versions) and that one methodology integrates another (`integrates:`). A portfolio above the
  projects is a fourth methodology integrating the project one, aggregating Scope versions the same way.
- **What a request is, is a domain** (`intake@Request`, its attributes and lifecycle); what a scope is, the project's
  domain. An organisation with another intake (an RFC, a regulatory finding, an incident) declares another type or
  domain; the pattern does not move.
- **How each level works is a methodology** (`request-management`, the project methodology, `sdlc`), chosen per
  project (`ProjectUnit.methodologies`) and versioned like any other (ADR 0113).
- **The coupling is events and URLs**: declared events (ADR 0110), event triggers and `on:` (ADR 0036 §3, 0106),
  links to logical URLs (ADR 0154). Replacing one level touches neither of the others.

### 6. Across graphs: no shadow node, no distributed transaction

`intake` and the project's namespace may live in two graphs (ADR 0154 §9, federation). Then:

- **The pointed graph is never written by the pointing one.** The Need's link is a URL kept on the Need's version
  (`link.to_url`); the intake graph holds no copy of it and no shadow node. Since the URL names a version, it never
  drifts: a Request edited later is a newer version the project sees as such (`?v=3` against a head at 5), never a
  link silently pointing at other content.
- **What the Request side must know, it records itself, from the events.** `request.planned` / `request.fulfilled`
  carry the URLs of the Need versions; the intake change they start writes on the Request what it learns (its state,
  and if wanted a link back `intake@taken_in_by` to the Need version's URL). That is intake's own fact, in its own
  transaction, not a mirror kept in sync.
- **"Who points to me" is a read, not data.** The back links across graphs are a query of the index (the external
  links indexed by their target URL, a federated search), eventually consistent; nothing depends on it to be correct.
- **Each graph commits alone; the chain is a saga.** The outbox delivers each event at least once, in order (ADR
  0136); the reactions are idempotent (the event id, and the version in the URL: moving a Request already
  `fulfilled` for that Need is a no-op). A step undone on one side is a new event, not a rollback: a Request
  withdrawn raises `request.withdrawn`, and the project marks the Needs reframing it as suspect. The window where
  the Request says `accepted` while a project already scoped it is accepted, and shown as such.

### 7. Views and modes

In project mode (ADR 0158 draft) a project shows its inbox: the Requests of its chain at the head of main, by state.
A request is edited only in its change (change mode), and a draft request exists only in that change's view until it
commits (ADR 0157). Triage and validation tasks open in the context of their request's change.

## Consequences

- A request is graph data: searchable, linkable, versioned, auditable; the change log of its change says who
  qualified, decided and closed it. ADR 0033 §3's `request` fact is not needed.
- The trace from the spark to the system is links between versions (Request ← Need, with what is scoped; Need ← Scope; Need ← Requirement),
  and the closing of a request is the effect of an event, so each level can be replaced (another project methodology,
  another intake) without touching the others.
- Nothing new in the graph or the engine, as far as this ADR goes: a domain, two methodologies, links to URLs and
  declared events. To check while implementing:
  - a link from a node of `alm` to a version of a node of `intake` (ADR 0154 step 2: a local target with a version);
    the link type `alm@reframes` declares `toTypes: [intake@Request]` across domains, with its attributes;
  - the payload of a declared event carries the URLs (signal item data), and the closing change finds them there;
  - the assistant's `register_request` and an `intake` importer.
- Gaps found in the code (2026-10):
  - Done: every commit, rebase and merge names the branch and the node versions it left there (`change.committed`, and
    the new `change.rebased` and `change.merged`: `domain.ChangeEvent.Branch` / `Nodes`, `domain.EventNode`; CEL
    `event.branch`, `event.nodes`), so the backlog trigger filters on a committed Request (`event.nodes.exists(n,
    n.type == "intake@Request" && n.state == "accepted")`). The events stay events of the change: only a change
    modifies the graph.
  - Done: a trigger with `target: new_change` creates its change in the project of the event's change, else of its
    process (`TriggerEvent.projectKey`), no longer in the root project.
  - Conditions of the project change see the linked Requests only within one graph (`BlackboardIn` hydrates the
    neighbours of a local target, any namespace, and skips `External()` links). Across graphs, the Need's conditions
    on the Request state need the external targets resolved into the blackboard (ADR 0154: resolution at read, not
    done yet).
  - The back links across graphs need the index to hold the external links by target URL (`graph/index` indexes
    nodes and changes, not links).
- A request reframed in several projects is fulfilled when every Need reframing it is delivered (§4); whether one
  project may decline its part and leave the request fulfilled by the others is the intake methodology's rule.
