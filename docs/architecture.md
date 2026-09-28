# GOAP — Agentic platform for enterprise methodologies

> Architecture document — version 0.1 (foundation)
> Status: **working draft**. Sections marked 🟡 are hypotheses to validate, 🟢 are implemented in the initial core.

## 1. Vision

GOAP is a **general-purpose** agentic platform in which **enterprise methodologies**
(impact analysis, requirements management, architecture review, compliance, onboarding…) are deployed
as declarative definitions. A methodology describes:

- a **domain model** (node types, link types);
- **conditions** (predicates on the state of a change);
- **actions** (units of work: LLM, MCP tool, human, code) with preconditions, effects, and cost;
- **goals** expressed as a set of conditions to reach.

The execution engine is inspired by [Embabel](https://github.com/embabel/embabel-agent):
**GOAP planning** (Goal Oriented Action Planning, A\*) on a **blackboard**, replanning after
each action (OODA loop), and a preliminary **intent loop** that turns a natural-language
request into a formal goal.

GOAP's originality: the blackboard is not a bag of objects in memory, it is the **change axis**
of a versioned knowledge graph, whose other axis, the **domain axis**, describes the reference content.

## 2. Fundamental concepts

### 2.1 The two-axis graph

```
                 DOMAIN AXIS (content, versioned)
   ┌──────────────────────────────────────────────────────────┐
   │  NEED-4@v2 ◄──satisfies── REQ-12@v3 ◄──verifies── TST-7@v1│   baseline B1 (reference)
   │                              │                             │
   └──────────────────────────────┼─────────────────────────────┘
                                  │ reference (target)
                 CHANGE AXIS (modification, = blackboard)
   ┌──────────────────────────────┼─────────────────────────────┐
   │ Change CR-42 (baseline = B1, intent = "…")              │
   │   ├─ change impact #n1 modified REQ-12 (pre v3, post v4)     │
   │   ├─ change impact #n2 modified TST-7  (pre v1, planned)     │
   │   ├─ change impact #n3 created  TST-9  ─verifies→ REQ-12@v4  │
   │   └─ decision #d1 (fact)                                   │
   └────────────────────────────────────────────────────────────┘
                                  │ apply
                                  ▼
                 baseline B2 (resulting graph): REQ-12@v4, TST-9@v1 …
```

#### Domain axis

| Concept | Description |
|---|---|
| **Node** | Typed content element. Its type is a qualified reference `<namespace>@<NodeType>` (`alm@Requirement`, [ADR 0012](adr/0012-node-types.md)). Stable identity `NodeID` + readable `Key` (`REQ-12`). |
| **Version** | Each modification creates a new immutable version `NodeID@vN`. A version can be a *tombstone* (deletion). |
| **Link** | Typed relationship **from version to version**: `REQ-12@v3 ─satisfies→ NEED-4@v2`. A link does not automatically "follow" new versions: if `NEED-4` moves to v3, the link becomes **suspect** — this is the model's native impact signal. |
| **Baseline** | Coherent set `{NodeID → Version}`, scoped to one namespace (every node in it belongs to that namespace): a "commit" of the graph. A baseline's links are those whose two endpoints are both in the baseline. Every modification starts from a reference baseline of its own namespace and produces a resulting baseline of that same namespace. |

**Namespaces** ([ADR 0015](adr/0015-namespaces.md)): every node lives in a namespace, and a namespace is the content of
one domain ([ADR 0013](adr/0013-domains.md)): `alm` (delivery), `organisation`, `platform` (MCPs, adapter definitions,
model configuration), and the meta-domain `methodology` that holds the definitions of the methodologies
([ADR 0023](adr/0023-definitions-in-the-graph.md)). `methodology`, `organisation` and `platform` are **built in and
frozen**: the platform reads them in its own way, so they are initialised at startup from `domains/builtin/`, change
with the code and are read-only in the domain editor; a new namespace is a new domain published from the domain editor.
Domains themselves are not graph data: the registry keeps them in its database. Keys are unique per namespace. A change acts on one namespace: it can
only create and modify nodes of that namespace, but may link to nodes of another one. So that an action or agent's
default queries (a prompt's `.Baseline.Nodes`, a script's `Host.Node`/`Nodes`/`Links`, a human task's node picker)
are bound to the change's own declared namespace rather than exposing every namespace at once, baselines and
branches are namespace-scoped too: a baseline only ever holds nodes of one namespace, and a branch's identity is
`(namespace, name)` — "main" is really "main of namespace X", and two namespaces may each have their own branch of
the same name. Cross-namespace capabilities (ownership links, adapter/MCP resolution, ABAC, the model gateway, the
methodology registry) are unaffected: they read a specific namespace's own head directly (e.g. `BranchHead(ns,
"main")`) instead of relying on one shared baseline that happened to hold every namespace's nodes together.
A baseline can be browsed without loading it whole (`ListNamespaces`, `ListBaselineNodes`: the node count per type
and a page of the nodes of one type, text-filtered on the server; `GetNodeNeighbourhood`: a node, its direct
neighbours in both directions and the suspect links among them, `pkg/graph/browse.go`). The IDE's Baseline explorer
and tab navigate namespace → baseline → node type → paged nodes, and show the neighbour graph of the selected node.
A request started without a baseline or a change (the assistant, a trigger) opens its change on the latest baseline
of the namespace its methodology acts on, known once the intent is identified.
The organisation is a hierarchy of units in its own namespace (`organisation`); nodes reference their owner
unit across namespaces, and a change is split into sub-changes along unit boundaries
([ADR 0016](adr/0016-organisation-and-sub-changes.md)).

**Organisations** ([ADR 0019](adr/0019-organisations-mcp-connectors.md)) are the `OrgUnit` nodes above. A
change is held by one unit (`ownerOrg`; empty: the default organisation `ORG-DEFAULT`, created at the first start
and the implicit root of every unit's ancestors). The units also hold the **adapters** that implement the tools of
the platform (§3.9): a unit inherits the adapters of its ancestors, the nearest wins.

Versioning rule ([ADR 0003](adr/0003-version-to-version-links.md)): **outgoing links belong
to the source node's version**. Adding/removing an outgoing link creates a new version of the source;
a node that changes version carries its outgoing links forward; incoming links from unmodified
nodes stay on the old version and become **suspect**. Baselines thus remain immutable.

#### Version branches ([ADR 0009](adr/0009-branches-options-decisions.md))

Versions are numbered **per node, across all branches** (`REQ-1@v7`), and each version carries
its `branch` (`main` by default), its `parents`, and its `reason`: `create`, `revise` (successor on the same
branch), `derive` (first version on a parallel branch), `merge` (two parents) or `adopt` (ADR 0025).

```
REQ-1  v1(main) ── v3(main, revise) ───────────── v4(main, merge ← v3 + v2)
          └────── v2(opt-a, derive) ─────────────────┘
```

- A **branch** (`CreateBranch`) belongs to one namespace (`Branch.namespace`) and starts from a baseline of that
  same namespace (`forkBaseline`), advancing through the changes applied on it (`Change.branch`); its **head**
  (`head`) is the latest baseline produced. `main` exists implicitly, per namespace.
- "Latest version" is read **per branch** (`latest(node, branch)`): applying (`apply`) a change detects a
  conflict when a node has advanced **on the change's branch** since the base version.
- **Branch merge** (`PlanMerge` / `MergeBranch`): for each node modified on the source branch since
  the fork, a 3-way merge against the common ancestor (walking up `parents`) — property by property (one side
  equal to the ancestor takes the other, otherwise **conflict**) and outgoing links as a set (key = type + target
  node: added on one side → kept, removed on one side → removed). The merge is a change of the platform (one change impact per merged node, the versions written directly) on
  the target; conflicts require a resolution (resolved properties, or `skip`). The branch moves to `merged`.

#### Change axis

| Concept | Description |
|---|---|
| **Change** | A modification request. References a starting baseline, carries the initial intent and the chosen goal. It is **the blackboard** of an agentic process. |
| **ChangeItem** | Blackboard **fact** with no node: `kind` ∈ `decision`, `artifact`, `merge`, `flow`. Each item has a provenance (`producedBy` = action, `derivedFrom` = other items). |
| **ChangeImpact** | Link from the change to a node ([ADR 0024](adr/0024-change-impacts.md)): `intent` (`created` / `modified`), `rationale`, `pre` (released version), `post` (version written on the change branch, empty while only planned: an impact without a proposal), `landed` (version on the target branch once applied), `review` with a mandatory comment. `WriteNode` creates the post version, `ReviewNode` accepts or rejects, `Apply` lands the branch (fast-forward, else auto-merge or `merge_pending`, then `MergeChange`). A node version records its `changeId`, `changeImpact` and `comment`. A node's type is a direct attribute of the node (`Node.Type`), there is no `instanceOf` link. The change impacts are **event-sourced** ([ADR 0029](adr/0029-event-sourced-change-impacts.md)): every operation (`declared`, `written`, `reviewed`, `discarded`, `adopted`, `landed`, `rebased`) is an event of the change's impact log with its caller (principal, action run, flow); the `change_impact` table is its projection, and what a flow sees is a fold of the log (`ListChangeEvents`; the **Audit** pane of the change merges it with the execution journal and the facts into one chronological trail, with CSV / JSON export). |
| **Decision** | A choice about something that is not a node (human or agent); the acceptance of a node is the review of its change impact. |
| **Artifact** | Free-form data produced by an action (summary, report, tool response). |

Applying a Change (`ApplyChange`) lands the accepted change impacts' versions and creates a new baseline. The Change remains the explainable history of *why* the graph changed.

### 2.2 Correspondence with Embabel

| Embabel | GOAP | Comment |
|---|---|---|
| Blackboard | **Change** (change axis) | Persisted, shared, auditable; references domain elements. |
| Blackboard object | **ChangeItem** | Typed by `kind` + semantic `type`. |
| Condition | **Condition** = [CEL](https://cel.dev) expression evaluated on the blackboard *hydrated* with the referenced domain nodes | See §2.3. |
| Agent (`@Agent`) | Methodology **Agent**: a planner (`goap`, `utility`, `hybrid`) + admissible actions + goals | See §2.9. An agent can call other agents. |
| Action (`@Action`) | Methodology **Action** (`script` JS/Go, `llm`, `tool`, `human`, `builtin`) | Preconditions/effects = named conditions. An action's **expectation** translates into a link on the domain axis (§2.4). `script` action code uses the DSL (§2.10). |
| Goal (`@AchievesGoal`) | **Goal** = conjunction of conditions + value | |
| GOAP planner (A\*) | `pkg/goap` 🟢 | A\* over the space of boolean states. |
| Autonomy / goal selection | **Intent loop** `pkg/intent` 🟢 | Identification of the agent and the goal (all published methodologies) + clarification as long as confidence is insufficient. |
| AgentProcess | **Process** `pkg/engine` 🟢 | Observe → plan → act → replan loop. |

### 2.3 Conditions: expressions on the state of the change

A condition is a **named** predicate evaluated on the blackboard. We chose **CEL** (Common Expression
Language, `cel-go`): not Turing-complete, typed, fast, sandboxed, serializable in a YAML definition.

Variables exposed to the expression:

| Variable | Content |
|---|---|
| `change` | `{id, title, intent, status, goal, baseline, branch, data}` |
| `items` | the active ChangeItems (facts) |
| `decisions`, `artifacts`, `merges` | items filtered by `kind` |
| `changeImpacts` | the change impacts ([ADR 0024](adr/0024-change-impacts.md)), `{id, key, type, types, intent, rationale, review, reviews, comment, pre, post, landed, planned, hasPost, recheck, props, via}`; `pre`, `post`, `landed` are hydrated node views or `null` |
| `vars` | free process variables (clarification answers, parameters) |

Each node reference of a change impact (`pre`, `post`, `landed`, link endpoints) is **hydrated**:
`{id, version, key, type, props, out: [{type, to}], in: [{type, from}], latest}`. A condition can thus
navigate the *reference* domain without a network call during evaluation (hydration is done
once per cycle by the engine via the Graph Service).

Examples:

```cel
// at least one identified impact
size(changeImpacts) > 0

// every impacted requirement has a written version
changeImpacts.filter(n, "Requirement" in n.types).all(n, n.hasPost)

// no suspect: every impacted pre is at its latest version
changeImpacts.all(n, n.pre == null || n.pre.version == n.pre.latest)
```

The world seen by the planner is **the boolean evaluation of all conditions**
(`WorldState = {name → true/false}`). A condition whose evaluation fails (missing field, type error) is
**unknown** and satisfies no precondition.

### 2.4 Actions and "expectation" on the domain axis

An action declares:

- `pre`: required conditions (`{name: bool}`);
- `effects`: conditions the action is **expected** to make true/false (used for planning);
- `expects` (optional): the **expectation** expressed as a **domain link pattern**. Example:
  "for each impact on a `Requirement`, produce a `TestCase` change impact linked by `verifies`".

An `expects` is **compiled into a CEL condition** (`expect:<action>`) automatically added to the action's effects.
Thus the link on the domain axis is simultaneously the action's **specification**, its **success
criterion**, and a **plannable effect**. After execution, the engine re-evaluates: if the promised effect is not
observed, the step is marked `effectsMet=false`; after 2 failures the action is **disabled** for this
process and the planner replans toward another action producing the same effect (e.g. fallback
`identify_impacts` (LLM) → `select_impacts` (human)). If no plan exists, the process goes to `stuck`.

Executor types:

| Kind | Execution | Output |
|---|---|---|
| `llm` | Prompt (Go template) + blackboard context → Model Gateway, structured JSON output | ChangeItems |
| `tool` | Call to a tool (`<mcp>/<tool>`) of the MCP hub, through the organization binding (§3.9) | Artifact `{tool, result}` |
| `human` | Creates a task; the process moves to `waiting` until `SubmitHumanInput` | Submitted items |
| `builtin` | Registered Go function: `graph.propagate` (impact propagation), `graph.apply` (change application) | ChangeItems / new baseline |

LLM/human outputs use a simplified input format (`engine.ItemInput`): nodes are
designated by their **key** (`REQ-1`), items from the same batch by `#ref`, existing items by `@<id>`;
the engine resolves them into exact `NodeRef`s of the reference baseline.

### 2.5 Intent loop

Before any planning:

1. the user expresses a request ("the payment provider is changing its API, what does that break?"),
   optionally specifying the methodology and/or the agent;
2. the **Ranker** (LLM via the Model Gateway, or lexical in dev) ranks the **(agent, goal)** pairs of
   the methodology — or of **all published methodologies** if none is specified — with a
   confidence score (agent and goal description and examples);
3. if `confidence(top) ≥ threshold` and sufficient margin over the second → goal selected, `change.goal` set;
4. otherwise → **clarification question** (generated from the candidate goals), process moves to `clarifying`;
   the answer is added to the history and the loop repeats (max N rounds);
5. the selected goal may require **parameters** (e.g. the starting node): these are extracted into `vars`.

### 2.6 Execution loop (Process)

```
        ┌─────────────┐
        │   intent    │── clarification ──► (waiting for user)
        └──────┬──────┘
               ▼
   ┌──► observe: hydrate blackboard, evaluate conditions ──► goal reached? ── yes ──► completed
   │           │ no
   │           ▼
   │    plan: A*(WorldState, actions, goal) ── no plan ──► stuck
   │           │
   │           ▼
   │    act: execute the plan's 1st action
   │           │   (human / approval → waiting; error → retry/failed)
   │           ▼
   └──── record items + step (NATS event)
```

### 2.7 Change application and permissions

Actions never modify the domain directly: they feed the change, and the actual
transformation only happens when the change is **applied** ([ADR 0004](adr/0004-change-application.md)).
This application is itself a plannable action, `builtin: graph.apply`, which carries a **permission**:

```yaml
- name: apply_change
  kind: builtin
  builtin: graph.apply
  pre: {reviewed: true, applied: false}   # only after review
  effects: {applied: true}                # applied: change.status == "applied"
  permission: change:apply
```

- The process remembers its **initiator** (identity propagated by the gateway: `X-Goap-Subject/Org/Roles`).
- If the initiator holds the permission, the action executes automatically.
- Otherwise, the process moves to `waiting` with an **approval** task (`pending.kind = approval`).
  An authorized person calls `ApproveAction`: if they approve, the action executes with their identity
  (`step.approvedBy`); if they refuse, the action is disabled for the process and the planner
  looks for another path (generally: `stuck`).
- Any action can carry a permission, not just `graph.apply`.

Permissions are decided **ABAC**-style by Casbin (§2.8): the resource is the change, with the
organization and the owner (= the initiator) as attributes. The default policy applies the
**four-eyes principle**: an approver applies changes from their own organization, never their own.

### 2.8 ABAC access control (Casbin)

All access decisions go through a [Casbin](https://casbin.org) enforcer with an
**ABAC** model ([ADR 0020](adr/0020-access-control.md)). A policy rule is:

```
p, <rule on attributes>, <resource type | *>, <action | *>, <allow | deny>
```

| Attribute | Content |
|---|---|
| `r.sub` | caller: `Subject`, `Org`, `Roles` (from the JWT, propagated by the gateway) |
| `r.obj` | resource: `Type`, `ID`, `Org`, `Owner`, `Name` |
| `r.act` | action: `read`, `start`, `submit`, `write`, `publish`, `delete`, `apply`… |

Functions available in rules: `hasRole(r.sub, "x")`, `hasAnyRole(r.sub, "a", "b")`,
`isAnonymous(r.sub)`. A matching `deny` overrides any `allow`.

Default policies (compiled in `pkg/authz`; seeded as `Policy` nodes at the first start, and applied as they are while the graph holds none):

| Rule | Resource | Action |
|---|---|---|
| `hasRole(r.sub, "admin")` | `*` | `*` |
| `!isAnonymous(r.sub) && (r.obj.Org == "" \|\| r.obj.Org == r.sub.Org)` | `*` | `read` |
| `hasAnyRole(r.sub, "contributor", "methodologist", "approver") && r.obj.Org == r.sub.Org` | `process` | `*` |
| `hasRole(r.sub, "methodologist") && r.obj.Org == r.sub.Org` | `methodology` | `*` |
| `hasRole(r.sub, "approver") && r.sub.Org == r.obj.Org && r.sub.Subject != r.obj.Owner` | `change` | `apply` |
| `hasRole(r.sub, "release_manager") && r.sub.Org == r.obj.Org && r.sub.Subject != r.obj.Owner` | `release` | `deploy` |

- There is no IAM service: who may do what is **graph data** ([ADR 0020](adr/0020-access-control.md)). A rule is a
  `Policy` node and a caller a `User` node (profile, roles, `member_of` a unit) of the `organisation` domain, changed
  through changes like any node and edited in the frontend's "Access" screen. A rule is validated (compilation + trial
  evaluation) when read.
- Every service builds its `authz.Authorizer` in process (`pkg/access`) from a snapshot of the head of the
  `organisation` namespace's `main` (the graph service over its own graph, the others through a graph client),
  rebuilt when that head moves (looked at once per second).
  The roles of the `User` node of the subject are added to those of its token, and its unit is its organisation when the
  token names none (the gateway does the same before propagating `X-Goap-*`; `GET /api/whoami` returns the result).
- Lock-out protection: the administrator rule is a compiled-in **floor** checked before the policies, so no stored
  policy (not even a `deny`) can take administrators out. Changes to `Policy` and `User` nodes need `policy:write`
  checked against the floor alone (`graphsvc.Handler.Floor`); direct writes of these nodes are refused. While the graph has
  no policy, or cannot be read, the compiled-in defaults apply.
- Enforcement points: engine (start / respond to / submit / read a process, action permission,
  approvals), registry (write / publish / delete a methodology or a domain), graph (policies and users, lifecycle transitions, object creation).

Replanning at every step makes the engine robust to non-deterministic actions (LLM) and to concurrent
modifications of the blackboard (a human can add an impact during execution).

Foundational decisions: [ADR 0001 — blackboard = change axis](adr/0001-change-blackboard.md),
[ADR 0002 — CEL conditions](adr/0002-conditions-cel.md), [ADR 0003 — version-to-version links](adr/0003-version-to-version-links.md),
[ADR 0004 — change application](adr/0004-change-application.md).

### 2.9 Agents and planners

A methodology declares **agents** (Embabel): `{name, description, examples, planner, model, actions, goals}`.
With no agent declared, an implicit `default` agent (all actions, all goals, `goap`) is used.

| Planner | Choice of next action |
|---|---|
| `goap` | A\*: sequence of actions of minimal cost reaching the goal |
| `utility` | the applicable action (preconditions true, effects not yet reached) with the greatest **utility**; no lookahead |
| `hybrid` | A\* where each action's cost is divided by its utility (CEL): the goal is reached while favoring useful actions |
| `llm` | the LLM (`agent.model`) picks the next action directly, given the world state, goal and candidate actions |
| `llm-scoring` | the LLM scores each candidate action's utility; same A\* cost/utility reweighting as `hybrid` |

An action's **utility** is a numeric CEL expression (`utility`), evaluated each cycle on the
blackboard (default: 1; a utility ≤ 0 excludes the action). Example:
`has(vars.review) && vars.review == "human" ? 0.1 : 0.9`.

**Agent model** (ADR 0021): `agent.model` is an LLM alias, required when `planner` is `llm` or
`llm-scoring` (the planner itself calls the model each planning cycle, distinct from any `kind: llm`
action's own `model`). Authors only ever name an alias — never a raw `provider/model` — administrators
alone decide what an alias resolves to. Publishing a methodology that references an alias with no
`platform@LlmAlias` node yet opens a pending change proposing an empty stub for one, so an
administrator sees it in the change-review queue and configures it.

**Sub-agents**: an action can call `ctx.runAgent(name, intent)`. The sub-agent is a child
process (`parentId`) of the **same methodology**, working on the **same change** (shared blackboard)
with the initiator's identity. If it completes, the action resumes with its result; if it is waiting on a human,
the parent action is **suspended** (`pending.kind = agent`) then replayed when the child completes — since
writes are only committed at the end of an action, replay is safe and finds the sub-agent already started.

**Specialization** ([ADR 0009](adr/0009-branches-options-decisions.md), complementary decision 5): an action can specialize
another one (`specializes: <action>` or `<methodology>/<action>`), with a CEL guard `when` and a
`priority`. A specialization is not planned: it inherits the specialized action's preconditions, effects, and
cost, and **replaces it at execution time** when its guard is true (the highest priority wins, including
those from other methodologies). An action of `kind: abstract` has no implementation of its own: it requires
an applicable specialization (e.g. `build` specialized into `build_java`, `build_c`, `build_shell`).
**Subtyping** (complementary decision 6): a node type can extend another (`extends`); conditions see
`x.types` (the type and its ancestors): `"alm@Requirement" in n.types` holds for its subtypes.

**Node types** ([ADR 0012](adr/0012-node-types.md)): a node's type is a qualified reference `<namespace>@<NodeType>`
(`Node.Type`), the namespace being the domain that declares the type ([ADR 0013](adr/0013-domains.md)). Every reference
to a type or a link type (in a domain, a methodology, CEL, `expects`) is qualified; `Node.Type` is composite
(namespace + node type key). The **registry is the reference**: it resolves each type of the published domains
([ADR 0023](adr/0023-definitions-in-the-graph.md)) to its properties, `extends` chain, lifecycle, validators, document,
change control, search declarations and editor, and checks the references of a methodology or a domain when saving
and publishing. The graph and the engine hold an in-memory copy (the **type catalogue**), loaded from the registry at
startup and kept in sync with its events, plus the built-in domains `methodology`, `organisation` and `platform`. The graph judges
a change by the catalogue in force and refuses a node whose type it does not resolve. Nothing is projected onto the
graph.

A node type may name the **editor** of its nodes in the IDE (`editor: agent`, inherited through `extends`,
[ADR 0027](adr/0027-node-type-editors.md)): the IDE opens every node through `openNode` (`web/src/lib/nodeEditors.ts`),
which reads the editor from the type catalogue the IDE loads from the registry (`ListTypes`) and falls back to the default node editor; the editors a type can name
are registered in `web/src/lib/views/nodeEditors.ts` (`agent`, `action`, `methodology`, `domain`, `unit`, `mcp`, ...).

### 2.11 Agent triggers

Outside the intent loop, an agent can be executed **automatically** by triggers
declared on the agent (`agents[].triggers`):

| Field | Role |
|---|---|
| `type` | `event` or `schedule` |
| `event` + `filter` | `change.created`, `change.applied`, `change.item_added`, `process.completed`, `process.failed`, `process.stuck`, `methodology.published`; CEL filter on `event` (`event.change.*`, `event.process.*`) |
| `schedule` | cron expression (5 fields, UTC) |
| `goal`, `intent` | targeted goal (otherwise identification is limited to the agent) and intent text |
| `target` | `new_change` (new change on the latest baseline) or `event_change` (the event's change) |
| `roles` | roles of the **service identity** `system:trigger:<methodology>/<agent>/<trigger>` (ABAC) |

Safeguards: a trigger never reacts to its own productions (the change it opens is marked
`data.trigger`, the process carries `trigger`), and it does not run more than once every 2 s.
Events come from NATS (graph, engine, registry services) or the local bus in all-in-one mode.
`ListTriggers` / `FireTrigger` expose the state and manual firing (permission `trigger:fire`).
With several engine replicas, only one must run the triggers (leader election: M1).

### 2.12 Execution journal and self-observation ([ADR 0011](adr/0011-journal-auto-observation.md))

The execution of a change is **captured on the change axis**, alongside the blackboard:

```
change CR-42
 ├─ items (blackboard) ── item.execution ──┐
 └─ journal                                ▼
     process.started · tick (world, plan, replanned?) · action (specialization, effects, items,
     tokens, LLM / tool calls, traceId/spanId) · approval · process.ended (status, totals)
```

- A step can be **relaunched**: the blackboard is an append-only log, the relaunch opens a *flow branch*,
  the outputs of the step and of what followed are marked stale, a replanned run appends candidate items,
  and a human adopts or discards the branch ([ADR 0017](adr/0017-flow-branches.md)).
- Before every cycle the engine **validates the blackboard**; on inconsistencies the process waits and proposes
  to restart from the earliest step that produced faulty content (ADR 0017).
- A step is a transition of the blackboard: the `action` record carries `boardBefore` / `boardAfter`
  (item count of the change around the step), `reads` (node versions the step started from) and `items`
  (what it produced), see [ADR 0015](adr/0015-namespaces.md).
- Every planning tick and every action execution (LLM or formal) is persisted and linked to the
  facts and change impacts it produced (`execution`): **auditing** can trace back from a node version to the model
  call that produced it, and to the
  corresponding OpenTelemetry span.
- The **methodology is graph data** ([ADR 0023](adr/0023-definitions-in-the-graph.md)): a journal record names the
  version executed (`methodology@version`), i.e. a set of definition nodes of the `methodology` namespace whose
  version history is the history of each element.
- The **`methodology-improvement/observer`** agent triggers at the end of every root process
  (completed, failed, or stuck): it analyzes the journal and traces (pain points: loops, failures, costly
  or systematizable LLM calls, replans, slow spans), proposes modifications as change impacts **on the
  definition nodes of a new draft version** of the methodology (specialization by script, model, cost, agent) and MCP
  tool requests, submits them for human review, and saves the draft with the accepted ones
  ([ADR 0011](adr/0011-journal-auto-observation.md) §2).

### 2.10 Script actions and DSL

`kind: script` actions are **JavaScript** (goja) or **Go** (yaegi) code entered in the IDE. The engine
injects a `ctx` object (same API in both languages, reference: [docs/dsl.md](dsl.md)):

- **reading** the blackboard (hydrated items) and the reference **domain** (`node`, `nodes`, `links`);
- **writing** to the change (`addImpact`, `proposeNode`, `proposeUpdate`, `proposeLink`, `addArtifact`,
  `decide`, and the change impact calls `impactNode`, `createNode`, `writeNode`, `reviewNode`, ADR 0024) — buffered, applied at the end of the action;
- **platform calls**: `llm` / `complete` (model gateway), `runAgent` (sub-agents), `callTool` (MCP), `log`.

The interpreters expose neither files, network, nor processes (Go: subset of the stdlib;
JavaScript: no `require`), with a timeout. The code runs in the process's **sandbox** (§3.6).

### 2.13 Algorithms: the DSL plugged into the domain ([ADR 0018](adr/0018-algorithms.md))

The DSL is a generic capability tied to a **usage** (which fixes the `ctx` the code sees): `action`
(§2.10, code inline in the agent's action) and three *pluggable* usages of the domain —
`property_validator`, `transition_guard`, `transition_action`. A domain declares
**algorithms** (JavaScript or Go, with typed parameters) and **instances** (parameter values);
node types plug validator instances on their properties, lifecycle transitions plug guard and action
instances, in call order. The type catalogue (the graph's copy of the registry's model) resolves the plugged instances (like the lifecycle, ADR 0014): validators run when items are added and when a change is applied, guards and
actions when a transition is applied. Reference: [docs/dsl.md](dsl.md), IDE section *Algorithms*.

## 3. Component architecture

```
                          ┌──────────────┐
           browser ────►  │  web (Svelte)│
                          └──────┬───────┘
                                 │ HTTPS (Connect JSON / REST)
                          ┌──────▼───────┐       ┌─────────┐
                          │   gateway    │  (users and roles: graph nodes)
                          │ (Echo, authN,│
                          │  routing)    │
                          └──┬───┬───┬───┘
              connect-rpc    │   │   │
        ┌────────────────────┘   │   └─────────────────────┐
  ┌─────▼──────┐         ┌───────▼──────┐          ┌───────▼──────┐
  │  registry  │◄────────│    engine    │─────────►│    graph     │
  │ (methodol- │         │ (processes,  │          │ (domain +    │
  │  ogies)    │         │  planning,xN)│          │  change)     │
  └────────────┘         └──┬────────┬──┘          └──────────────┘
                            │        │
                    ┌───────▼──┐  ┌──▼──────────┐   ┌──────────────────────────┐
                    │ modelgw  │  │ mcp         │   │ sandboxes (1 / process)  │
                    │ (LLMs)   │  │ connector   │   │ goap-runner : JS / Go    │
                    └────┬─────┘  └─────────────┘   │ ◄── jobs ── engine       │
                         ▼                          │ ── RuntimeService ──►    │
             Anthropic / OpenAI-compatible / Ollama… └──────────────────────────┘

  Cross-cutting: PostgreSQL (schema per service) · NATS JetStream (events) · Vault (secrets)
               OpenTelemetry → collector → Jaeger (traces) / Prometheus (metrics) / Grafana
```

| Service | Responsibility | API | Persistence | Status |
|---|---|---|---|---|
| **gateway** | Single entry point, authentication (JWT/OIDC), routing to services, CORS, rate-limit | Echo HTTP, Connect reverse proxy | — | 🟢 core |
| **registry** | Methodologies (graph data) and domains (its database), ADR 0023: editing (draft), validation, publishing, versions, the type catalogue, YAML import/export | Connect `registry.v1` | `registry` (`domain_version`) + the graph | 🟢 |
| **engine** | Intent loop, planning, process execution; deployable as a cluster | Connect `engine.v1` | `engine` | 🟢 core (memory) |
| **graph** | Domain axis (versioned nodes, links, baselines) + change axis (Changes, change impacts, facts, apply) | Connect `graph.v1` | `graph` | 🟢 |
| **modelgw** | Multi-provider / multi-model abstraction, aliases (`default`, `fast`, `reasoning`), administered catalog with global token quotas and required roles (see below), traces | Connect `model.v1` | `modelgw` (token usage; the configuration is graph data, ADR 0021) | 🟢 core |
| **indexer** | Node index ([ADR 0026](adr/0026-node-index-and-search.md)): follows the node / baseline events of the graph, embeds through modelgw (alias `embed`), answers hybrid full-text + semantic searches with facets, filtered by ABAC | Connect `index.v1` | `index` (tsvector + pgvector; FTS5 + exact cosine scan in SQLite) | 🟢 |
| **mcp** | MCP hub: connector registry (self-registration), resolution of the adapters and their restrictions (graph) along the organisation hierarchy, tool calls, the built-in connectors of the platform (§3.9) | Connect `mcp.v1` | `mcp` | 🟢 |
| **connector-\*** | One service per real system (`connector-localfs`, ...), registers itself with the hub | Connect `connector.v1` | — | 🟢 localfs |
| **goap-runner** | Sandbox for executing script actions (one per process) | Connect `runtime.v1` (SandboxService) | — | 🟢 |
| **otel-collector** | OTLP reception, trace export (Jaeger) and metrics (Prometheus) | OTLP | — | 🟢 |
| **vault** | Secrets (LLM API keys, MCP credentials, DSN) | HashiCorp Vault KV v2 | — | 🟢 dev mode |

### 3.1 Communication

- **External**: HTTP via Echo on the gateway. The frontend uses the **Connect JSON**
  protocol (`POST /goap.engine.v1.EngineService/StartProcess`), avoiding any code generation on the web side.
- **Synchronous inter-service**: **connect-rpc** (HTTP/2 h2c internally, HTTP/1.1 compatible). Contracts in
  `proto/`, generated by `buf` into `gen/`.
- **Asynchronous**: **NATS JetStream**. Subject conventions:

| Subject | Emitter | Content |
|---|---|---|
| `goap.process.<id>.started` / `.step` / `.waiting` / `.completed` / `.failed` | engine | `ProcessEvent` |
| `goap.change.<id>.item_added` / `.applied` | graph | `ChangeEvent` |
| `goap.node.<ns>.<type>.<id>.written` | graph | `NodeEvent`: one node version written (any branch), with its searchable text and facets resolved from the `search` declaration of its node type |
| `goap.baseline.<branch>.advanced` | graph | `BaselineEvent`: diff with the parent baseline (the `main` facet of the index follows the head of each namespace's own main) |
| `goap.registry.methodology.published` | registry | name + version |
| `goap.engine.work` (work queue) 🟡 | engine | process tick to execute (clustering) |

### 3.2 Engine clustering 🟡

Target: a process's state is persisted (`engine` schema); each execution step is triggered by a
message on a **work-queue** JetStream stream (`goap.engine.work`, key = processId). Any
replica consumes the message, locks the process (`SELECT … FOR UPDATE SKIP LOCKED` or an advisory lock),
executes **one** step, persists it, and republishes a tick if the process is not terminal. Steps are
idempotent (key `processId/stepIndex`). The initial core uses an in-memory `ProcessStore` behind
an interface, replaceable with the PostgreSQL implementation without changing the engine.

### 3.3 Persistence

- **Local without containers**: a single SQLite file shared by `goap-dev` (migrations `migrations_sqlite/`
  per component, [ADR 0010](adr/0010-local-sqlite-mode.md)).
- **Dev**: one PostgreSQL instance, **one schema per service** (`graph`, `engine`, `index`,
  `modelgw`, `mcp`) and a dedicated role per service (`deploy/postgres/init.sql`).
- **Prod**: one database (or cluster) per service; only the DSN changes (`GOAP_DB_DSN`, read from Vault).
- Migrations embedded in each service (`embed.FS`), applied at startup (advisory lock).

`graph` model (simplified):

```sql
node(id uuid, key text, type text, latest int)
node_version(node_id, version, props jsonb, deleted bool, change_id, created_at)  -- PK (node_id, version)
link(id uuid, type, from_id, from_version, to_id, to_version, props jsonb, change_id)
baseline(id uuid, name, namespace, branch, parent_id, change_id, created_at)
baseline_entry(baseline_id, node_id, version)
branch(namespace, name, parent, fork_baseline, head_baseline, origin, status, created_at)  -- PK (namespace, name)
change(id uuid, title, intent, status, baseline_id, goal, methodology, result_baseline_id, data jsonb)
change_item(id uuid, change_id, kind, type, status, target_id, target_version, payload jsonb,
            produced_by, derived_from uuid[], created_at)
```

> Why PostgreSQL and not a graph database? The required traversals (neighborhood, impact propagation
> at bounded depth) are expressed as recursive CTEs; version-to-version versioning and baselines are
> simpler in a relational model; only one engine to operate. A projection to a graph database remains
> possible via NATS events.

### 3.4 Security

- The gateway validates the JWT (OIDC in prod, HS256 signed with a Vault secret in dev) and propagates
  `X-Goap-Subject`, `X-Goap-Org`, `X-Goap-Roles` to services (internal network only).
- Action permissions: see §2.7. The engine trusts the `X-Goap-*` headers: it must only be
  reachable via the gateway (which systematically overwrites them).
- Each resource (methodology, changeset, process) belongs to an **organization**: multi-tenant isolation
  via `org_id` in all tables (organisation units of the graph).
- Secrets are never in environment variables in prod: `internal/platform/secrets` reads from Vault
  (KV v2, token auth in dev / Kubernetes auth in prod) with a fallback to the environment in dev.

### 3.4b Node index and search ([ADR 0026](adr/0026-node-index-and-search.md))

A node type declares the searchable properties of its nodes (`search: [{property, text, facet}]`, inherited through
`extends`). The graph observes every committed transaction (`Graph.Observe`) and publishes one `NodeEvent` per node
version written and one `BaselineEvent` per baseline; the **indexer** consumes them with a durable JetStream
consumer, embeds each document through `model.v1 Embed` (alias `embed`; no embedding model means text only) and
upserts it, idempotently, on `(node, version)`. Every version of every branch is indexed; the built-in facet `main`
(kept from the baseline diffs) restricts a search to the head of `main`, and `namespace`, `type`, `state`, `branch`
and the declared facets are counted per search.

`Search` merges the full-text and vector rankings (reciprocal rank fusion, vector matches under a cosine floor are
dropped), then filters the candidates by ABAC (`read` on the node type in its namespace) *before* counting the facets
and paging. `Reindex` empties the index and has the graph publish everything again (`RepublishIndex`).

| | PostgreSQL | SQLite (`goap-dev`) |
|---|---|---|
| Full text | `tsvector` + GIN | FTS5 |
| Vectors | pgvector (HNSW created for the model's dimension) | float32 BLOB, exact cosine scan |
| Facets | `jsonb` | `json_extract` |

In `goap-dev` the indexer runs in process (the graph publishes to it directly) and the graph is published again at
each start. The compose image is `pgvector/pgvector`; the `index` schema needs `CREATE EXTENSION vector`
(`deploy/postgres/init.sql`).

### 3.5 Deployment

- `deploy/compose/docker-compose.yml`: postgres, nats (JetStream), vault (dev), all services, web,
  otel-collector, Jaeger, Prometheus, Grafana, restricted Docker API proxy (sandboxes).
- **Local mode without containers** ([ADR 0010](adr/0010-local-sqlite-mode.md)): `make devlocal` launches
  `goap-dev` (all services in one process, in-memory event bus) on a **SQLite** file
  (`.goap/goap.db`, pure Go driver) and serves the compiled IDE on http://localhost:8080. `GOAP_STORE=memory`
  (`make dev`) keeps the ephemeral mode.
- A single multi-target image (`Dockerfile`, `ARG SERVICE`), static binary on `distroless`.
- Kubernetes 🟡: one Helm chart per service (or kustomize); engine as a scalable `Deployment` (HPA on
  the work-queue stream depth), NATS via the official chart, Vault Agent Injector.

### 3.6 Sandboxed execution of actions (executor)

The engine is the **control plane** (planning, state, blackboard); it never executes the
methodologies' code ([ADR 0007](adr/0007-sandbox-executor.md)).

```
 engine ──Acquire(process)──► Pool ──Start(spec)──► Provisioner ──► sandbox (goap-runner)
   │                                                                   │
   ├── SandboxService.Execute(job, token) ────────────────────────────►│ interprets JS / Go (DSL)
   │◄──────────── RuntimeService.Call(token, llm|agents|tools|domain) ─┤
   └── buffered items, journals, suspension ◄──────────────────────────┘
```

- **One sandbox per process**, started on the first script, stopped when the process ends or after
  a period of inactivity (a process waiting on a human keeps no container).
- The sandbox knows only the engine's **RuntimeService** URL and a **per-job token** (revoked at the end
  of the job). Every outgoing operation (LLM, sub-agents, tools, domain reads) goes through the engine:
  authorized, traced, counted. No key or secret ever enters the sandbox.
- `Provisioner` adapted to each environment (`GOAP_SANDBOX`):

| Provisioner | Isolation |
|---|---|
| `inproc` | none (interpreters in the engine) — tests and development only |
| `process` | separate process, empty environment, process group killed on stop; configurable *wrapper* (bubblewrap, nsjail) and dedicated UID for real isolation on bare metal |
| `docker` | container per process: read-only rootfs, `cap-drop ALL`, `no-new-privileges`, non-root user, CPU / memory / PID limits, internal network with no Internet, optional runtime (gVisor `runsc`); Docker API via a restricted proxy |
| `kubernetes` | pod per process: `restricted` Pod Security, no service account token, seccomp `RuntimeDefault`, optional RuntimeClass (gVisor, Kata), NetworkPolicy limiting flows to the engine (`deploy/k8s/sandbox.yaml`) |

### 3.7 Observability (OpenTelemetry)

`internal/telemetry` component ([ADR 0008](adr/0008-opentelemetry-observability.md)), enabled by the
standard `OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_*` variables:

- **traces** of all calls: HTTP (Echo, gateway), Connect (client and server, W3C context propagated),
  PostgreSQL (pgx), NATS (headers), sandboxes (the runner continues the action's trace);
- **one process = one trace**: root span `process <agent>` (the `traceparent` is kept in the
  process, background executions continue it), an `action <name>` span per action;
- **LLM calls**: span `chat <model>` (GenAI semantic conventions: `gen_ai.system`,
  `gen_ai.request.model`, `gen_ai.usage.input_tokens` / `output_tokens`…) attributed to the process, the
  agent, and the action via **baggage** propagated from the engine to the model gateway; metrics
  `gen_ai.client.token.usage` and `gen_ai.client.operation.duration`;
- **tools**: span `execute_tool <name>` (`gen_ai.tool.name`);
- metrics `goap.actions`, `goap.action.duration`, `goap.tokens` (by agent / action / result).

The gateway also exposes `GET /api/status` (availability and latency of each service), displayed in the
IDE's status bar along with the user's running runs and their notifications.

In the IDE the **Changes** explorer is the single entry point for every modification: a change is the
blackboard, and the executions (agent processes, sub-agents nested) that work on it are listed under it.
Executions without a change appear in a "No change" group. The waiting / clarifying badge sits on Changes.

Counters are also **kept in the process** (tokens, LLM and tool calls per step and overall)
and displayed in the IDE, with a link to the Jaeger trace (`traceId`).

The change **execution journal** (§2.12) carries `traceId` / `spanId`: the self-observation agent
re-reads a run's trace via the Jaeger query API (`GOAP_TRACE_QUERY_URL`, links `GOAP_TRACE_UI_URL`)
to look for pain points (slow spans, tools, model calls).

### 3.9 Tools: MCP, connectors, adapters ([ADR 0019](adr/0019-organisations-mcp-connectors.md))

Three independent things, and the one place where they meet:

```
MCP        node of the `platform` namespace (`platform@MCP`): the generic usage of a tool
           by an LLM, name + tool signatures, e.g. document-repository { list, read, write }.
           Knows no connector, no adapter. Referenced by actions (`mcps:`) and agents (`mcps:`).
Connector  a service of its own wrapping a real API (connector-localfs, connector-gdrive, ...). It registers
           itself with the hub and announces its configuration parameters, secrets and the operations it
           exposes (list_dir, read_file, ...). Knows no MCP, no adapter.
Adapter    the code that makes the two work together: it implements the tools the MCP expects with the
           operations the connector exposes. An `AdapterDef` node of the `platform` namespace (usage
           `adapter`, changed through a change), for one MCP and one connector, with typed parameters. Each
           organisational unit holds an INSTANCE (an `Adapter` node of the `organisation` namespace, owned by
           the unit through `owner`): the name of the definition and the parameter values. Where everything converges.
```

The adapter code is the body of `function (ctx)`: `ctx.tool()` is the MCP tool called, `ctx.args()` its
arguments, `ctx.param(name)` a parameter of the unit's instance, `ctx.call(operation, args)` calls an operation
the connector exposes (a failure throws), `ctx.operations()` lists them, `ctx.fail(message)` rejects; it returns
the result of the tool. The template is generated from the MCP definition (one `case` per tool) and the
operations the connector exposes (the closest one is proposed), and the parameters from the connector's
configuration schema. Parameters are handed to the connector as its configuration (same name); a `secret`
parameter is a reference (`env:<VAR>` or `<vault path>#<field>`) that the hub resolves and passes to the connector
under the parameter name, and that the code can never read. Runs are bounded (30 s, 32 connector calls).

- **Resolution.** For a tool `<mcp>/<tool>` and the unit holding the change, the hub takes the adapter instance of the
  nearest unit along `unit -> parent (part_of) -> ... -> ORG-DEFAULT`, loads its algorithm from the library, and runs
  it. Several organisations working on a change (sub-changes split by owner) can therefore all have file access
  with the same MCP, the same adapter and the same connector, each with its own root directory.
- **Restrictions** ([ADR 0028](adr/0028-builtin-mcps-and-connectors.md)). An `Adapter` node also restricts the MCP for
  its unit and sub-units (`disabled`, `tools` allow-list, `deny`, `readOnly`), with or without an adapter of its own.
  The implementation is the nearest instance naming an adapter; the restrictions of every instance of the chain add
  up (allow-lists intersect), so a unit narrows what it inherits and never widens it. The hub lists and calls only the
  allowed tools; the engine plans a `tool` action only when its tool is allowed.
- **Built-in MCPs** ([ADR 0028](adr/0028-builtin-mcps-and-connectors.md)): the platform as tools, split by concern —
  `goap-graph` (read / glob / grep / links / baselines), `goap-change` (create / read / write / edit / link / retire /
  note / validate on a change; no apply), `goap-scheduler` (start / list / get processes, triggers / fire; scope
  `agent`) and
  `goap-admin` (units, users, MCPs, connectors, domains, methodologies). Their connectors
  (`internal/connectors/builtin`) run in the hub and act for the caller (per-type read authorization, the access gate
  on `User` / `Policy`, the engine's process authorization); the call context (`mcp.CallContext`: unit, change,
  process) lets `goap-change` work on the change of the calling process by default. Pass-through adapter
  definitions and instances on `ORG-DEFAULT` give them to every unit (`graphsvc.SeedBuiltins`, which keeps the
  definitions in sync with the code at every start).
- **Adding a connector is adding a service.** A connector implements `connector.v1.ConnectorService`
  (`Describe`, `Invoke`); `internal/connectorkit` does the rest: `connectorkit.Run("name", connector)` serves the
  protocol and registers the connector with the hub (`RegisterConnector`, renewed as a heartbeat within a
  lease, 30 s by default; the hub lists it live or expired). No hub configuration is needed; only
  `GOAP_MCP_URL`, `GOAP_CONNECTOR_URL` (its address as the hub reaches it) and, when set on both sides,
  the shared `GOAP_CONNECTOR_TOKEN`. `internal/connectors/localfs` is the reference implementation.
- **The hub** (`cmd/mcp`, `internal/mcpsvc`) keeps only the registry of connectors (table `connector`, PostgreSQL and
  SQLite). MCPs and adapter definitions (`platform` namespace) and adapter instances and the unit hierarchy
  (`organisation` namespace) are read from the graph — a snapshot of each namespace's own head of `main`, combined,
  rebuilt when either moves. It checks the arguments against
  the tool's schema, runs the adapter, resolves the secret parameters, passes the connector only the secrets it
  declares, and returns the result. `CheckAdapter` validates an instance before it is saved (unknown MCP, definition
  missing or of another MCP, missing or unknown parameter: error; unregistered connector, secret the connector needs: warnings);
  `AdapterTemplate` generates the code skeleton.
- **Editing.** MCPs, adapter definitions and adapter instances are nodes, created, changed and removed through changes like any
  node. The first start creates `ORG-DEFAULT`, `document-repository` and the `localfs-document-repository` adapter
  definition (`graphsvc.SeedDefaults`).
- **Scope** ([ADR 0028](adr/0028-builtin-mcps-and-connectors.md)). An MCP says where a methodology may use it:
  `action` (declared by actions), `agent` (declared by agents only, reached by their llm actions: orchestration such as
  `goap-scheduler`), or `both` (default). An action never gets an agent-scoped MCP (not planned, not callable), an
  agent never an action-scoped one; the registry reports both at validation.
- **Scheduling.** An action declares the MCPs it uses (`mcps:` on `llm` and `script` actions; a `tool` action is
  `<mcp>/<tool>`). It is available to the planner only when the unit holding the change resolves an adapter for each
  of them; otherwise it is left out (and a specialization needing one is skipped). An action can call the tools of
  the MCPs it declares and, for `llm` / `script` actions, those declared by its agent (`agents[].mcps`).
- **LLM actions** with MCPs may call the tools in a bounded loop (`MaxToolRounds`), exchanged as JSON on top
  of any model (`{"tool_calls":[...]}`, then `{"items":[...]}`); every call is journaled with its duration and
  error. Calls run with the principal of the process initiator (`tool:call` permission).
- **Web**: *Connectors* (registry, read-only), *MCPs* (platform nodes), *Adapters* (adapter definitions, with a template generated from an MCP and a connector) and *Organisation* (units; the *MCP* pane of
  a unit lists the MCPs it can use, own or inherited, built in or not, with their allowed tools, attaches or overrides
  them with an instance of an adapter definition and its parameter values, and restricts them). A new organisation
  is a new unit.
- `goap-dev` runs the hub, the built-in connectors and the localfs connector in-process (`GOAP_DEV_FS_ROOT` gives the default organisation
  an adapter instance on a directory); connectors started separately register over HTTP.

### 3.8 Voice input ([ADR 0022](adr/0022-voice-interaction.md))

The assistant accepts push-to-talk voice input. Speech-to-text runs **entirely in the browser**
(Whisper `tiny` / `base` through transformers.js in a Web Worker, WebGPU with WASM fallback;
French and English). Only the resulting text reaches the backend, through the same
`StartProcess` path as typed requests, so voice adds no server-side load, API or scaling
concern. Audio is never uploaded or persisted. Code: `web/src/lib/voice/`. Server-side
transcription (stateless unary RPC) and live conversation (ephemeral token to a realtime
provider, or gateway WebSocket) are analyzed in the ADR but not built.

## 4. Format of a methodology

Methodologies are **graph data** ([ADR 0023](adr/0023-definitions-in-the-graph.md)), edited from the frontend: a
version is a node `methodology@MethodologyVersion` (key `MV:<name>@<version>`, namespace `methodology`: scalar fields,
status) and one node per element typed by the built-in meta-domain (`methodology@Agent`, `@Action`, `@Condition`,
`@Goal`), keyed `<version key>/<kind>/<name>` and tied by `methodology@defines`. Every save, publication or deletion is
a change applied on main; editing an action versions that node only (`registrysvc.GraphStore`, through a graph
client). **Domains** are the definition of a graph (namespace, node types, link types, lifecycles, algorithms): each
graph holds them in memory (the type catalogue) to keep its data coherent, and the registry keeps their versions in its
database (`domain_version`, PostgreSQL or SQLite, `registrysvc.SQLDomainStore`). The registry validates, compiles,
publishes and emits the `goap.registry.*` events.

Lifecycle of a version: **draft** (editable, can be invalid: anomalies are returned
with their path, e.g. `conditions[2].expr`) → **published** (validated, immutable, the only one executable by the
engine) → **archived**. Modifying a published version means creating a new draft version (`CreateVersion`).

**Domains** ([ADR 0013](adr/0013-domains.md)). The object part of the model (node types, link types, lifecycles,
algorithms) lives in **domains**, one per namespace (`alm`, `organisation`, `platform`), versioned on their own (same
draft → published → archived lifecycle; `registry.v1` `*Domain*` RPCs, ABAC resource `domain`, role `methodologist`).
A methodology is the active part only (agents, actions, conditions, goals): it names its **target namespace**
(`namespace: alm`, the domain its changes act on; it replaces `domainRef`) and the types it works on with qualified
references (`alm@Requirement`, [ADR 0012](adr/0012-node-types.md)), which may point to other domains for what it reads
or links to. Consistency is
checked at save / publish against the domain versions in force: action `expects`, CEL type literals
(`"alm@Requirement" in n.types`, `l.type == "alm@verifies"`) and builtin `params.linkTypes` must resolve. A methodology
can only be published on published domains, and a domain version is refused when it would break a published
methodology (`GetDomainUsage` lists the dependents).

**YAML** is only an **import / export** format (`ImportMethodology`, `ExportMethodology`, `ImportDomain`, ...); the
files in `domains/` then `methodologies/` are imported and published at registry startup if they don't already exist
(`GOAP_DOMAINS_DIR`, `GOAP_METHODOLOGIES_DIR`; a changed file needs a new version number).
Example YAML definition (target format of ADR 0012):

```yaml
name: impact-analysis
version: 1.2.0
description: Impact analysis of a change on a requirements repository
namespace: alm                               # target namespace: the domain its changes act on
conditions:
  - name: has_impacts
    expr: changeImpacts.exists(n, n.intent == "modified")
  - name: propagated
    expr: artifacts.exists(a, a.type == "propagation")
actions:
  - name: identify_impacts
    kind: llm
    description: Identify the nodes directly impacted by the intent
    pre: {has_impacts: false}
    effects: {has_impacts: true}
    cost: 2
    prompt: |
      ...
  - name: propose_test_updates
    kind: llm
    pre: {propagated: true}
    expects:
      forEach: changeImpacts                 # x = iterated change impact
      where: x.type == "alm@Requirement"
      produce: {op: create_node, nodeType: alm@TestCase}
      link: {type: alm@verifies}             # direction: out (new -> target) by default
goals:
  - name: assess_impact
    description: Measure the impact of a change without modifying anything
    examples: ["what does this break", "what is the impact"]
    pre: {propagated: true}
```

See `methodologies/examples/impact-analysis.yaml` for the full executable example, and
`methodologies/methodology-improvement.yaml` (self-observation: abstract action specialized by rules
or an LLM). Action specialization fields: `specializes`, `when`, `priority`, `kind: abstract`;
node type subtyping: `extends`. An `incremental: true` action reaches its effects across several
executions: an execution that produces items without reaching them is **progress**, not a failure.

### 4.1 SDLC methodology on the ALM domain (`methodologies/sdlc.yaml`, version 0.3.0, on the shared `alm` domain of `domains/alm.yaml`)

ALM domain (demo data: `internal/graphsvc/seed.go`):

```
Need ◄─satisfies─ Requirement ◄─realizes─ Function ◄─implements─ Component ◄─built_from─ BuildArtifact
  ▲               (Functional / NonFunctional ⊃ Security)            │  ▲  depends_on / accesses ─► Data
  └─addresses─ Solution ─includes─► Application ─composed_of─────────┘  └───── deploys ───────────┘
                                        ▲  ▲                              Data ─owned_by─► Application
                   Interface ─exposed_by┘  └─source / target─ Flow ─through─► Interface ; Flow / Interface ─carries / exchanges─► Data
TestCase ─verifies─► Requirement
Release ─releases─► Application ; Release ─contains─► BuildArtifact
Deployment ─of_release─► Release ; Deployment ─in_environment─► Environment ; Environment ─promotes_to─► Environment
                                   (dev → test → staging → prod, order property)
```

Cycle (goals, from most partial to most complete):

| Goal | Steps |
|---|---|
| `analyze_impact` | scope (LLM, human fallback) → propagation along all ALM links |
| `specify` | requirements revised / created (LLM, human fallback) → traceability to needs (script) → one test case per requirement (script, level depending on subtype) |
| `design` | allocation of requirements to functions (script) → design of components / interfaces / flows / data (LLM, human fallback) → consistency check (script) |
| `build_components` | **abstract and incremental** `build`, specialized by technology: `build_java` (Maven, JDK 21), `build_c` (gcc/make, cppcheck), `build_shell` (shellcheck, bats), `build_generic`; each execution builds the components of one technology, creates the `BuildArtifact`s and the `built_from` / `deploys` links |
| `release` | one **release** per application receiving a new artifact (next minor version, `releases` / `contains` links) and a fixed environment chain (`release_plan`) → release note → review → **abstract and incremental** `deploy`: one wave per environment, specialized by stage — `deploy_auto` (dev, test: unit tests, smoke tests), `deploy_staging` (regression, performance, security, test case campaign), `deploy_production` (change window, rollback; permission `release:deploy`: a release manager, never on their own change); each wave creates `Deployment` nodes |
| `deliver` | the whole cycle, then `graph.apply` (permission `change:apply`): the reference repository receives requirements, design, artifacts, releases, and deployments |

Agents: `analyst` (goap), `architect` (hybrid), `builder` (goap), `release_manager` (goap), `delivery`
(goap, the whole cycle). Review covers content; deployments, recorded after it, do not
require it. The permission for a specialization (e.g. `deploy_production`) is checked before its
execution: without it, the process waits for approval from an authorized person. Since the
"each element …" conditions hold on an empty set, the design, build, and delivery actions also require
specified requirements to anchor the cycle. The self-observation agent applies to its own runs just like
to any other methodology.

## 5. Repository organization

```
cmd/<service>/main.go        entry points (gateway, registry, engine, graph, modelgw, indexer, mcp)
cmd/goap-dev/                all-in-one for local development (memory or SQLite, serves the IDE)
cmd/goap-runner/             sandbox for executing script actions
internal/platform/           config, logs, HTTP/Connect server, NATS, Postgres, Vault secrets
internal/<service>/          Connect handler implementation for a service (graphsvc, registrysvc…)
internal/identity/           caller identity (headers set by the gateway)
pkg/domain/                  graph model (domain axis + change axis)
pkg/graph/                   Store (memory, PostgreSQL, SQLite), apply, branches / merge / rebase, execution journal
pkg/observe/                 run cost analysis (journal + traces) and improvement proposals
pkg/goap/                    A* planner
pkg/condition/               CEL compilation/evaluation, `expects` compilation
pkg/intent/                  intent loop (lexical Ranker, LLM Ranker)
pkg/engine/                  processes, agents and planners, action executors, DSL host, sub-agents, events
pkg/dsl/                     script action DSL (ctx API, JavaScript and Go interpreters)
internal/sandbox/            sandbox pool, provisioners (process, docker, kubernetes), RuntimeService, runner
internal/telemetry/          OpenTelemetry: exporters, interceptors, process / action / LLM / tool spans
pkg/methodology/             methodology model, validation (localized anomalies), compilation, YAML import/export
pkg/authz/                   ABAC: identity, requests, Casbin model and enforcer, default policies
pkg/llm/                     completion and embedding contracts (implemented by internal/modelgw)
pkg/index/                   node index: hybrid search, facets, stores (memory, SQLite FTS5, PostgreSQL pgvector)
proto/                       connect-rpc contracts (buf)
gen/                         generated code (committed)
methodologies/               example methodologies (active part)
domains/                     domains: seeded at startup (alm), builtin/ embedded and frozen except domain (one per namespace)
deploy/                      compose, postgres init, otel collector, prometheus, grafana, k8s (sandboxes)
web/                         Svelte frontend
docs/                        architecture, ADRs
```

## 6. Roadmap

| Step | Content |
|---|---|
| **M0 — foundation** 🟢 | Doc, domain/change model, A\* planner, CEL conditions, intent loop, engine (memory), graph (memory + Postgres), registry, modelgw (fake + Anthropic + OpenAI-compatible), gateway, compose, minimal UI |
| **M1 — engine persistence** | PostgreSQL `ProcessStore`, JetStream work-queue, crash recovery, multi-replica |
| **M2 — IAM** | organizations, users, OIDC, `org_id` isolation in the graph, ABAC on the graph service |
| **M3 — MCP** ✅ | MCP hub, connectors as separate self-registering services, MCPs and adapter instances as graph nodes, adapter definitions as platform nodes, resolved along the organisation hierarchy, `tool` actions and LLM tools, scheduling filter, secrets via Vault · remaining: more connectors, adapter validation as a node type algorithm |
| **M4 — advanced change axis** | impact propagation (recursive CTE parameterized by link types), suspect links, baseline diff, merge/rebase of concurrent changesets |
| **M5 — UX** | ✅ methodology editor (forms, localized anomalies, publishing, versions, YAML import/export), "Access" screen (ABAC policies), approvals · remaining: graph and plan visualization |
| **M6 — K8s** | Helm charts, engine HPA · ✅ OpenTelemetry observability, sandbox manifests |
| **M8 — branches and decisions** 🟡 | ADR 0009 (partially implemented) · ✅ graph: per-branch versions, 3-way branch merge, change branches merged at apply (`merge_pending` + `MergeChange`) · remaining: earlier conflict detection and merge proposals by agents, change budget, options explored as branches, comparison, decision loops (questions → analyses), merging the chosen option; then versioned containers and releases |
| **M9 — self-observation** ✅ | ADR 0011: execution journal on the change axis (ticks, actions, LLM / tool calls, decisions, item provenance), `observer` agent (journal + OpenTelemetry traces → findings → proposals → review → draft), action specialization and type subtyping |
| **M10 — SDLC** 🟡 | `sdlc` 0.4.0 methodology on the `alm` namespace (ALM domain) (need → requirement → function → component → artifact → application → solution, data, interfaces, flows), build specialized by technology, incremental releases and deployment (dev → test → staging → production, release manager approval), incremental actions · to refine: quality (coverage, security), rollback, freezes / change windows, MCP tools (repositories, CI, artifact registry, deployment) |
| **M7 — agents** ✅ | agents (goap / utility / hybrid), JS / Go script actions with DSL, sub-agents, sandbox per process, IDE |
| **M11 — node types** ✅ | ADR 0012 / 0013 / 0023: qualified type references `<namespace>@<NodeType>`, one domain per namespace, registry as the reference of the types, with an in-memory catalogue in the graph and the engine kept in sync by its events and an existence rule, methodologies as nodes of the `methodology` meta-domain, domains in the registry's database; no `NodeType` projection, no `M:` / `D:` elements, no `Def*` types |

## 7. Open questions

1. **Condition granularity**: should conditions be *parameterized* (per node) rather than global?
   Classic GOAP reasons over global booleans; a condition per node would blow up the state space.
   Proposal: quantified global conditions (`all`/`exists`) + actions that iterate internally.
2. **Action expectation**: is `expects` enough to express every expectation, or is a real graph
   pattern language needed (a restricted Cypher-like language)?
3. ~~**Concurrency on a baseline**~~ → [ADR 0009](adr/0009-branches-options-decisions.md): detection on the
   base version per branch, 3-way merge at apply, conflicts resolved by a human (`merge_pending`).
4. **Action cost**: static (declared) or dynamic (estimated tokens, observed latency)?
5. ~~**Human decisions**: mandatory validation or a separate `apply`?~~ → settled by [ADR 0004](adr/0004-change-application.md):
   `apply` is a plannable action conditioned by review and protected by a permission.


## Model gateway administration (platform settings)

Administrators (`admin` on the `platform` resource, Casbin) open **Platform settings** from the gear in the IDE
status bar. The gateway configuration is **graph data** ([ADR 0021](adr/0021-model-configuration-in-the-graph.md)):
`LlmProvider`, `LlmModel` and `LlmAlias` nodes of the `platform` namespace (`pkg/llmcfg`, the built-in `platform` domain), changed
through changes like any node (the screen writes them with `web/src/lib/llmEdit.ts`). The gateway reads a snapshot of the
`platform` namespace's head of `main` and rebuilds its router when it moves. `GOAP_MODELS_CONFIG` / env keys only seed a graph that holds no provider
(`graphsvc.SeedModels`). The `modelgw` database keeps only the token usage (`llm_usage`, keyed by the key of the model node).

- **Providers** are pluggable: a `Protocol` (`anthropic`, `openai`, `gemini`, `fake`; `internal/modelgw/protocols.go`)
  knows how to complete and how to list models; a `Kind` is a preset on a protocol (Anthropic, Mistral AI, Google
  Gemini, OpenAI, custom OpenAI-compatible). Adding a provider type = `RegisterProtocol` / `RegisterKind`.
- **API keys** are never stored: a provider node holds `apiKeyRef` (`env:<VAR>` or `<vault path>#<field>`, alternatives
  separated by `|`) that the gateway resolves (`platform.Secrets.Resolve`), like the secret parameters of an adapter.
- **Catalog**: `DiscoverModels` asks the provider for its models; the admin adds them to the catalog, with a global
  token quota (per day / month / total, shared by all users) and the roles allowed to call the model (none =
  any signed-in user; `admin` always). Only enabled catalog models can be called; calls without identity
  (engine, in-process) are trusted and skip the role check but still count toward the quota.
- **Aliases** (`default`, `fast`…) point to catalog models; the router follows the graph. The RPCs of the gateway are read-only
  (`ListProviders`, `ListCatalog`, `DiscoverModels`, `ListProviderKinds`, `ListModels`, `Complete`).


## Node lifecycle (ADR 0014)

A domain is composed of **node types, link types and lifecycles**. A lifecycle is a named state machine
(states with an `editable` flag, transitions with permission, CEL guard, required attributes/links and,
for documents, allowed child states) that node types name and subtypes inherit. The state is stored on
each node version. A node is modified only in an editable state, which it holds only through a change:
the change reopens it (writes a version in an editable state), edits it and moves it out of the editable states before
it is applied. Changes are attached to the nodes they modify (several changes may be attached to one node;
conflicts appear at Apply). A document type embeds nodes through `contains` links and its transitions
validate the states of its children in the result baseline. The rules live in `pkg/graph/lifecycle.go`
(checked when a change impact writes a version and again by `Apply`); the type catalogue resolves the lifecycles
(ADR 0012).
