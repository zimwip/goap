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
| **Node** | Typed content element. Its type is a qualified reference `<namespace>@<NodeType>` (`alm@Requirement`, [ADR 0012](adr/0012-node-types.md)). Stable identity `NodeID` + readable `Key` (`REQ-12`). A version of a successor node records its lineage (`Origins`, [ADR 0077](adr/0077-impact-node-operations.md), "Merge and split"): the nodes it derives from, set on its first version; `Graph.DerivedNodes` answers the other way. |
| **Version** | Each committed modification creates a new immutable version `NodeID@vN`. A version can be a *tombstone* (deletion). It is written **when its change commits** (commit), from the node's draft ([ADR 0079](adr/0079-drafts-and-versions-at-commit.md)): while a change works on a node it has no version, only a **draft** (per change impact and flow, derived from the change log, never stored). A written version is never edited. It is owned by an organisational unit, and its node was created in a project ([ADR 0054](adr/0054-structures-bootstrap-and-the-graph-guard.md)). |
| **Link** | Typed relationship **from version to version**: `REQ-12@v3 ─satisfies→ NEED-4@v2`. A link does not automatically "follow" new versions: if `NEED-4` moves to v3, the link becomes **suspect** — this is the model's native impact signal. |
| **Baseline** | Coherent set `{NodeID → Version}`, scoped to one namespace (every node in it belongs to that namespace): a "commit" of the graph. A baseline's links are those whose two endpoints are both in the baseline. Every modification starts from a reference baseline of its own namespace (the empty state before the first change) and produces a resulting baseline of that same namespace: a baseline is the state a change leaves ([ADR 0056](adr/0056-baselines-are-states-of-changes-names-are-tags.md)); a tag names one node version, and a smart tag, held by its own store, selects a set of versions ([ADR 0143](adr/0143-the-state-of-a-node-is-a-selection.md) §6, §7). The state of a baseline is a selection ([ADR 0143](adr/0143-the-state-of-a-node-is-a-selection.md)): the version of a node at a baseline is its last commit on the baseline's branch up to the baseline's order (`baseline.seq`), else, past the fork of the branch, the selection on the branch it was forked from; every baseline, the bootstrap included, stores only its header, nothing is stored or checkpointed. A view of the head (access, hub, model and methodology snapshots, `graphsnap.Head`) reads the head whole once, then only the nodes committed since (`Graph.BaselineDelta`, RPC `GetBaselineDelta`); the one way to read the graph at a commit is `graphsnap.Reader.At`, one reader per process shared by every view of it (`graphsnap.View`, which asks for the head at each read), so each namespace is held once per process ([ADR 0145](adr/0145-one-way-to-read-the-graph-at-a-commit.md)). The branch of a change holds no baseline: its state is its fork with the versions committed on it since, the last one of a node winning, and a commit writes only its versions (ADR 0140 §9). A commit and a merge never read a state whole: they read the versions of the nodes they touch (`Tx.BaselineVersions`: the selection of those nodes), find what a change's branch changed from the versions on that branch, and write a header ([ADR 0137](adr/0137-a-commit-reads-the-nodes-it-touches.md)). |

**Namespaces** ([ADR 0015](adr/0015-namespaces.md)): every node lives in a namespace, and a namespace is the content of
one domain ([ADR 0013](adr/0013-domains.md)): `alm` (delivery), `organisation`, `platform` (MCPs, adapter definitions,
model configuration), and the meta-domain `methodology` that holds the definitions of the methodologies
([ADR 0023](adr/0023-definitions-in-the-graph.md)). `methodology`, `organisation` and `platform` are **built in and
protected**: the platform reads them in its own way, so the domain registry publishes them at each start from the files
of `lib/domains/builtin/` and protects them; they change with the code (or the registry's command line) and are
read-only in the domain editor; a new namespace is a new domain published from the domain editor.
Domains themselves are not graph data: the domain registry, a service of its own, keeps them in its database ([ADR 0125](adr/0125-source-components.md) §3, [ADR 0167](adr/0167-domain-registry-a-service-of-its-own.md)). Keys are unique per namespace. A change acts on one namespace: it can
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
neighbours in both directions and the suspect links among them, `graph/graph/browse.go`). The IDE's Baseline explorer
and tab navigate namespace → baseline → node type → paged nodes, and show the neighbour graph of the selected node.
The statistics of the graph are read on demand, nothing stored (`graph/graph/stats.go`): `GetGraphOverview` sums what
each namespace holds (those holding nodes and those of every domain in force, empty ones included), `GetGraphStats` gives for one namespace the rows of the store counted by the database
(`Tx.StoreCounts`, one statement per measure in `sqlbuild.go`: nodes and versions per type, versions per reason and per
node, link rows and drops, how versions hold their link sets, joins, baselines per kind, changes per status, tags, log
entries per type) and the shape of the state at the head of a lasting branch, computed from that state (links within
it, suspect and leaving ones, degree distributions, histogram, per node type and link type with the type pairs, the
most connected nodes, weakly connected components, density, reciprocal pairs). The IDE's *Graph statistics* tool (left
bar) lists the namespaces and opens a statistics tab per namespace.
A request started without a baseline or a change (the assistant, a trigger) opens its change on the latest baseline
of the namespace its methodology acts on, known once the intent is identified.
The organisation is a hierarchy of units in its own namespace (`organisation`); nodes reference their owner
unit across namespaces, and a change is split into sub-changes along unit boundaries
([ADR 0016](adr/0016-organisation-and-sub-changes.md)). A sub-change is a change on its own branch, forked from the
branch of its parent change, recursively ([ADR 0103](adr/0103-sub-changes-on-their-own-branch-operation-rights.md),
superseding ADR 0081 and 0082): it sees what its parent committed, never its drafts, commits on its branch and merges
into its parent's like a root change into its target (rebased onto the head of the parent's branch, held by its own
guardian, a fast-forward); a parent's uncommitted draft of a node the sub-change changed holds that merge. The parent
records the merged work as accepted, committed impacts of its own (`recordInParent`) and merges it in turn: the work climbs
one level per merge. The change's overview gives a sub-change the same Commit and Merge ("Merge into parent") buttons;
its Dependencies pane (the changes it depends on, those needing it, its parent and sub-changes) draws the history of the change with its parent and sub-changes as a branch graph
(`SubChangeHistory.svelte` over `HistoryGraph`, `web/src/lib/subChangeHistory.ts`: a lane per change, a row per impact
event of its main flow, a sub-change's lane starting where its parent stood, its rebase, and the "merged" edge from its
commit of a version to the parent's; a node filter).

**Methodologies integrating methodologies** ([ADR 0106](adr/0106-methodologies-integrating-methodologies.md)): a
methodology may declare that its changes integrate the changes of others (`integrates: [{methodologies, exclusive,
attach}]`) and what it requires of them (`guardian: {merge, transitions}`, CEL with `integrator`); levels (release,
portfolio) are derived from that relation, never declared. A change of the integrator is the parent of the changes it
integrates: `AttachChange` makes an open root change its sub-change (forked again from the parent's branch, what it
committed rebased there), `DetachChange` gives it back. The guardian of a change is the **chain** of the methodologies
framing it, outermost first, saved on the change (`Change.Guardians`, computed at creation from the parent or, for a
root change, by the registry over the methodologies applicable to its project; replaced at attach / detach): a commit
or a transition needs the agreement of every guardian, its own first (`checkCommit`, `TransitionChange`; the merge is
mechanical and asks none, [ADR 0146](adr/0146-the-guardian-judges-commits-the-merge-is-mechanical.md)), the outer
ones evaluated by the registry (`ChangeLifecycles.CheckGuardians`). `methodologies/release-based.yaml` is the first
integrator (of `sdlc`, exclusive). A person attaches from the change tab (*Attach to change…*, *Detach*, the
*Guardians* row), `goap-change` (`attach`, `detach`) or the assistant's proposals (`attach_change`, `detach_change`). The
registry's validation checks the composition against the other methodologies (`compositionIssues`,
`methodology.CompositionIssues`): events a subscription waits for that its source never emits, states, transitions,
steps or conditions a filter or a guard names that do not exist, guards that never hold, alone or together along the
chain of guardians (tried over every state and integrator); warnings, each with a fix (`Issue.Hint`), shown by the
Problems console and the methodology editor (*Composition* card of the overview).

**Dependencies between changes** ([ADR 0108](adr/0108-dependencies-between-changes.md)): a change lists the changes
whose work must have reached the branch it merges into before it merges (`Change.DependsOn`,
`Graph.SetChangeDependencies`, operation `change.depend`: no cycle, no ancestor, no sub-change, no abandoned change).
A dependency is satisfied once its work reached main or the dependent's parent (two candidates of one release);
`checkMerge` refuses the merge before that. `Graph.ChangeDependencies` gives the dependencies, the changes depending on
a change and the merge order of its open sub-changes (`domain.DependencyOrder`). Conditions and guards read
`dependencies` (the library `dependencies`: `dependencies_met`, which no action produces, so a step needing it waits;
`sdlc`'s application step does), and `Engine.relay` wakes the dependents when a dependency moves (`on.from:
dependency`). Set from `goap-change` (`depend`, `dependencies`) and the change tab's *Depends on* section.

**Operations on a change** ([ADR 0103](adr/0103-sub-changes-on-their-own-branch-operation-rights.md)) are a catalogue
the code owns: each method of `graph/graph` a person performs on a change (checkout, edit, withdraw, review, commit,
merge...) carries `//goap:operation <code> <doer|reviewer>` and calls `g.allow(ctx, id, operations.<Const>)` first;
`platform/cmd/goap-opgen` (`platform/opgen`, `go generate ./contracts/operations/`) generates `contracts/operations/operations_gen.go` from the
annotations and refuses a duplicate code. A methodology's roles list the operations they allow (`roles[].operations`:
codes or kinds; none listed anywhere: nothing restricted), so a reviewer (`operations: [reviewer]`) accepts or rejects
and never edits or withdraws; the graph asks `Graph.OperationAuthorizer` (`graphsvc.OperationAuthorizer`:
`operation:perform` with the roles allowed, from `registrysvc.Service.OperationRoles`).

A **root change** is rebased the same way onto the head of the branch it was forked from ([ADR 0101](adr/0101-a-change-is-rebased-onto-its-target-before-it-commits.md), `RebaseChange` / `GetRebaseState` also serve root changes; `MergeChange` rebases it first, conflicts stop with `ErrConflict`): a clean rebase keeps the reviews, the change and its branch move to the head (one `change.updated` entry with a `baselineId` `{from, to}` field, `Branch.rebasedFrom` set once), and commit is a fast-forward, so the history of the target is linear. Since [ADR 0102](adr/0102-iterate-on-the-branch-of-a-change.md) a change commits on its branch without being rebased and is rebased when it is merged: its fork moves onto the head (no baseline: the branch of a change holds none, ADR 0140), the nodes changed on both sides merged into drafts whose version takes the target's as second parent; a change that committed nothing has nothing to rebase. Since [ADR 0165](adr/0165-a-commit-rebases-its-drafts-onto-the-head.md) the commit first rebases the drafts it writes onto the head of its target (a draft checked out again from the head's version, the difference of the three-way merge as an update, the state moved only by moves its lifecycle allows from the head's state: the moves the change recorded replayed, or one move when both sides moved it, else a `state` conflict); a conflict stops the commit and the rebase is kept, a clean one commits versions deriving from the head's, and the merge, following the lineage of each node, is a fast-forward. Since [ADR 0166](adr/0166-a-change-stays-open-until-closed.md) the merge leaves the change open: it merges what the change committed (its branch forked again from what it merged), whatever the change still works on, and `CloseChange` ends it: everything settled, the lifecycle in a final state, what is left merged, the change applied. The same sync pane (`SubChangeSync.svelte`) shows "Behind <branch> (now at <head>)", Rebase and "Keep mine" for a root change.

**Organisations** ([ADR 0019](adr/0019-organisations-mcp-connectors.md)) are the `OrgUnit` nodes above. A
change is held by one unit (`ownerOrg`; empty: the default organisation `ORG-DEFAULT`, created at the first start
and the implicit root of every unit's ancestors). The units also hold the **adapters** that implement the tools of
the platform (§3.9): a unit inherits the adapters of its ancestors, the nearest wins.

Versioning rule ([ADR 0003](adr/0003-version-to-version-links.md)): **outgoing links belong
to the source node's version**. Adding/removing an outgoing link creates a new version of the source;
a node that changes version carries its outgoing links forward; incoming links from unmodified
nodes stay on the old version and become **suspect**. Baselines thus remain immutable. The links a
version carries are held as a **link set** ([ADR 0141](adr/0141-the-links-of-a-version-are-a-set.md)):
shared as is with an earlier version when unchanged, or written as a delta (added links, drop rows)
over an earlier set: the set of order k over the one of order `k & (k-1)` (skip deltas, written whole at each power of
two), so a chain is as deep as the bits of its order and a node whose links grow never rewrites them every few sets.

#### Version branches ([ADR 0009](adr/0009-branches-options-decisions.md))

Versions are numbered **per node, across all branches** (`REQ-1@v7`), and each version carries
the `branch` it was written on (`main` by default, never changed), its `parents`, and its `reason`: `create`, `revise` (successor on the same
branch), `derive` (first version on a branch other than its base's: the common case, a change's commit of a version of main),
`merge` (a three-way branch merge, written on the target: two parents) or `rebase` (a merge seen from the change's branch:
the rebase of the change onto its parent branch, written on the change's branch; parents: the version the change committed,
then the parent branch's, [ADR 0133](adr/0133-rebase-is-a-merge-and-the-change-stack.md)). Versions are written when a
change commits (`CommitChange`, ADR 0079 / 0102), on its own branch; the merge only rebases and fast-forwards.
A version is **part of several branches** when a merge commits it as is: it *joins* the target (`node_branch`,
`Node.Joined`, [ADR 0032](adr/0032-branches-as-pointers-baselines-as-deltas.md)) instead of being copied.

```
REQ-1  v1(main) ── v2(change-A, derive) ═══ joins main (fast-forward of A)
          └────── v3(change-B, derive) ── v4(change-B, rebase ← v3 + v2) ═══ joins main (fast-forward of B)
```

- A **branch** (`CreateBranch`) belongs to one namespace (`Branch.namespace`) and starts from a baseline of that
  same namespace (`forkBaseline`), advancing through the changes applied on it (`Change.branch`); its **head**
  (`head`) is the latest baseline produced. `main` exists implicitly, per namespace.
- "Latest version" is read **per branch** (`LatestOn(node, branch)`: the highest version written on the branch or
  joined to it): applying (`apply`) a change detects a conflict when a node has advanced **on the change's branch**
  since the base version.
- **Branch merge** (`PlanMerge` / `MergeBranch`): for each node modified on the source branch since
  the fork: a node changed on the source only (`added`, `fast_forward`) **joins** the target as is, no version is
  written; a node changed on both sides gets a 3-way merge against the common ancestor (walking up `parents`) —
  property by property (one side equal to the ancestor takes the other, otherwise **conflict**) and outgoing links
  as a set (key = type + target node: added on one side → kept, removed on one side → removed), written as a
  `merge` version. A joined version keeps its links: one that targets a node that got a merge version is suspect
  (ADR 0003). The merge is a change of the platform (one change impact per merged node) on
  the target; conflicts require a resolution (resolved properties, or `skip`). The branch moves to `merged`.
  A change branch that the target did not move since its fork commits by a **fast-forward**: its versions join the
  target, nothing is copied. A change is rebased onto its target before it commits (ADR 0101), so the fast-forward is the
  normal path and the merge a race or a branch merged by hand. The baseline carries its `kind` (`fast-forward`,
  `merge`, `snapshot`, ...; a change branch holds none, so no `commit`, `rebase` or `revert` baseline is written for
  one: a withdrawn committed node is reverted by a version with reason `revert`, ADR 0140 §9) and, for a rebased change,
  `rebasedFrom` (the baseline it was forked from before, copied from `Branch.rebasedFrom`). The history tab (`BaselineBranchGraph`, `baselineHistory.ts`) shows the kind as a badge on each row,
  a "rebased" badge ("rebased onto T1, was B") with a dashed "rebased from" edge next to the solid fork edge, edges labelled
  fork / rebase / merge / fast-forward, and on a merge the change and the branch merged. Every open branch forked from the
  branch shown has a lane (ADR 0102): an open change branch is its open head, drawn as a transparent dot.
- **Change-stack** ([ADR 0133](adr/0133-rebase-is-a-merge-and-the-change-stack.md)): every node version and every baseline
  exposes the changes it is the application of, oldest first, with the lineage they are read from (`Graph.ChangeStack`,
  RPC `GetChangeStack`): for a version, its ancestors through `parents` with the change that wrote each and the changes
  whose merge made it join a branch (`node_branch`); for a baseline, the baselines it follows from (parent, merged head)
  and, for one that took a change in, the sub-changes merged into it (`BaselineStep.merged`: a change branch holds no
  baseline, ADR 0140). A sub-change names its parent. Nothing is stored: the stack is composed from what every version and
  baseline records. `DiffBaselines` also answers the changes that explain the difference (`changes_added` /
  `changes_removed`). Web: `ChangeStackView.svelte` in the node history, the Baseline tab and the comparison.
- **Views of a change** (`ChangeView`): the head of the change branch with the change impacts of a flow counted at a
  level — `written` (the drafts proposed or accepted, not rejected: what the change or an option would look like),
  `accepted` (what would commit), `committed` (the version it committed: on its own branch when committed, then on the branch it is merged into; the last one counts). Only `committed` is stored.
- **Options** ([ADR 0009](adr/0009-branches-options-decisions.md) §3, ADR 0032 §6): an option is a flow opened as a
  hypothesis (`OpenOption`); several are explored at once, each holding its own drafts of the same nodes in the change's
  log (an option is a flow of the change, not a branch of the domain, and writes no version, ADR 0079). The
  **active option** is the one the change works on: every call that names no flow goes to it
  (`Change.ResolveFlow`; `main` names the main flow), and a process on the main flow reads its graph
  (`ChangeGraph`). Two scopes (the main flow or an option, open or decided) are compared impact by impact at `written` or
  `accepted` (`DiffFlows`, [ADR 0083](adr/0083-option-diff.md); the older `CompareOptions` still serves the `goap-change` tool); an option is evaluated,
  then one is selected (its flow is adopted: its drafts become the main flow's) and the others are
  rejected. In the IDE the change editor opens on a **scope bar**: the main flow and one chip per option (status,
  the active one marked, the impacts it declared). The scope is chosen first, and the scoped panes (`<scope> ▸
  Impacts`, `<scope> ▸ Artifacts`) show the change as that flow sees it (`GetBlackboard` on the flow), each impact
  marked *this option* or *main flow*; reviews and edits made there name that flow explicitly. Looking at an
  option is local to the editor; *Work on it* moves the active pointer (where agents and calls without a flow go).
  The Impacts pane is one list: a row per impacted node (why, versions `pre → post`, state and edits, review), an
  impact is added by creating a node or picking one with the reason it is impacted, and the node link opens the
  version the change wrote in that scope (the node editor is bound to the change and the flow, its edits go there).
  Comparing, evaluating, selecting and rejecting are the **Compare & decide dialog**, opened from the scope bar (shown
  when the change has an option): a left and a right scope (by default the main flow and the option the scope bar looks at,
  else the first open option), a swap, a level, then the impacts that differ (*added* on the right only, *removed* on the
  left only, *modified* with the attribute-level changes: properties, state, owner, links), filter chips by category
  and a count of the identical impacts it does not list; a row opens the impact in the Impacts pane of its scope; the
  decide bar (evaluate, select, reject) acts on the option on the right.
  The Artifacts pane lists the artifact items of the scope; an item-level decision (`kind: decision`, accept / reject with a comment) is
  shown on the artifact it concerns, and stays in the Audit pane; item types with no pane of their own sit in a collapsed raw list.
- **Decision loops** ([ADR 0009](adr/0009-branches-options-decisions.md) §4): a **decision point** is a question the
  change must settle, usually which option (`OpenDecision`). A ruling is *decided* (an option, a confidence, a
  justification) or *undecidable* (why, and the questions to answer first).
  Open questions **block** the point; they are answered by hand or by `decision.investigate`, a builtin that runs one
  sub-agent per question, whose intent is the question (identification picks the agent in any methodology), and the
  planner comes back to the decision. When a ruling settles the point and when only a person may rule it is a
  **policy** ([ADR 0067](adr/0067-flow-origin-and-decision-policy.md)), not the graph's: the graph keeps the mechanism
  (open, rule, answer, ratify, who ruled, the statuses) and the opaque policy values the opener gave
  (`DecisionPoint.Policy`), and asks a `domain.DecisionPolicy` (`Graph.DecisionPolicy`; none: a ruling of anyone
  settles the point). The platform's policy is `contracts/decision`: an agent ruling below the point's confidence threshold
  waits for a person's **ratification**; after `maxRounds` rulings that did not settle it, or past its deadline, the
  point is **escalated**: only a person rules it (values `decider`, `threshold`, `maxRounds`, `maxDuration`). A
  decided point selects its option; `Apply` refuses a change with a pending decision. The points are facts of the main flow (`TypeDecisionPoint`, replayed by
  `Change.DecisionPointsAt(now, policy)`); actions work on them through items of type `decisionPoint` (LLM, script, human task:
  a human task's ruling is a person's), scripts through `ctx.decisionPoints()` / `openDecision` / `decide` /
  `undecidable` / `answer`, agents through the `goap-change` tools `decisions` / `decision` / `rule` / `answer`,
  people through the Decisions pane of a change (`methodologies/examples/option-decision.yaml` runs the loop).

#### Change axis

| Concept | Description |
|---|---|
| **Change** | A modification request. References a starting baseline, carries the initial intent and the chosen goal. It is **the blackboard** of an agentic process. |
| **ChangeItem** | Blackboard **fact** with no node: its `type` (the **item type**) is declared by a domain (`itemTypes:` of a domain, [ADR 0128](adr/0128-item-types-declared-by-domains.md)): the built-in methodology domain declares the change mechanism (`artifact`, `decision`, `decision_point`, `flow`, `review`, `signal`, `transition`) and the platform use cases (`risk`, `action`, `derogation`, `assessment`, `comment`, `verification`, whose code registers their validation); the graph refuses a type no domain in force declares and checks the data keys a type declares as attributes. Its `target` is what it is about within that type (what an artifact is, the name of a signal), a signal's `recipient` the process it addresses ([ADR 0119](adr/0119-item-types-in-the-domain-explorer.md)). `ListItemTypes` lists the types in force; each domain shows and edits its own under *Item types* in the domain explorer. Each item has a provenance (`producedBy` = action, `derivedFrom` = other items). |
| **ChangeImpact** | Link from the change to a node ([ADR 0024](adr/0024-change-impacts.md)): `intent` (`created` / `modified`), `rationale`, `pre` (released version), `post` (the **draft reference** `{node, version 0}` while the change works on the node, the version written once it is committed; empty while the impact of an existing node is only proposed, or while a creation is only planned: `ProposeImpact` takes existing nodes and planned creations (intent `created`, a key and a type, no draft, [ADR 0121](adr/0121-planned-creations.md)), a new node is created by `ImpactNodeCreate`, one `created` event for its impact and draft, or, for a planned key, a `checkedOut` event giving the planned impact its draft; a key is one node's: a creation of a key another open change wrote is refused at once, and a key only abandoned changes wrote, or that lives only on other lasting branches, is taken over, the node getting its next version as a creation, `takeKey`, [ADR 0151](adr/0151-git-branches-on-graph-branches.md); a merge of two lines that both created it is a three-way merge with no base), `committed` (version on the target branch once applied: the post version itself when it joined the target, a merge version when the node changed on both sides), `review` with a mandatory comment. Every node write names a change and works on a **draft** ([ADR 0076](adr/0076-checkout-working-versions.md), [ADR 0079](adr/0079-drafts-and-versions-at-commit.md)): `ImpactNodeCreate` / `ImpactNodeCheckout` give the node its draft (once per change and flow; the impact's post; `ImpactNodeCheckoutBatch` checks several nodes out in one transaction, all or none), `ImpactNodeUpdate` and the link operations (`ImpactLinkCreate`, `ImpactLinkUpdate`, `ImpactLinkDelete`) edit it, `ImpactNodeReview` accepts or rejects (always an explicit operation: an edit never reviews; an acceptance is gated by the checks of a version run on the draft and writes nothing; editing after it sends the review back to `proposed`), `ImpactNodeTransition` moves a node along its lifecycle (on the draft, which a node with none is checked out for first, the guard seeing the change, the impact and the draft); `ImpactNodeCancel` drops a draft and `WithdrawImpact` takes an impact out of the change (`WithdrawImpacts` several at once, in one transaction: all or none, the impacts tab's *Remove selected from change* over the checked rows; a node is never deleted: a creation dropped leaves no node, a node no parent holds is retired by its lifecycle). A draft is free to be incomplete (only the attributes, their types and enums are checked as it is edited); its validators, required links and link attributes are checked when its review is accepted and when it commits. The commit then the merge (every impact accepted): the commit **writes the versions from the drafts** on its own branch (the next version number, the version checked out from as parent, owner, origins, comment, links resolved to the versions of the change) after rebasing a root change onto the head of its target branch ([ADR 0101](adr/0101-a-change-is-rebased-onto-its-target-before-it-commits.md)), and commits the branch (a fast-forward; the three-way merge only when the target moved between the rebase and the integration, then `MergeChange`). A node version records its `changeId`, `changeImpact` and `comment`. A node's type is a direct attribute of the node (`Node.Type`), there is no `instanceOf` link. Everything that happens to a change is an entry of **one log** ([ADR 0030](adr/0030-one-change-log.md)): its facts, its journal records (scheduling included) and its impact events, in one order, with the type, flow branch, process and action run as columns to filter on (`ListChangeLog`). The change impacts are **event-sourced** ([ADR 0029](adr/0029-event-sourced-change-impacts.md)): every operation (`proposed` for an existing node, `created` for a new one with its draft, `checkedOut`, `transitioned`, `updated`, `cancelled`, `withdrawn`, `reviewed`, `discarded`, `adopted`, `committed`, `rebased`) is an event of the change's impact log with its caller (principal, action run, flow), its payload holding what the event changes and nothing the entry's columns hold, a checkout of a stored version naming that version rather than copying it ([ADR 0142](adr/0142-impact-events-carry-what-they-change.md), read with `domain.ImpactEventsOf`; the verdicts of one review and the versions of one commit are one entry, [ADR 0144](adr/0144-a-review-and-a-commit-are-one-entry.md)); the `change_impact` table is its projection, and the drafts are not stored at all: they are the fold of the log (`domain.FoldDrafts`), kept in a per-change in-process cache validated by the log position (`graph/graph/draftcache.go`, ADR 0079 §2); that fold is the **view** of the change every operation reads and extends, with the phases, the impact events and the moves, a change being read at a position of its log (`Change.Seq`) that a write returns in the caller's watermark and a read may require, and readable as it stood at an earlier position (`ChangeAt`, [ADR 0153](adr/0153-a-change-is-read-as-a-view-of-its-log.md)), and what a flow sees is a fold of the log (`ListChangeEvents`; the **Audit** pane of the change merges it with the execution journal and the facts into one chronological trail, with CSV / JSON export, and the whole log as W3C PROV-O provenance in JSON-LD, `ExportChangeProvenance`, `graph/prov`, [ADR 0057](adr/0057-change-log-as-prov-o.md)). The state of a namespace at a baseline is exported as a JSON snapshot without history (`ExportGraph`, `graph/graph/snapshot.go`) and imported as one new change left for review, which creates the missing nodes and brings the others to the snapshot (`ImportGraph`, [ADR 0104](adr/0104-domain-and-graph-import-export.md)); the same state is exported as an OKF tree, a zip of one Markdown file per node with a YAML front matter (`graph/okf`, [ADR 0111](adr/0111-okf-export-of-the-graph.md)); the web exports and imports them as jobs (`StartGraphExport`, `StartGraphImport`, [ADR 0149](adr/0149-a-job-service-for-long-runs.md)), the file a job writes saved when it is done. Merging nodes into a new one and splitting one into several are modifications of the **parents** (`ImpactNodeMerge`, `ImpactNodeSplit`, [ADR 0077](adr/0077-impact-node-operations.md)): the parent's draft loses the links to the sources and gains the links to the successors, the sources stay untouched and their impacts carry the parent's impact as `via`; a successor is accepted and committed only once the impacts of its origins are accepted. |
| **Change lifecycle, gate** | A change may have a lifecycle ([ADR 0058](adr/0058-change-lifecycle-and-gates.md)), orthogonal to its technical status: the domain defines it (`domain.Lifecycle`), the methodology says which one its changes follow, the change holds its state. A transition is guarded by a CEL guard in the condition environment; a **gate** is a guard requiring a decided decision point and the expected world state (the `done` of the steps of the state being left, no checklist of its own). A step names the state it is active in through `pre: {"state:<name>": true}`. Leaving a state freezes its impacts, going back needs a new decision and marks later impacts `stale`; commit stays at the end of the change. Which states and gates exist is a use case, not the platform's. |
| **Model exchange** | The request and the answer of one LLM call of an action, an entry `model.call` of the change log ([ADR 0059](adr/0059-prompts-in-the-change-log.md)): capped, read with `prompt:inspect`, shown by the Tokens console (the call itself is a row of the gateway's ledger, ADR 0089, which stores the exchange of every call this log does not keep); the journal record keeps the token counts only (`contracts/journal`, ADR 0066). |
| **Decision** | A choice about something that is not a node (human or agent); the acceptance of a node is the review of its change impact. |
| **Artifact** | Free-form data produced by an action (summary, report, tool response). |
| **Review** (object) | A review the reviewer builds up and submits ([ADR 0080](adr/0080-review-object.md)): a change item of the built-in kind `review` (`domain.TypeReview`; `contracts/review` the use case), each version the whole record of its `key` (`REV-…`): a global comment, a status `open` → `submitted` or `discarded` (both final) and one entry per change impact (own comment, outcome `accept` / `reject`). Submitting applies every entry as an ordinary review in one transaction (`ImpactNodeReviewBatch`: all or none), each `reviewed` event carrying the review id. Every verdict is made by a submitted review ([ADR 0105](adr/0105-every-verdict-is-a-submitted-review.md)): a single verdict, a producer's commit, an action's review and a merge each write the record of a submitted review first, and each decided `impact.reviewed` log entry names it as its `causation`. |
| **Risk**, **Action** | Records of the change ([ADR 0036](adr/0036-risks-actions-brief-transverse-methodologies.md)): each item is a version of the risk or action of its `key` (`RSK-1`, `ACT-1`); the register is the last version of each. A risk has a probability, an impact, a status and an owner role; an action answers a risk or a decision. The register is a use case outside the core: `contracts/risk` registers the item types (`risk.Register()`, [ADR 0065](adr/0065-item-kinds-required-links-change-data.md)) and folds them (`risk.Risks`, `risk.Actions`, `risk.Derogations`). `goap-change` serves `risks`, `risk`, `action`, and `brief` (the change in one line per fact, also given to every LLM action on a change in its system prompt) and `trace` (where an item, a risk or a node comes from and what it led to). |

Applying a Change is three acts (ADR 0102, 0166): *Commit* (`CommitChange`) writes its accepted modifications on its branch, *Merge* (`MergeChange`) takes them into what it was forked from, creating a new baseline and leaving the change open, *Close* (`CloseChange`) merges what is left once everything is settled and applies the change; there is no single Apply call, and both answer the header of the baseline, never its state (a caller reading nodes asks for them). The Change remains the explainable history of *why* the graph changed.

#### Operating the graph by hand

The graph is fully operable from the IDE with no methodology, agent or action running: a change needs none
(`CreateChange` names a methodology only when a process drives it), and every step below is a graph call the IDE
makes for the person.

| Step | Where in the IDE | Graph calls |
|---|---|---|
| Start an empty namespace | nothing to do: the first change of a namespace starts from the empty state, and leaves its first baseline ([ADR 0056](adr/0056-baselines-are-states-of-changes-names-are-tags.md)) | `CreateChange` |
| Name a version | a tag on one node version (not unique) | `TagVersion`, `ListTags`, `DeleteTag` |
| Select versions | a smart tag: a named set of versions, owned and shared to read or edit (`graph/smarttagsvc`, ADR 0143 §7) | `smarttag.v1.SmartTagService`: `List`, `Get`, `Create`, `Update`, `Share`, `Delete` |
| Create a change | Changes explorer **+**: title, intent, namespace, the branch it commits on, the baseline it starts from, own branch, or a parent change (a sub-change) | `CreateChange` |
| Edit or abandon it | Change → Overview: *Edit* (title, intent), *Abandon* (its sub-changes too, its branch closed) | `UpdateChange` |
| Move it to another project | Change → Overview: *Move to project…* (a root change, draft or active; the projects applying its methodology; its sub-changes follow, [ADR 0091](adr/0091-a-change-names-its-project.md)) | `MoveChange` |
| Declare impacts | Change → Impacts: pick an existing node with the reason it is impacted (a new node is created, see below) | `ProposeImpact` |
| Create or edit a node | Node editor bound to the change and its scope: properties (a draft: the version is written when the change commits; the review stays a separate, explicit action), lifecycle moves, *Remove from change* | `ImpactNodeCreate`, `ImpactNodeCheckout`, `ImpactNodeUpdate`, `ImpactNodeTransition`, `WithdrawImpact` |
| Edit its links | Node editor → Relations → *Links*: the outgoing links of the draft (add: a link type the node type allows and a target; remove) | `ImpactLinkCreate`, `ImpactLinkDelete` |
| Retire an entry | An MCP, adapter, policy, assignment or model entry is never deleted: *Retire* moves it to `retired` (its readers leave it out), saving it again restores it | `CommitEdits` (`state`) |
| Review | Change → Impacts: *Review…* (comment mandatory), a shortcut for one impact | `ImpactNodeReview` |
| Review several impacts | Change → Reviews: *New review* (prefilled with every proposed impact), a global comment, one table of the proposed impacts (include / remove box, inline comment, accept / reject per row, Accept all / Reject all), *Submit review* (all or none) or *Discard* | `ReviewOpen`, `ReviewUpdate`, `ReviewSubmit`, `ReviewDiscard` (over `ImpactNodeReviewBatch`) |
| Options, decisions | Scope bar in the header of every tab (labelled "Flow: Main flow" or "Option: name", its colour, a status line; the Impacts, Reviews and Artifacts tabs follow it, marked by a dot of its colour), Compare & decide dialog (opened from the scope bar), Decisions pane (a ruling made there is a person's) | `OpenOption` … `SelectOption`, `OpenDecision` … `RuleDecision` |
| Adopt or discard a flow | Change → Overview (a flow no run works on is adopted straight on the graph) | `AdoptFlow`, `DiscardFlow` |
| Commit | Change → Overview: *Commit* (enabled from `GetCommitState`): the accepted modifications are written as versions on the change's own branch and the nodes withdrawn since their commit reverted; the change stays open, edited and committed again; the methodology is not asked ([ADR 0102](adr/0102-iterate-on-the-branch-of-a-change.md)). The button opens the choice of the nodes to commit (`CommitDialog.svelte`, the `candidates` of `GetCommitState`), all ticked: a partial commit (`CommitChangeRequest.nodes`, `Graph.CommitChangeNodes`) writes the ones named, an impact merged or split via one named following it, and refuses a node with nothing to commit and a version linking to the draft of a node left out; the others stay drafts for a later commit. Only a modification is accepted and committed: a candidate or a draft left as it was checked out is not; a rejected impact is reworked (an edit proposes it again) or withdrawn, and an impact is withdrawn at any time, several at once by checking them in the impacts tab | `CommitChange`, `GetCommitState`, `WithdrawImpact`, `WithdrawImpacts` |
| Merge a change | Change → Overview / toolbar: *Merge* (a sub-change: *Merge into parent*, into its parent's branch, ADR 0103), once it committed nodes it has not merged yet, whatever it still works on (ADR 0166): rebased onto the head of its target when it moved (a clean rebase commits its merges, mechanically: the links of a node merged only when both sides changed its link set), its dependencies closed (no guardian: it judged each commit, `ChangeLifecycles.Commit`, ADR 0146), then a fast-forward; the change stays open, its branch forked again from what it merged | `MergeChange` (`IntegrateChange`) |
| Close a change | Change → Overview / toolbar: *Close*, once everything in the change is settled (every impact committed, none rejected or a candidate, no open flow, decision point or sub-change) and its lifecycle in a final state: what is left merged as above, then the change applied (`applied`) and its branch merged | `CloseChange` |
| Rebase a change | Change → Overview: when a change is behind the head of the branch it was forked from (a sub-change: its parent's), *Rebase* (ADR 0101, 0103); *Keep mine* settles an impact's conflicts | `RebaseChange`, `GetRebaseState`, `ImpactNodeResolve` |
| Merge a committed change | A `committed` change (its merge into the branch it was forked from, a release branch for instance, waits until someone merges it) shows each node changed on both sides: keep the target, or merge taking a side per conflicting property | `PlanMerge`, `MergeChange` (`IntegrateChange`) |
| Merge in a run | The `graph.apply` action commits and merges; when the three-way merge cannot settle a property (both branches changed it apart) the run waits on a `merge` human task listing the value of each side and proposing the change's; *Merge and go on* completes the merge and resumes the run. Before that, when the rebase of the change onto its target (or parent) left conflicts, the apply stops and the run waits on a `rebase` task listing them per impact; once settled and reviewed again in the change tab, *Settled, continue* applies again (ADR 0101) | `ResolveMerge`, `ResolveRebase` (engine), `Engine.ResolveMerge`, `Engine.ResolveRebase` |
| Branches | Baseline explorer → Branches: open one from a baseline, merge it into another (same resolver), abandon or reopen it | `ListBranches`, `CreateBranch`, `MergeBranch`, `SetBranchStatus` |
| Graph statistics | *Graph statistics* tool (left bar): the namespaces with their totals; a namespace opens its tab (branch selector, key figures, links per node, node and link types, most connected nodes, connected parts, the store) | `GetGraphOverview`, `GetGraphStats` |
| Compare baselines | Baseline tab → *Compare*: the nodes added, removed or changed from another baseline (its parent by default), with the properties that differ | `DiffBaselines` |
| Browse the changes | Changes explorer, grouped by status: the open changes listed whole; the applied and abandoned ones by pages of 50, newest first, of the active project (every project with the toggle or a search, the search matched by the server), *Load more* under each group | `ListChanges` (`limit`, `cursor` → `next_cursor`, `total`; `project`, `query`) |
| A node's history | Node editor → History: the changes that acted on it, its versions and the branches each joined | `ListNodeChanges`, `ListNodeVersions` |

The lifecycle transitions a change makes are authorized when it is applied (ADR 0014) before its transaction:
the authorizer reads the access graph, which the transaction of the apply would otherwise block.

### 2.2 Correspondence with Embabel

| Embabel | GOAP | Comment |
|---|---|---|
| Blackboard | **Change** (change axis) | Persisted, shared, auditable; references domain elements. |
| Blackboard object | **ChangeItem** | Typed by its item type (`type`, declared by a domain) + what it is about (`target`). |
| Condition | **Condition** = [CEL](https://cel.dev) expression evaluated on the blackboard *hydrated* with the referenced domain nodes | See §2.3. |
| Agent (`@Agent`) | Methodology **Agent**: a planner (`goap`, `utility`, `hybrid`) + admissible actions + goals | See §2.9. An agent can call other agents. |
| Action (`@Action`) | Methodology **Action** (`script` JS/Go, `llm`, `tool`, `human`, `builtin`) | Preconditions/effects = named conditions. An action's **expectation** translates into a link on the domain axis (§2.4). `script` action code uses the DSL (§2.10). |
| Goal (`@AchievesGoal`) | **Goal** = conjunction of conditions + value | |
| GOAP planner (A\*) | `engine/goap` 🟢 | A\* over the space of boolean states. |
| Autonomy / goal selection | **Intent loop** `engine/intent` 🟢 | Identification of the agent and the goal (all published methodologies) + clarification as long as confidence is insufficient. |
| AgentProcess | **Process** `engine/engine` 🟢 | Observe → plan → act → replan loop. |

### 2.3 Conditions: expressions on the state of the change

A condition is a **named** predicate evaluated on the blackboard. We chose **CEL** (Common Expression
Language, `cel-go`): not Turing-complete, typed, fast, sandboxed, serializable in a YAML definition.

Variables exposed to the expression:

| Variable | Content |
|---|---|
| `change` | `{id, title, intent, status, goal, baseline, branch, data}` |
| `items` | the active ChangeItems (facts) |
| `decisions`, `artifacts` | items filtered by `kind` |
| `changeImpacts` | the change impacts ([ADR 0024](adr/0024-change-impacts.md)), `{id, key, type, types, intent, rationale, review, reviews, comment, pre, post, committed, planned, hasPost, recheck, props, via}`; `pre`, `post`, `committed` are hydrated node views or `null` |
| `vars` | free process variables (clarification answers, parameters) |
| `options`, `activeOption` | the options of the change ([ADR 0032](adr/0032-branches-as-pointers-baselines-as-deltas.md) §6), `{id, name, hypothesis, status (exploring / evaluated / selected / rejected), active, evaluation}`, and the id of the one it works on (`""`: the main flow) |
| `decisionPoints`, `questions` | the decision points ([ADR 0009](adr/0009-branches-options-decisions.md) §4), `{id, question, status (open / blocked / ratifying / escalated / decided), options, criteria, decider, threshold, rounds, maxRounds, escalation, openQuestions, questions, ruling, option, decidedBy}`, and all their questions `{id, point, text, status (open / answered), answer, answeredBy}` |

| `risks`, `actions` | the risk register and the actions of the change ([ADR 0036](adr/0036-risks-actions-brief-transverse-methodologies.md)): `{key, title, description, probability, impact, score, status, live, owner, actions, step, versions}` and `{key, title, status, owner, due, for, result, versions}`, the last version of each key |
| `verifications`, `derogations` | the verification state of the effects of the action runs and the derogations of the change ([ADR 0075](adr/0075-verification-derogation-criticality.md)): `{execution, action, impact, oracle, independent, state, producer, by, open}` and `{key, rule, target, reason, signatory, expires, status, open, expired, versions}` |
| `change.criticality`, `criticalityPolicy` | the criticality level of the change and what the organisation requires of it: `{oracles, sampling, signatoryRole, maxDerogationHours}` (not `policy`, taken by decision points) |
| `dependencies`, `change.dependsOn` | the changes the change depends on ([ADR 0108](adr/0108-dependencies-between-changes.md)): `{id, title, methodology, status, state, parent, satisfied, reason}`, `satisfied` when its work reached the branch the change merges into; and their ids |
| `assessments` | the latest rating of each subject of the change ([ADR 0110](adr/0110-reactive-activities-review-of-the-description.md), `contracts/assess`): `{key, score (0-5), verdict, findings, suggestion, current, answered, item, versions}`; `current` the subject (`description`: the title and the intent) still reads as rated, `answered` no `comment` about it came after the rating |
| `history` | what the runs on the change did ([ADR 0109](adr/0109-conditions-derived-scoped-history.md) §4), folded from the log of the change (`journal.Fold`, facet `domain.FacetHistory`: by the graph with every blackboard, `Graph.LogFacets`, so lifecycle guards, gates and guardians read it too, else by the engine): `runs` `{process, parent, methodology, agent, goal, status, seq}`, `steps` `{path, methodology, process, action, execution, outcome (succeeded / failed / running), succeeded, error, seq, runs, current, staleBy}` and `last` (the latest run of each step path). `current`: the success still stands, nothing it started from moved since (an edit, a rebase, a cancel, a new version of a keyed item) but by a step relying on it, and the steps it relies on stand and are older, so a stale step re-opens the steps after it (§5) |

**Condition libraries** ([ADR 0064](adr/0064-condition-libraries.md)): a methodology gets no condition it did not
declare, except those of the libraries it imports (`imports: [decisions, risks, verification, derogations, dependencies]`, `condition.Library`). `decisions`:
`open_questions`, `no_open_questions`, `decision_ready` (a point can be ruled now), `decision_pending`,
`no_decision_pending`, `ratification_pending`, `decision_escalated`, `options_open`, `options_evaluated`,
`option_selected`. `risks`: `open_risks`, `unmitigated_risks` (a live risk of score ≥ `condition.HighRisk`, 9, without
an action), `risks_under_control`, `open_actions`, `no_open_actions`. `verification`: `unverified_effects`, `all_verified`, `reserves_open`. `derogations`: `no_expired_derogation`, `derogation_debt` (`condition.DerogationDebtLimit`, 3 open). `dependencies`: `dependencies_met`, `dependencies_pending` (ADR 0108). `assessments`: `description_rated` (a current rating answering the last remark, the effect of a rating), `description_accepted` (rated, score ≥ `condition.AcceptableScore`, 3) (ADR 0110). A condition the methodology declares under the
same name replaces the library one. The variables of the decision and risk concepts (`options`, `decisionPoints`,
`questions`, `risks`, `actions`) are always in the environment, and computed only when an expression reads them.

**Scoped conditions and aggregates** ([ADR 0109](adr/0109-conditions-derived-scoped-history.md) §3): a condition may
have a `scope` (`impact`, `item`, `risk`, `action`, `step`; default `change`) and a `where`; it is evaluated per subject
of its scope (`self`), its values carried by the entries of the scope (`changeImpacts[i].conditions.x`, `self.conditions.x`
for another condition of the same subject). An aggregate (`all:` / `any:` / `none:` of a scoped condition, `where`,
`empty`: the value of `all` on no subject, default false) brings them to the level of the change, the only level the
planner and the goals see. A **scoped action** (`scope`, `where`) runs once per pending subject (its scoped pre holds,
its scoped effects do not), one step per subject with `vars.item` / `vars.subject`, several subjects at once for an llm or tool action
(`Engine.MaxParallel`, default 4, fewer with the action's `parallel`); the planner sees it lifted (pre
`pending:<action>`, effects the `all:` aggregates of its scoped effects over its subjects). A step running one may
narrow its subjects (`where` on the step, with its `done`): one action, one pass per part (the layers of the `sdlc`
impact analysis).

Each node reference of a change impact (`pre`, `post`, `committed`, link endpoints) is **hydrated**:
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
`identify_impacts` (LLM) → `select_impacts` (human)). If no plan exists, the process **waits** (task `condition`)
when what is missing is established outside it (no action of its agent produces it, and assuming it the goal is
reachable: another process, a person, the state of the change), else it goes to `stuck`, waiting for a person to unblock it (ADR 0036 §3).

Executor types:

| Kind | Execution | Output |
|---|---|---|
| `llm` | Prompt (Go template) + blackboard context → Model Gateway, structured JSON output | ChangeItems |
| `tool` | Call to a tool (`<mcp>/<tool>`) of the MCP hub, through the organization binding (§3.9) | Artifact `{tool, result}` |
| `human` | Creates a task; the process moves to `waiting` until `SubmitHumanInput`. An action declaring a `form` (ADR 0115, `contracts/form`: fields, widgets of an extensible registry, constraints) shows it and takes its values (`SubmitHumanInput.form`, `Engine.SubmitForm`), checked by the engine, then turned into items by the action's `code` (`ctx.var("form")`) or recorded as an artifact with the links of its link fields | Submitted items, or the items of the form |
| `builtin` | Registered Go function, checked by name when the methodology is compiled ([ADR 0062](adr/0062-builtins-registry.md)): `graph.propagate` (impact propagation), `graph.apply` (change application), `decision.investigate`; the self-observation ones (`observe.analyze`, `observe.propose`, `methodology.draft`) are registered by the composition roots | ChangeItems / new baseline |
| `call` | Generated by the compiler for a step done by a nested process or a method, never declared ([ADR 0126](adr/0126-formal-execution-elements.md)): the action's `Call` (`methodology.Call`: agent and goal, or the capability whose applicable method is chosen when it runs, `foreach`, `groupBy`) names the sub-agent the engine starts on the same change and waits for; a primitive of the engine (`RunCall`), not an entry of `Engine.Executors` | What the sub-agent did |

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
3. if `confidence(top) ≥ threshold` and sufficient margin over the second → goal selected for the process (the goal of the change is its methodology's main goal, [ADR 0096](adr/0096-goal-of-a-methodology-default-goal-of-a-change.md), and does not follow the runs);
4. otherwise → **clarification question** (generated from the candidate goals), process moves to `clarifying`;
   the answer is added to the history and the loop repeats (max N rounds);
5. the selected goal may require **parameters** (e.g. the starting node): these are extracted into `vars`.

**Goal of a change** ([ADR 0096](adr/0096-goal-of-a-methodology-default-goal-of-a-change.md)): a methodology names its main goal (`goal:`, a goal or a process); `CreateChange` gives it to the change through the registry seam `ChangeLifecycles.DefaultGoal` (an explicit `NewChange.Goal` wins, a sub-change keeps its parent's), and it changes afterwards only by an explicit edit, never by a run.

**Starting points** ([ADR 0097](adr/0097-starting-points-possible-steps-towards-the-end-goal.md)): the goal being the end point of the process, `Engine.StartingPoints` lists the steps (and steps of a method) of the change's methodology that are possible now towards it: every entry condition holds, the exit does not, nothing carries them out. A step that waits is only counted, an action is never listed; starting a point hands the selection to the scheduler (`StartProcess` with the agent of the process or method and the step path as goal, which resolves to the exit criteria of the step). RPC `ListStartingPoints`, `goap-scheduler/starting_points`, the "Possible next steps" panel of the change tab, `list_agents` / `start_agent` of the assistant (propose only possible steps), and `Compiled.Hints()` (a methodology that cannot start on an empty change).

### 2.6 Execution loop (Process)

```
        ┌─────────────┐
        │   intent    │── clarification ──► (waiting for user)
        └──────┬──────┘
               ▼
   ┌──► observe: hydrate blackboard, evaluate conditions ──► goal reached? ── yes ──► completed
   │           │ no
   │           ▼
   │    plan: A*(WorldState, actions, goal) ── no plan ──► waiting (conditions from outside) | stuck (a person unblocks)
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


**Personal changes** ([ADR 0037](adr/0037-personal-changes-and-preferences.md)). A change held by the personal unit of a
person (`USR:<subject>`) is visible to and changeable by that person only, and is never split into sub-changes. A change
that committed nothing can be purged with its log (`DeleteChange`).
The settings of the platform edited in the web dialog are staged this way: each edit is an impact of the personal
change, **Save** applies it, **Discard** (or leaving the dialog and accepting to lose the edits) purges it. The graph
service does the staging, one call per edit or batch of edits (`GetPendingSettings`, `StageSettings`, `SaveSettings`,
`DiscardSettings`, [ADR 0122](adr/0122-settings-staged-by-the-server.md)). Personal
preferences are not graph data ([ADR 0038](adr/0038-user-preferences-service.md)).

### 2.7b Verification, derogations and criticality ([ADR 0075](adr/0075-verification-derogation-criticality.md))

A result produced is not a result accepted. Three small mechanisms, each placed in the concept that answers its question:

| Question | Concept | What |
|---|---|---|
| WHO | Organisation | `organisation@CriticalityPolicy` (one per level, `adminOnly`, nearest unit wins, whole policy): accepted oracle kinds, sampling, derogation signatory role and maximum lifetime; the permissions `derogation:sign`, `derogation:sign-role`, `change:lower-criticality` |
| WHY | Change | `criticality` (`C1` / `C2` / `C3`) in `Change.Data` (`domain.DataCriticality`; raising is free, lowering needs `change:lower-criticality`); `verification` and `derogation` items |
| HOW | Methodology | `verify: {oracle: tool\|human\|model, independent}` on an action; `Methodology.Criticality` (default level); `vetos` / `objectives` on a lifecycle transition; the libraries `verification` and `derogations` |
| WHAT | Domain | nothing: the domains stay unaware |
| WITH WHAT | Adapter / model alias | the alias of a `model` oracle must differ from the producer's |

- **Verification** (`contracts/verify`). When an action with `verify` ends, the engine writes a `produced` item of type
  `verification` (oracle, `independent`, producer alias, impact ids); the states `produced`, `verified`, `accepted`,
  `accepted_with_reserve`, `rejected` are items of the change log (`fact.verification`), folded by `verify.Subjects`.
  `Graph.ReviewPolicy` (a `domain.ReviewPolicy`) is asked by every review of an impact and `verify.Policy` refuses a
  verifier equal to the producer. The graph keeps the mechanism, `contracts/verify` the rule, as for decisions.
- **Derogations** (`contracts/risk`). An item of type `derogation` (key, rule, target, reason, signatory, `expires`; no
  expiry, no derogation), versioned like a risk; `derogation:sign` (and `derogation:sign-role` for the role the policy
  of the level asks) is checked through `Graph.ItemAuthorizer`; `accepted_with_reserve` needs one in force
  (`ImpactNodeReviewWithReserve`). The builtin `derogation.expire` (`methodologies/examples/derogation-expiry.yaml`) closes
  the expired ones and sends what they covered back to `proposed` (`Graph.ReopenImpacts`).
- **Criticality** (`contracts/criticality`). The policy of a level is resolved along the unit chain (`lib/access`), else the
  compiled-in `Defaults()`; `Graph.ItemPolicy` applies it to the items written (oracle kinds). CEL reads
  `change.criticality` and `criticalityPolicy`.
- **Gates**. A transition may list `vetos` (one unmet blocks) and `objectives` (an unmet one needs a derogation in force
  naming it); `condition.CheckGate` evaluates them, the reserve is recorded on the transition item as `unmet` / `reserve`.

`graph/graph` and `contracts/domain` import none of these packages (`platform/layering`); `platform/cmd/graph` plugs the
seams explicitly. Open points (web editors of the policies, sampling enforcement...) are in the ADR's "Not done".

### 2.7c The EU AI Act floor ([ADR 0129](adr/0129-eu-ai-act-floor.md))

What goap is for, its excluded uses and how to operate it under the AI Act are in [docs/ai-act.md](ai-act.md). Four
defaults hold whatever a methodology says: **a person reviews what a model produced** (`Engine.PersonReviewsModelOutput`,
`GOAP_PERSON_REVIEWS_MODEL_OUTPUT`, on in the compositions: an llm action accepts no change impact, no automated action
accepts one whose producing run called a model, `engine/engine/oversight.go`); **the record of an execution is never
purged** (`PurgeChange` refuses a change whose log holds a stream the graph does not write: journal, model exchanges);
**the ledger of model calls is kept six months** (`modelgw.MinAuditRetentionDays`, a shorter retention or the prompts
off are warned about at start); **provenance leaves with the content** (`provenance` of a snapshot node and of the OKF
front matter: version, change, producer, `model: true`). The assistant and the helper say they are AI.

### 2.8 ABAC access control (Casbin)

All access decisions go through a [Casbin](https://casbin.org) enforcer with an
**ABAC** model ([ADR 0020](adr/0020-access-control.md)). A policy rule is:

```
p, <rule on attributes>, <resource type | *>, <action | *>, <allow | deny>
```

| Attribute | Content |
|---|---|
| `r.sub` | caller: `Subject`, `Org`, `Roles` (the roles it holds on the resource's project, merged by the authorizer, ADR 0043; a token's only from an external identity provider, ADR 0138), `Global` (the platform roles, `admin` for an administrator, a platform Assignment, ADR 0047), `ProjectAdmin` / `ProjectOwner` (the flags of its Assignments on the resource's project chain, ADR 0138) |
| `r.obj` | resource: `Type`, `ID`, `Org`, `Owner`, `Name`, `ProjectID`, `Roles` (roles allowed to run an agent / action) |
| `r.act` | action: `read`, `start`, `submit`, `write`, `publish`, `delete`, `apply`, `run`… |

Functions available in rules: `hasRole(r.sub, "x")` (on the project or platform-wide), `hasAnyRole(r.sub, "a", "b")`,
`hasRoleIn(r.sub, role, r.obj)`, `onProject(r.sub)` (holds a role on the resource's project, or a flag),
`mayRun(r.sub, r.obj)` (one of `r.obj.Roles`, any role on the project when it lists `"*"`, nobody when it lists
none, a project administrator always), `isProjectAdmin(r.sub)`, `isProjectOwner(r.sub)`, `isAnonymous(r.sub)`. A
matching `deny` overrides any `allow`. **Without an explicit grant everything is refused** ([ADR 0138](adr/0138-deny-by-default-owner-and-admin-assignments.md)):
a nil authorizer refuses (`authz.Check`; tests pass `authz.AllowAll`), an empty list of roles grants nothing.

Default policies (compiled in `contracts/authz`; seeded as `Policy` nodes at the first start, and applied as they are while
the graph holds none; a graph still holding the defaults of before ADR 0043 unchanged gets these instead,
`graphsvc.SeedAccess`):

| Rule | Resource | Action |
|---|---|---|
| `hasRole(r.sub, "admin")` | `*` | `*` |
| `hasRole(r.sub, "reader")` | `*` | `read` |
| `!isAnonymous(r.sub) && ((r.obj.Org != "" && r.obj.Org == r.sub.Org) \|\| onProject(r.sub))` | `*` | `read` |
| `!isAnonymous(r.sub) && r.obj.Namespace != ""`; `!isAnonymous(r.sub)` | `*`; `trigger` | `read` |
| `isProjectAdmin(r.sub)` | `*` | `*` |
| deny unless `isProjectOwner(r.sub)` or `admin` | `assignment`, `project` | `flag`, `stop`, `move` |
| `onProject(r.sub)` | `process`, `object`, `node`, `tool` | `*`, `create`, `transition`, `call` |
| `onProject(r.sub)`, deny `r.sub.Subject == r.obj.Owner` but `admin` | `change` | `apply` |
| `hasRole(r.sub, "release_manager") && r.sub.Subject != r.obj.Owner` | `release` | `deploy` |
| `hasRoleIn(r.sub, r.obj.Role, r.obj)` | `step` | `perform` |
| `hasRoleIn(r.sub, r.obj.Accountable, r.obj)`, deny `r.sub.Subject == r.obj.Owner` but `admin` | `step` | `approve` |
| `mayRun(r.sub, r.obj)` | `action`, `agent`, `model` | `run`, `run`, `use` |

Methodologies, domains, triggers, the organisation, policies and adapters have no rule of their own: administrators
administer them. **Project owners and administrators** (ADR 0138): a project Assignment carries the flags `owner`
and `admin` (never a platform one); they hold on the project and its sub-projects, inherited, never taken. An
administrator does everything on the resources of the project, manages the roles of its Assignments and creates its
sub-projects; an owner also sets the flags, stops and restarts the project (`stopped` attribute of `ProjectUnit`: a
stopped project, or one below it, takes no new change) and moves it. `access.ProjectScopeValidator` judges every
write of an Assignment or a project at commit against the state it started from, `ProjectOwnerValidator` keeps an
owner on a project that had one; the graph handler reads a node or a change only for who may access its project.

**Reach and owned writes** ([ADR 0161](adr/0161-access-rights-and-data-segregation.md), phase 1): what a caller
reads of the current graph is decided once, in the view reader (`graph/graph/view.go`, `reach.go`), never per
handler: `Graph.ReachOf` (`graphsvc.ReachOf(directory.ReachableProjects)`, plugged by `platform/cmd/graph`)
gives the projects whose nodes the caller reads (`access.Snapshot.ReachableProjects`: the projects
`MayAccessProject` allows) and the namespaces read whole (organisation, platform, methodology). `ViewNode`,
`ViewNeighbourhood`, `ViewNodes`, `ViewNodesOfType`, `QueryView`, `BaselineNodes` / `BaselineLinks` and
`ExportSnapshot` leave out the nodes out of reach and every link touching one (no stub), the pages and counts filtered
in the store (`NodeQuery.Projects` / `LinkQuery.Projects`, `n.project IN …`); `GetBaselineGraph`, `GetBaselineDelta`
and `DiffBaselines` keep what `Reach.Keep` / `Sees` keep. A platform service acting for a person (`OnBehalfOf`) reads
as that person (`graphsvc.Reader`); a service acting by itself, or an in-process call naming no principal, reads
everything. A change checks out a stored node only when its project, or one above the node's, owns it
(`Graph.OwnedWrites` = `graphsvc.OwnedWrites`: every namespace but the open ones; `checkOwned`, `authz.ErrForbidden`).
The creator, the editor, the visibility scopes of the lifecycle states and the roles as action lists are the next
phases of the ADR.

**Batched reads**: a read of the current graph reads what it meets in batches, never one row per element, so its
round trips do not grow with the links of a hub or the length of a history (`graph/graph/batchread.go`): the
neighbours of a node (`ViewNeighbourhood`, and so each hop of `QueryView`) and of the blackboard's nodes through
`draftReader.nodes` (the drafts from the fold, the stored versions in one `Tx.NodesAt`), the posts of a change's
impacts (`storedPosts`: `openWorld`, `changeViewTx`), the headers of the changes of a history (`changeHeads`:
`ChangesFilter.IDs` in one `ChangeHeads`; `NodeChanges`, `stackChanges`, `diffBaselines`), the headers of a page of
`ChangesPage` given to the transaction (`guardTx.knowHeads`). `TestReadsAreBatched` counts the single-row reads of
these paths over a hub; a new read path meeting several nodes or changes goes through these helpers.

**Roles** ([ADR 0035](adr/0035-methods-roles-operations-documents.md) §2, [ADR 0043](adr/0043-project-scoped-roles-admin-flag.md)):
a methodology declares its roles (`methodology@Role` nodes, ADR 0039); its processes and methods assign them RACI
style (`roles: {responsible, accountable, consulted, informed}` on a process, a method or a step; a process and a method
must name their responsible role, [ADR 0139](adr/0139-roles-of-processes-methods-agents-actions.md): an error once
stored, a warning in a draft; a step inherits what it leaves unsaid from its parent step, then its process or method,
field by field), the agent running a process or a method acts as its responsible role (`Agent.role`, unless declared),
an action acts as the role of what refines it (`Action.role`: its step, the methods listing it, else the action it
specializes), and its agents and actions name the roles allowed to run them (`roles: [...]`; an action without roles takes its agent's,
none at all: no role check beyond the run's own permission, held by the members of the project). It names roles, never people. A user holds no role of their own (administration is a
platform Assignment, ADR 0047): a project names the methodologies that apply to it (inherited by its sub-projects), which identifies the
roles it needs, and an `Assignment` grants some of them to a unit or a user on the project. The authorizer resolves
the roles of the subject — its own `User` node, then its unit and that unit's ancestors (`Snapshot.SubjectChain`) — on
the resource's project and its ancestors (`ProjectChain`), and merges them into `r.sub.Roles`: from one project to
another the same person holds different roles, and so may do different things. The responsible role performs a step's
human tasks (`step:perform`, checked on submit), the accountable role may approve its gates (`step:approve`), never on
its own change; an action its initiator holds none of the roles of waits for someone who does (`action:run` approval;
a human action is submitted by one of them), and an agent is started only by someone holding one of its roles. The
engine acts as its initiator on the process's project (`ProcessRef.Actor`), so tool calls and model use resolve the same
roles. The engine reaches who and where through one port, `engine.Scope` ([ADR 0063](adr/0063-engine-scope-port.md)):
`ProcessRef` (process id, initiator, organisation, project) in, plain answers out (`As`, `Tools`, `CallTool`,
`Allowed`, `MayRun`, `MayStep`); `AuthzScope` implements it with `authz.Authorizer` and the MCP hub. Nodes carry no project (a node is shared by the changes of several projects): object access is the work of
processes, changes and tools of a project, not of a node's own project.

- There is no IAM service: who may do what is **graph data** ([ADR 0020](adr/0020-access-control.md)). A rule is a
  `Policy` node and a caller a `User` node (profile, `member_of` a unit) of the `organisation` domain, changed
  through changes like any node and edited in the frontend's "Access" screen. A rule is validated (compilation + trial
  evaluation) when read.
- Every service builds its `authz.Authorizer` in process (`lib/access`) from a snapshot of the head of the
  `organisation` namespace's `main` (the graph service over its own graph, the others through a graph client),
  rebuilt when that head moves (looked at once per second).
  The roles of the `User` node of the subject are added to those of its token, and its unit is its organisation when the
  token names none (the gateway does the same before propagating `X-Goap-*`; `GET /api/whoami` returns the result, with the structures, names, platform roles and capability hints the web builds on: ADR 0070).
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
startup and kept in sync with its events, the built-in domains `methodology`, `organisation` and `platform` among them (stored
and protected by the registry, ADR 0167). The graph judges
a change by the catalogue in force and refuses a node whose type it does not resolve. Nothing is projected onto the
graph.

A node type may name the **editor** of its nodes in the IDE (`editor: agent`, inherited through `extends`,
[ADR 0027](adr/0027-node-type-editors.md)): the IDE opens every node through `openNode` (`web/src/lib/nodeEditors.ts`),
which reads the editor from the type catalogue the IDE loads from the registry (`ListTypes`) and falls back to the default node editor; the editors a type can name
are registered in `web/src/lib/views/nodeEditors.ts` (`agent`, `action`, `methodology`, `domain`, `unit`, `mcp`, ...).
The default node editor (`NodeTab.svelte`) follows the change selected in the context (the working change of the
header): it sees the graph through it, the nodes the change created, the versions it committed and the drafts it holds,
the other nodes as its fork has them, and it reads only what it shows: the node and its neighbours, more as the reader
unfolds the tree (`Graph.ChangeNodeNeighbourhood`, `GetNodeNeighbourhood` with `change_id`: the change's impacts laid over
its branch or its fork for those nodes only, a link kept when its target is in that world at the version it names; at the
head `NodeNeighbourhood`; web `hoodIndex` / `baselineHoods` / `changeHoods`), and its link and form pickers search the
baseline (`ListBaselineNodes`), as the *New object* dialog and the declared forms of human tasks do: no whole namespace
is read to edit a node (`GetChangeGraph` with `world` stays for the readers of a whole world); the *Edit* of an impact and opening a node from a change select that change first,
and with no change selected (*Latest version*, or none in the header) it shows the latest released versions.
It may also give the **icon** and **color** its nodes are drawn with (`icon: database`, a name of the IDE's icon set,
`color: "#b45309"`; inherited through `extends`, `def.NodeType.Icon` / `Color`, `TypeInfo.icon` / `color`): the compact graph
of the node editor's *Graph* pane draws each node as a circle of that color holding that icon, its details in a popup on
hover (`NodeGraph.svelte` `compact`, `TypeCatalog.look`, which falls back to the node icon and a color from the type's name).

A link type may be flagged **`compose: true`** (`def.LinkType.Compose`, `typecat.LinkType.Compose`, `LinkTypeInfo.compose`): its target is a part of its source, a piece of display metadata
the editors read from the domain rather than knowing links by name. The methodology editor lists as panes the element types the version composes
(`TypeCatalog.sectionsOf`, through `defines`), and shows under a process or a method the steps composed through `sub_activity` (`TypeCatalog.partsOf`); a new type composed by
`defines` and bound to a draft collection (`TYPE_COLLECTION`, `methodologyTabs.ts`) gets its pane without editor code. The `role` and `step` node editors open the version's Roles pane and the owning process or method.

A link is read from the node looking at it: a link type carries a **`label`**, how it reads from its source ("implements"), and an
**`inverseLabel`**, how it reads from its target ("implemented by") (`def.LinkType.Labels`: the label falls back to the name, the inverse label
to the label; `typecat.LinkType.Label` / `InverseLabel` and `LinkTypeInfo.label` / `inverseLabel` hold them with the fallbacks applied). Pure
display metadata, the graph never reads them: the web shows a link with `TypeCatalog.linkLabel(ref, 'out' | 'in')` (every graph and link
view: the node's links, the parents and children trees, the node graph seen from its center, the graph explorer, the links and
neighbours of the Baseline explorer; the ontology graph of the domain editor reads them from the form) and keeps the qualified link type
in the tooltip.

### 2.11 Agent triggers

Outside the intent loop, an agent can be executed **automatically** by triggers
declared on the agent (`agents[].triggers`):

| Field | Role |
|---|---|
| `type` | `event` or `schedule` |
| `event` + `filter` | `change.created`, `change.edited`, `change.attached`, `change.detached`, `change.item_added`, `change.signal`, `change.committed`, `change.rebased`, `change.merged`, `change.transitioned`, `change.applied`, `process.completed`, `process.failed`, `process.stuck`, `process.attached`, `step.completed`, `methodology.published` (`contracts/events.All`); CEL filter on `event` (`event.change.*`, `event.process.*`, `event.items` for `change.signal`'s payload, `event.branch` and `event.nodes` (`{id, version, namespace, key, type, state, deleted}`) for `change.committed` / `rebased` / `merged`, ADR 0159); an event trigger's new change acts in the project of the event's change |
| `schedule` | cron expression (5 fields, UTC) |
| `goal`, `intent` | targeted goal (otherwise identification is limited to the agent) and intent text |
| `target` | `new_change` (new change on the latest baseline) or `event_change` (the event's change) |
| `roles` | roles of the **service identity** `system:trigger:<methodology>/<agent>/<trigger>` (ABAC) |

Safeguards: a trigger never reacts to its own productions (the change it opens is marked
`data.trigger`, the process carries `trigger`), and it does not run more than once every 2 s.
Events come from NATS (graph, engine, registry services, each through its outbox, ADR 0136) or the relays' in-process
sinks in all-in-one mode; a methodology published reloads the triggers from the event the graph's relay delivers.
`ListTriggers` / `FireTrigger` expose the state and manual firing (permission `trigger:fire`).
With several engine replicas, event triggers are delivered through a durable JetStream consumer
(competing consumers, one replica handles each event) and schedule (cron) firing is gated by a
JetStream-KV lease (`TriggerManager.Leader`) — `Reload`/`States()`/manual `fire` stay available
on every replica regardless of leadership (ADR 0031). The runs themselves are shared by the
replicas through the process store and its locks (§3.2, ADR 0124).

**Transverse methodologies** ([ADR 0036](adr/0036-risks-actions-brief-transverse-methodologies.md) §3): a methodology
that declares `appliesTo: [sdlc]` has its processes run **alongside** the changes of those methodologies, on the same
change, choreographed by the events of the change rather than orchestrated. The engine publishes `step.completed` for
every step of a process; a transverse methodology declares the events it reacts to (`on: [{event, filter}]`); one
companion run per change and process (`Trigger` = `companion:<methodology>/<process>`) runs again in place for each
matching event, with the event in `vars.event` (items carry `at` to compare with `vars.event.at`), the events arriving
while it works waiting in its inbox. Back the other way, through the state: a step may need what the transverse process
establishes (`risks_under_control`, a condition of the `risks` library): the process then **waits** for those conditions (`waiting`, task `condition`),
told from a genuinely **stuck** one (nothing established outside would unblock it, `Engine.awaited`); both are tried
again when the change moves (`Engine.Accompany`, fed by the trigger manager's events). Neither is an end: a person who
answers for the run (its initiator, the accountable role of its step, a member of its organisation) can always unblock
it (`Engine.Unblock`): retry the actions it gave up on, or abandon it. Nobody declares a condition established: it
holds when the change says so, so a person establishes it by working on the change, then retries
([ADR 0109](adr/0109-conditions-derived-scoped-history.md) §6). The same person can **cancel** any run that has not ended
(`Engine.Cancel`, RPC `CancelProcess`, "Cancel run" on the run tab): a run in flight is interrupted through its context
(its model and tool calls end; `Engine.Run` keeps the cancel function of each run in flight and stops without failing
when a cancellation was asked), then the process is `cancelled` (terminal, `Error` "cancelled by <subject>: <reason>",
what it waited for dropped, `process.ended` journaled with its status), and so are its live sub-processes, the parent
first so that it is not woken by them; a parent waiting on a cancelled sub-process resumes as for a failed one, and a
cancelled companion run is not brought back by the events of its change. A transverse methodology has no namespace of its own.

Events cross the integration relation too ([ADR 0106](adr/0106-methodologies-integrating-methodologies.md)): `on:`
takes `from: self | integrated | integrator | dependency`, and the engine relays an event of a change to its parent, to
its open sub-changes and to the open changes depending on it ([ADR 0108](adr/0108-dependencies-between-changes.md))
(`Engine.relay`), waking the subscribed processes of their methodologies on their own change with
`vars.event.relation`; the graph publishes `change.transitioned`, `change.committed`, `change.rebased`,
`change.merged`, `change.attached` and `change.detached` for that coupling. A commit, a rebase and a merge each name the
branch and the node versions they left there ([ADR 0159](adr/0159-requests-three-levels-of-change.md): `branch`,
`nodes` of `domain.ChangeEvent`, `domain.EventNode`): `change.committed` the versions a commit wrote on the change's
branch, `change.rebased` the versions the commit of a rebase's merges wrote (none when only the fork moved),
`change.merged` (every merge: a sub-change into its parent's branch, a root change into a lasting one, before
`change.applied`) the versions it wrote or joined on the target.
**Reactive activities** ([ADR 0110](adr/0110-reactive-activities-review-of-the-description.md)): any activity of a
methodology (a process, a step of a process or of a method, a method, an action) may declare `on:` (the same
subscriptions, `from` included). On each matching event of a change of its methodology the engine runs it on that change
as a companion run kept per change and activity (`companion:<methodology>/<activity path>`, re-run in place, inbox):
a process or a method with its agent towards its goal, a step with the agent of its process (or method) towards the
step path, an action alone with the agent `on.<action>` towards its effects (`methodology.Compiled.Reactions`,
`Engine.react`). The event says when to look, the conditions whether there is work: a run woken while the exit of its
activity holds ends at once. Items produced by a reaction's own actions do not wake it. An event of a subscription
flagged `supersede: true` arriving while the run is at work interrupts it (its model and tool calls, its sub-processes)
and runs it again on the change as it is now, instead of waiting in its inbox (`Engine.restartFor`; `sdlc` rates an
edited description so); an event trigger on the change of the event takes the same flag and cancels its runs still at
work on that change before it starts the new one (`Engine.supersedeRuns`). The graph publishes
`change.edited` (`event.fields`: `{<field>: {from, to}}`, title, intent, goal, `data.<key>`) when the header changes.
A process step may be declared a **gate** of the lifecycle of the change ([ADR 0132](adr/0132-process-gates.md)):
`gate: {transition, checklist, decision, freezes}` (`methodology.Gate`, `engine/methodology/gate.go`). The checklist
is split among roles: each item names a condition of the methodology (or one a person of its role attests, `attest`,
the human action `attest:<path>/<item>` writing the artifact `attestation:<path>/<item>`, CEL `attestations`), is a
veto or an objective (a derogation may cover it, ADR 0075), and may apply only `when` a CEL condition holds. The
compiler generates the conditions `gate:<path>` (the transition stands: `change.passed`), `gate_open:<path>`,
`gate_go:<path>` (a decided go not consumed by a move: `change.consumed`) and `attested:<path>/<item>`, and the
actions `<path>:open` (builtin `gate.open`: opens a decision point with the choices go / no_go / back and the policy
of the gate, once the unconditional vetos hold; a decided back takes the `back` transition), `<path>:rule` (the human
action the people of the board rule it with, seat by seat, form widget `decision` with `roles`; or `<path>:decide`, a
call to the decision method when the step names a capability) and `<path>` (builtin `gate.pass`: `TransitionChange`
with the decided go). The accountable role of the step chairs and sits on the board; `contracts/decision` folds the
rulings of a board (`board`, `rule` unanimity | majority | consent, `chair` breaks a split, votes kept in the policy);
the graph checks the seat (`RuleRequest.Role`, `domain.SeatedPolicy`, `Graph.SeatAuthorizer` = `decision:sit`,
`graphsvc.SeatAuthorizer`). The registry merges the criteria of the gates into the transitions they take
(`registrysvc.WithGates`: the items, then the veto `decision`) and their freezes (`domain.Freezes`: `fields` title /
intent / description, node `types`, `transitions` lets a frozen type still move, `keepPhase` leaves the impacts of
the state left editable), recorded on the transition fact; the graph refuses an edit of a frozen field
(`UpdateChange`) or a node of a frozen type (`work.checkTypeFreeze`; a candidate never written may still be
withdrawn) while the move stands (`Change.InForceFreezes`). An agent with no `actions:` plans the gates too; a gate
follows the gate before it by its `pre`. Choices of a decision point that are no flow: `OpenDecisionRequest.Choices`.
`sdlc` 0.11.0 follows `alm`'s `maturity_of_change` (framing, analysing, designing, building, releasing, delivered,
abandoned): the framing confirmation, the analysis (a board by consent, the architecture attested), the design, the
build and the delivery are gates, and the change commits delivered. The web edits a gate as JSON (*Gate* section of
`StepEditor.svelte`, `parseGate` in `methodologyForm.ts`) and draws it as a chip of `StepNode.svelte`.
A step may be declared a **review** ([ADR 0130](adr/0130-reviewing-steps.md)): `review: {subject, automatic}` names
the subject it rates (`description`, `title`, `intent`, `impact:<key>`, `item:<key>`); an automatic review starts by
itself, without anyone launching it, on the events of its subject (for a header subject: the change created, its fields
edited, superseding the rating in flight, a remark about it; the step's own `on:` otherwise, `Step.ReactsTo`), and the
other runs of its process leave its actions alone and wait for its exit (`Compiled.AutomaticReview`, `carried` in
`engine/engine/cycle.go`); a run whose goal is the step carries it. Its verdict is an assessment, advice: it accepts no
impact (the floor of ADR 0129 holds) and a person decides after it. `sdlc` (0.9.0) reviews the description in its
framing: the step `software_delivery/framing/review` names the goap method `description_rating` (an llm action first,
a human one as the fallback the planner takes when the model cannot rate) (items `assessment`, `contracts/assess`) and
answers the requester's `comment` items, then the product owner rules the go of the framing (`framing/confirmation`,
after the rating, a gate since 0.10.0 vetoed by `description_accepted`); the analysis waits for that gate; the web shows every assessment
by its subject as a comment bubble (`AssessmentBubble.svelte`, `web/src/lib/descriptionReview.ts`): the description wraps
the intent, its badge coloured by the state of the rating and the bubble opened by a click (by itself only for a
state the methodology declares `open`); the title, the intent, the impacts (rows of the
impact table, processed once the impact is decided by its review), the risks, the actions and the artifacts carry a
compact badge, coloured while pending, whose bubble opens on a click (placed on the viewport, never clipped by a
table); assessments and comments are no longer in the raw items list. A rating is about any subject:
`description`, `title`, `intent`, `impact:<node key>`, `item:<record key or id>` (`assess.Text` over the blackboard),
`rated` holding the text or its fingerprint (`assess.Fingerprint`, FNV-1a 32 bits, printed `fp=` by the brief).
How a bubble reads is declared by the methodology ([ADR 0116](adr/0116-declarative-assessment-states.md)):
`assessments: [{subject, states: [{when, conditions, tone, label, open}]}]` (`methodology.AssessmentView`, a subject
key or a glob, the first state that holds wins; `conditions` read a scoped condition on the subject, `when` is CEL
with `assessment`, `subject {key, kind, id, entry, conditions}` and `conditions`, `condition.AssessmentEnv`), checked
at compile; `Engine.AssessmentStates` (RPC `GetAssessmentStates`) evaluates them against the change, the defaults
(`condition.DefaultAssessmentViews`) settling the subjects no declared state does; the change tab gives the states to
its bubbles (Svelte context `ASSESSMENT_STATES`), which render the tone (ok, warn, danger, info, neutral), the label
and `open`, and judge by themselves only while the states load (`bubbleView`).
**Controls of an activity** ([ADR 0162](adr/0162-controls-of-an-activity.md)): beside what it consumes, an activity is
governed by rules it does not consume. The rules are domain content (`alm@Rule`, `ArchitectureDecision`,
`CodingRule`: a statement, a severity veto | objective | advice, a `check` code text in `cypher` or `shell`, a `fix`;
linked by `alm@governs` to what they govern; lifecycle `rule`); the methodology writes only general rules and how
they apply. A condition is never marked a control: it is one by its **use**. An action or a step lists `controls:
[{condition, types, via, where, rules}]` (`methodology.Control`, `engine/methodology/control.go`): the condition says
the rules were collected (zero rules is a valid answer) and is waited for (merged into the step's entry and the
action's `pre`), the use says which rules it covers; stored, it is the link `methodology@controlled_by` carrying them,
the condition node unmarked. `Compiled.ControlsOf` and
`Engine.ControlOf(change, action, step, subject)` (`engine/engine/control.go`) resolve the controls of an execution in
the view of the change, each established or pending; a run records the rules it was given (`Step.Controls`), an llm
action reads them in a `CONTROLS` section of its system prompt (one line per rule). The builtin `control.check`
(`engine/engine/controlcheck.go`) measures them without a model: a Cypher query over the view (`graphapi.Port.QueryView`,
`subject` bound, columns `at` / `msg`) or a command in the workspace (SARIF, `file:line: message`), and writes a
`control_report` artifact whose output is one line per rule broken, with where and how to fix it.
A methodology declares its own events (`events: [{name, description}]`, names out of the standard families) and
follows them like the standard ones; an unknown event is refused at compile, one followed from a related methodology
that does not declare it is a composition warning. A signal item of that name raises it: an action's `emits` (added
by the engine once its effects hold, `Engine.emitEvents`), a person or a script; `TriggerManager.Handle` turns the
signals of a `change.item_added` into those events (`engine.DeclaredEvents`). The web edits every `on:` with one form
(`SubscriptionsEditor.svelte`, `web/src/lib/subscriptions.ts`: standard and declared events, unknown ones flagged).
`methodologies/risk-management.yaml` applies to `sdlc`: at each step of its processes the risks that step introduces
are reviewed, and every high one gets an action; sdlc's release waits for `risks_under_control`.
**Proofs of concept** ([ADR 0131](adr/0131-proof-of-concept-sub-change.md)): an action of kind `poc` (purpose
`derisk`, the risks it checks; or `explore`, a prototype of the need) is opened by a person (`sdlc` step `proofs/open`)
as a sub-change of the `poc` methodology, which states its hypotheses, builds a probe per hypothesis in its workspace
under relaxed rules, judges it (an assessment `item:HYP-n`, `confirmed` / `refuted`) and is concluded by a person
(`poc.concluded`); the parent's reaction (`proofs/conclusion`, `from: integrated`) closes the refuted risks, reopens
the confirmed ones (a concluded POC covers no risk), marks the action done and abandons the sub-change, which commits
nothing. The build waits for `pocs_concluded`, the confirmation of the framing for `prototypes_concluded`.
**Persistent agents** ([ADR 0112](adr/0112-persistent-agents-and-the-current-state-of-a-change.md)): an agent or a
method declaring `persistent: {scope, until, memory}` has one instance per scope (the change, or a path into the
event: `event.process.methodology`), a process (`Process.Instance`) that every new execution of the agent there runs
again in place (`Engine.Start` → `resumeInstance`; an inbox while it works), until a run completes with its `until`
conditions (`Closed`; leftover events start the next instance, `handOver`). Its llm actions get back its last
exchanges with the model (`Process.Memory`). The observer of `methodology-improvement` is persistent per observed
methodology until `drafted`; a rating is not (`sdlc` 0.7.3: every edit gets an independent rating run, what was rated is
stamped by the engine, `stampRated`, and the continuity is the discussion of the topic, the `THREAD` the brief gives
every run; an action that missed its effects is not run again on its own, `Engine.MaxFailures` 1, ADR 0112 §6). The change tab shows the **current state** of the
change (`Engine.ChangeState`, RPC `GetChangeState`, `ChangeState.svelte`): each process once, its steps judged
against the change as it is with what the live runs do laid over them, and what each step that cannot start needs
(`StepProgress.Needs`: the condition, the value, what it means, the actions producing it and, in `Requires`, the
conditions it cannot hold without that do not hold yet: `condition.Set.Implied` reads from the expressions that
`description_accepted` cannot hold without `description_rated`; `Compiled.Prerequisites` / `Producers`, also taken in
by `NeededBy`; and `Facts`: the terms of the CEL expression evaluated on the change, an exists part by part, the first
missing one with the actions establishing it, `condition.Set.Facts` / `Missing` / `Establishes`).
 **What the change waits for from people**
([ADR 0117](adr/0117-what-a-change-waits-for-from-people.md)): `Engine.ChangeAsks` (RPC `GetChangeAsks`) gathers the
pending tasks of the runs of a change that wait on a person (input, approval, condition, unblock, flow, board, merge,
rebase, the intent question of a clarifying run) and the decision points only a person settles (open questions,
escalated or reserved points, rulings to ratify), each with whom it is addressed to (the roles that may run the action
and the responsible role of the step for an input, the roles and the accountable role for an approval, the accountable
role for a stuck run, else the person the run works for) and `mayAnswer`, computed with the checks of the calls that
answer it (`Engine.maySubmit`, `mayApprove`, `mayUnblock`, plus the run permission the handler asks). The change tab
shows them in one strip above every pane ([ADR 0152](adr/0152-asks-strip-and-impact-decision-history.md), `ChangeAsks.svelte`: folded, the counts for the person and for whom else; unfolded, *Waiting for you*, answered in place with the panel of the run's task,
`PendingTask.svelte`, shared with the run tab; *Waiting for others*, with whom; the impacts awaiting their review join
the list; nothing waiting or the asks unreadable: no strip). The Reviews pane shows the stage of each review (to create,
open, submitted) and a submitted review as it was decided; each impact carries its decision history
(`ImpactDecisions.svelte`, from `ChangeImpact.reviews`).

A process can now run, and even finish, without ever being attached to a Change — binding stays
eager by default for existing methodologies; an agent can declare its own binding action to defer
it (`change_bound`, an ordinary condition `change.id != ""` of the methodology, `Engine.AttachChange`, `goap-scheduler attach`), and a fixed
`"process.attached"` event plus the custom `"change.signal"` event (above) both fire on a bind,
for different purposes — see ADR 0031.

### 2.12 Execution journal and self-observation ([ADR 0011](adr/0011-journal-auto-observation.md))

The execution of a change is **captured on the change axis**, alongside the blackboard:

```
change CR-42
 ├─ items (blackboard) ── item.execution ──┐
 └─ journal                                ▼
     process.started · schedule (why the run happens: started, input, approved, sub-agent, relaunched…,
     by whom, queue time) · tick (world, plan, replanned?) · action (specialization, effects, items,
     tokens, LLM / tool calls, traceId/spanId) · approval · process.ended (status, totals)
     every record carries the flow branch its process runs on
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

`kind: script` actions are **JavaScript** code entered in the IDE, run by goja or by QuickJS compiled to
WebAssembly on wazero (`GOAP_JS_ENGINE`, [ADR 0134](adr/0134-javascript-in-webassembly.md)). The engine
injects a `ctx` object (reference: [docs/dsl.md](dsl.md)):

- **reading** the blackboard (hydrated items) and the reference **domain** (`node`, `nodes`, `links`);
- **writing** to the change (`addImpact`, `proposeNode`, `proposeUpdate`, `proposeLink`, `addArtifact`,
  `decide`, and the change impact calls `impactNode`, `impactNodeCreate`, `writeNode`, `impactNodeReview`, ADR 0024) — buffered, applied at the end of the action;
- **platform calls**: `llm` / `complete` (model gateway), `runAgent` (sub-agents), `callTool` (MCP), `log`.

The engines expose neither files, network, nor processes (no `require`, no `std` / `os` module), with a
timeout; the wasm engine also caps the memory of a script and stops it for real at the deadline. The code runs in the process's **sandbox** (§3.6).

### 2.13 Algorithms: the DSL plugged into the domain ([ADR 0018](adr/0018-algorithms.md))

The DSL is a generic capability tied to a **usage** (which fixes the `ctx` the code sees): `action`
(§2.10, code inline in the agent's action) and five *pluggable* usages of the domain —
`property_validator`, `node_validator`, `link_validator`, `transition_guard`, `transition_action`. A domain declares
**algorithms** (JavaScript or Go, with typed parameters) and **instances** (parameter values);
attributes plug property validator instances, node types plug node validator instances (checked on the node as a whole, after the attributes'), link types plug link validator instances (checked on the link as a whole with both of its ends, after the validators of the link's attributes; [ADR 0100](adr/0100-validation-at-three-levels.md)), lifecycle transitions plug guard and action
instances, in call order. The type catalogue (the graph's copy of the registry's model) resolves the plugged instances (like the lifecycle, ADR 0014): validators run when items are added and when a change is applied, guards and
actions when a transition is applied. Reference: [docs/dsl.md](dsl.md), IDE: the *Algorithms* group of a domain version.

**Attributes** ([ADR 0055](adr/0055-attributes.md)): a node type (and a link type) defines its properties as attributes — code, label, type, widget, enum, default, section, order, tooltip, `asName` — resolved by the type catalogue (inherited through `extends`, overridable by name); the graph checks the type and enum membership of the values on commit, and the web renders and edits a node from them. Attributes are **strict**: a node carries the attributes of its type (ancestors included) and nothing else, so a property that is no attribute is refused (`ErrInvalid`, "property X is not an attribute of T") whenever it is written (create, update, checkout, transition, check at accept and at commit: `typeIndex.checkAttributes`, `TypeCatalog.AttributeNames`). A type that must hold arbitrary properties says so explicitly with `additionalProperties: true` on the node type (inherited by its subtypes, a checkbox of the type editor); a type the catalogue does not know and the untyped graph stay permissive. Everything the platform writes (seeds, sign-in, the registry's methodology nodes, the observer) is therefore declared by the built-in domains, and the web offers a node's declared attributes only (a free "new property" field appears for free-form types). A domain also declares **enums**, the closed lists enum attributes refer to. An attribute of type `code` ([ADR 0152](adr/0152-code-attributes.md)) holds source text with its language: a front matter naming the language, then the fenced code block (`contracts/codetext`, read the same by `web/src/lib/codeText.ts`); the graph refuses a malformed one, the web shows it highlighted with its line numbers (`CodeView.svelte`) and edits it with its language (`CodeValueEditor.svelte`), the OKF export writes it as a fenced block; the git importer fills the source text of each declaration this way (`code@Symbol.code`). An attribute names how a change of its value is shown, its `diff` ([ADR 0155](adr/0155-attribute-value-diffs.md): `value`, `words`, `lines`, `json`, `code`; empty: the usual one of its type, `def.Attribute.DefaultDiff`), and one component shows every diff of values in the web (`ValueDiff.svelte`, `web/src/lib/valueDiff.ts`: one line for a short change, else the lines unified or side by side, unchanged runs folded, code highlighted in its language): the impact diff, the comparison of options and of baselines, the history of a node, the merge resolver, the node tab.

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
| **registry** | Methodologies (graph data, ADR 0023): editing (draft), validation against the domains of the domain registry, publishing, versions, suspension of a version the domains in force break (ADR 0125 §3), YAML import/export | Connect `registry.v1.RegistryService` | the graph | 🟢 |
| **engine** | Intent loop, planning, process execution; deployable as a cluster | Connect `engine.v1` | `engine` | 🟢 core (memory) |
| **graph** | Domain axis (versioned nodes, links, baselines) + change axis (Changes, change impacts, facts, apply); its type catalogue read from the domain registry | Connect `graph.v1` | `graph` | 🟢 |
| **domains** | The **domain registry**, the definition of the graphs (ADR 0125 §3, [ADR 0167](adr/0167-domain-registry-a-service-of-its-own.md)): domain versions, the built-in domains protected, the type catalogue, YAML / JSON import-export ([ADR 0104](adr/0104-domain-and-graph-import-export.md)), a command line for the protected domains | Connect `registry.v1.DomainService` | `domains` (`domain_version`, `domain_protection`) | 🟢 |
| **modelgw** | Multi-provider / multi-model abstraction, aliases (`default`, `fast`, `reasoning`), administered catalog with global token quotas and required roles (see below), traces; `Suggest`, the stateless contextual helper over the `helper` alias (ADR 0086); the **one ledger of LLM calls**: `ListUsage` / `UsageSummary` (ADR 0089) | Connect `model.v1` | `modelgw` (quota counters and the ledger of LLM calls, ADR 0089; the configuration is graph data, ADR 0021) | 🟢 core |
| **preferences** | Personal preferences of the users (theme, voice input, dashboard defaults), one document per subject, outside the graph ([ADR 0038](adr/0038-user-preferences-service.md)); a caller reaches only their own, saved as they change it | Connect `preferences.v1` | `preferences` (`user_preference`) | 🟢 |
| **conversations** | Conversations of the users with the assistant (messages, structured UI actions), per subject, outside the graph ([ADR 0085](adr/0085-conversation-service.md)); the owner reads and writes, a system principal appends and updates the assistant's messages | Connect `conversations.v1` | `conversations` (`conversation`, `conversation_message`) | 🟢 |
| **assistant** (hosted by `conversations`) | The conversational assistant: answers the messages of a conversation on the `assistant` model alias as the caller, with its tools declared to the model as native tools ([ADR 0098](adr/0098-native-tool-calling.md), like the screen tools) following the frame of the turn (`platform/assistantsvc/frame.go`: outside any project, when none is named, only list projects, select project, search and open change; in a project, a project's tools; on the working change, a change's ([ADR 0135](adr/0135-working-context-project-and-change.md)): a change only shown is named, not worked on; `select_project` and `open_change` move the frame for the next rounds of the turn, and each answer records where it worked, `Message.context`, shown above it in the web) (list projects, list methodologies, select project, search the open changes, create change (offered once the open changes were searched in the conversation, [ADR 0106](adr/0106-methodologies-integrating-methodologies.md)), open change, propose to attach or detach the change it is on, list the agents the caller may run here, propose to start one, and `methodology_query`: read what a methodology declares, filtered by kind / name / text / parent, projected and capped at 6 KiB, [ADR 0094](adr/0094-assistant-methodology-query.md)); the pending message is the run, and an agent starts only when the person confirms the proposal (`ConfirmAction`, [ADR 0087](adr/0087-conversational-assistant.md), [ADR 0090](adr/0090-assistant-runs-agents.md)); it is contextualised to the open screen (layered context focus / screen / app) and may ask the interface to act through the tools the screen offers (`ui.*`: effects run at once, writes only after the person's confirmation and reported with `ReportAction`, [ADR 0092](adr/0092-contextual-assistant-screen-tools.md)); a long conversation keeps its older messages as a rolling summary (`platform/assistantsvc/compact.go`: once `CompactBatch` messages fell out of the history window, the summary is rewritten after the turn from the previous one and those messages: the recorded facts (what the actions of the messages proposed, started or changed and how it ended) written by code, never by the model, kept first with their exact ids and dropped oldest first past 2.5 KiB, then the model's text for what only the words say, durable facts kept verbatim, stored by `conversations` in `conversation_summary` with the seq it covers, `convsvc.Service.Summary` / `SetSummary`, and sent with the rules of each turn) | Connect `assistant.v1` | none (writes through `conversations`) | 🟢 |
| **indexer** | Document index ([ADR 0026](adr/0026-node-index-and-search.md), [0095](adr/0095-changes-in-the-index-typed-search.md)): follows the node / baseline / change-document events of the graph, embeds through modelgw (alias `embed`), answers lexical / semantic / hybrid and nearest-document searches over nodes and changes with facets, filtered by ABAC and project | Connect `index.v1` | `index` (tsvector + pgvector; FTS5 + exact cosine scan in SQLite) | 🟢 |
| **mcp** | MCP hub: connector registry (self-registration), resolution of the adapters and their restrictions (graph) along the organisation hierarchy, tool calls, the built-in connectors of the platform (§3.9) | Connect `mcp.v1` | `mcp` | 🟢 |
| **connector-\*** | One service per real system (`connector-localfs`, ...), registers itself with the hub | Connect `connector.v1` | — | 🟢 localfs |
| **goap-runner** | Sandbox for executing script actions (one per process) | Connect `runtime.v1` (SandboxService) | — | 🟢 |
| **otel-collector** | OTLP reception, trace export (Jaeger) and metrics (Prometheus) | OTLP | — | 🟢 |
| **vault** | Secrets (LLM API keys, MCP credentials, DSN) | HashiCorp Vault KV v2 | — | 🟢 dev mode |

### 3.0 Source components ([ADR 0125](adr/0125-source-components.md))

The picture of this section and of the services is [architecture-overview.html](architecture-overview.html) (open
it in a browser): the modules, the containers with their ports and calls, the deployment units and one step of a
change end to end. It is kept in step with the code (CLAUDE.md, `TestArchitectureOverview`).

The services are cut along components the sources follow too: a **contract** (protos, generated code, the pure
model, `contracts/graphapi`: the requests, answers and errors of the graph's operations), the **graph**, the **engine**, the
**MCP hub**, the **gateway** and the **platform** that composes them. The engine reaches the graph only through the
ports it defines (`engine.GraphPort`, written with `contracts/graphapi`), the graph and its service name no engine type;
each component is a Go module of the `go.work`, one directory at the root (`contracts`, `lib`, `graph`, `engine`,
`mcp`, `gateway`, `platform`; import paths `github.com/zimwip/goap/<module>/<package>`), whose `go.mod` requires the
modules it may depend on (contracts ← lib ← graph / engine / mcp / gateway ← platform). `platform/layering` checks
every import, the tests' included (`TestComponents`), and each `go.mod` (`TestModuleRequirements`); every
`platform/cmd/*` is the platform's composition.

Two registries: the **domain registry** (`graph/domainsvc`, `registry.v1.DomainService`) is a service of its own
([ADR 0167](adr/0167-domain-registry-a-service-of-its-own.md): `platform/cmd/domains`, Rust `goap-domains-rs`, port
8096, a database of its own): it seeds the built-in domains protected at each start (`SeedBuiltinDomains`, from
`GOAP_BUILTIN_DOMAINS_DIR`, else `GOAP_DOMAINS_DIR/builtin`, else `lib/domains/builtin`), then the shipped domains; a
protected domain (`domain_protection`) is refused every write over RPC and written by its command line only (`publish`,
`protect`, `unprotect`, `list`, on a context marked `domainsvc.Local`); it reads the organisation's rules from the
platform graph; it names no methodology. Every graph is its client (`GOAP_DOMAINS_URL`): it waits at its start for the
built-in domains, then reads the domains in force again on the registry's events (`typecat.NewLive` over
`lib/domainclient`; Rust `platform::Registry`), and its catalogue is exactly those domains (`typecat.Of`). The
**methodology registry** (`platform/registrysvc`, `platform/cmd/registry`) goes with the engine: it reads the domains through a
port (`registrysvc.Domains`: the service in process, `lib/domainclient` over RPC) and the graph through
`graphapi`. Publishing or archiving a domain is never refused for a methodology: the domain registry emits
`goap.registry.domain.published` / `deleted`, the methodology registry checks every published methodology version
again (`DomainsChanged` → `Recheck`, also at start) and **suspends** the ones that no longer compile against the
domains in force (`ErrSuspended`; events `goap.registry.methodology.suspended` / `resumed`; nothing stored): no change
starts on a suspended version, no run reads it, the lists leave it out, until a compatible methodology version is
published or the domains come back. The graph and the domain are never blocked. Migrating the data of a domain is a
separate matter.

A domain also declares the **sources** its graph can be loaded from (`importers:`, ADR 0148), each implemented by a
separate service the way a connector implements an MCP: the service, built on `lib/importerkit`, is a worker of the job
service claiming the jobs of kind `import.<domain>.<importer>`. The **import registry** the graph serves
(`graph/importsvc`, `importer.v1.ImportService`) lists the declared sources with whether a live worker claims them,
checks the parameters of a run against the declaration and the permission `import:run`, and creates its job for the
person; the run writes through the graph's change operations as `system:<service>` for the person. The first is
`importer-git` (`platform/cmd/importer-git`, the `git` source of `code`, ADR 0147); the Baseline explorer opens the
*Import from a source* tab. A parameter whose widget is `directory` names a directory on the machine of the service, so
the browser cannot pick it itself: `BrowseImport` creates a short job of kind `browse.<domain>.<importer>`, which an
importer implementing `importerkit.Browser` answers with the directories it may read (its roots, then the
subdirectories, git repositories flagged), and the registry awaits it; the tab's folder chooser walks that listing.

The long runs are **jobs** (ADR 0149): `platform/jobsvc` (`platform/cmd/jobs`, `goap.jobs.v1.JobService`, port 8091,
tables `job` / `job_blob`, outbox `job_outbox`) keeps a record per run (kind, label, target, the person, status
`queued` → `running` → `done` | `failed` | `cancelled`, progress line, `done` / `total`, counters, output, result file).
The service owning a kind authorizes and creates the job for the person (`CreateJob`, the services' token
`GOAP_JOB_TOKEN`); workers pull (`lib/jobkit`: `Queue`, `Client`, `Worker`, `Job`; `ClaimJob` long-polled, `ReportJob`
renewing a 30 s lease and answering a cancel, `ReadJobInput`, `WriteJobResult`; a lapsed lease fails the job). The
graph process is the worker of `graph.export` (JSON or OKF, `StartGraphExport`) and `graph.import` (`StartGraphImport`,
the snapshot as the job's input), `graphsvc.Handler.JobRunners`; an importer service the worker of its importers. A
person lists, cancels and downloads their jobs, and follows them over Server-Sent Events (`GET /api/jobs/events`,
`GET /api/jobs/<id>/result`, routed by the gateway): the web's `stores/jobs.svelte.ts` reads the stream, the status
bar shows the running jobs and opens the Jobs tab.

The engine is tested without the graph: the scenarios that run it on a real graph are the platform's integration tests
(`platform/e2e/engine`), reaching the engine's unexported workings through `engine.Internals` only. So are the other
tests that need two components (`platform/e2e/<name>`: the hub, the connectors and the access over a seeded graph).

The web's TypeScript is generated from the protos too (`web/src/gen`, `protoc-gen-es` with the JSON forms); the
hand-written API types are checked against it field by field (`web/src/lib/api/contract.test.ts`).

### 3.1 Communication

- **External**: HTTP via Echo on the gateway. The frontend uses the **Connect JSON**
  protocol (`POST /goap.engine.v1.EngineService/StartProcess`); the web calls it with its own types, checked against the
  TypeScript generated from the protos (§3.0).
- **Synchronous inter-service**: **connect-rpc** (HTTP/2 h2c internally, HTTP/1.1 compatible). Contracts in
  `proto/`, generated by `buf` into `contracts/gen/`.
- **Asynchronous**: **NATS JetStream**. Subject conventions:

| Subject | Emitter | Content |
|---|---|---|
| `goap.process.<id>.started` / `.attached` / `.step` / `.step_completed` / `.intent` / `.waiting` / `.clarifying` / `.stuck` / `.completed` / `.failed` / `.cancelled` / `.superseded` | engine, through the outbox of its process store (ADR 0136), with the process | `ProcessEvent` |
| `goap.process.<id>.log` | engine, published directly (a live display, ADR 0136 §4) | `ProcessEvent` with a `LogLine` |
| `goap.change.<id>.created` / `.edited` / `.attached` / `.detached` / `.item_added` / `.signal` / `.committed` / `.transitioned` / `.applied` / `.purged` | graph, through its outbox ([ADR 0136](adr/0136-each-service-emits-its-events-through-an-outbox.md)), from the operation that writes the fact | `ChangeEvent` |
| `goap.changed.<id>` | graph, through its outbox | `ChangeEvent` of type `change.updated`: the header, the impacts or the log of a change were written (the web) |
| `goap.changeindex.<id>` | graph, through its outbox | `ChangeDocEvent`: the document of a change for the index |
| `goap.node.<ns>.<type>.<id>.written` | graph | `NodeEvent`: one node version written (any branch), with its searchable text and facets resolved from the `search` declaration of its node type |
| `goap.baseline.<branch>.advanced` | graph | `BaselineEvent`: diff with the parent baseline (the `main` facet of the index follows the head of each namespace's own main) |
| `goap.registry.methodology.saved` / `.published` / `.deleted` | methodology registry, carried by the graph commit that stores the version (graph outbox) | name + version + status |
| `goap.registry.methodology.suspended` / `.resumed` | methodology registry, published directly (recomputed, ADR 0136 §4) | name + version + reason |
| `goap.registry.domain.saved` / `.published` / `.deleted` | domain registry (`domains`), through its outbox (`domain_outbox`) | name + version + status |
| `goap.credentials.<subject>.registered` / `.password_changed` / `.session_started` / `.session_ended` | credentials, through its outbox | subject + session + time (never a password or a token) |
| `goap.preferences.<subject>.updated` / `.reset` | preferences, through its outbox | subject + the keys changed (never the values) |
| `goap.conversation.<id>.created` / `.renamed` / `.deleted` / `.message_appended` / `.message_updated` / `.action_updated` / `.summarized` | conversations, through its outbox | conversation, owner, message, role, action index, status (never a text); the event stream delivers it to the owner only |
| `goap.mcp.connector.<id>.registered` / `.lost` / `.back` | hub, through its outbox | connector id, endpoint, liveness |
| `goap.model.call.<seq>.recorded`, `goap.model.quota.<subject>.exceeded` | model gateway, through its outbox, with the ledger row | the row (subject, end user, source, alias, model, tokens, outcome; never the exchange) |
| `goap.job.<id>.created` / `.started` / `.ended` | job service, through its outbox (`job_outbox`, ADR 0149) | id, kind, target, status, by (the progress travels on the job stream, `GET /api/jobs/events`) |

Each service emits its own events ([ADR 0136](adr/0136-each-service-emits-its-events-through-an-outbox.md)): an event is
written in the outbox table of its database in the transaction that writes its fact, and a relay (`lib/outbox`, started
by `outbox.Start`) delivers it afterwards, in order and at least once, with its id `<service>:<seq>` (`Nats-Msg-Id`,
`X-Goap-Event-Id`). No caller emits an event on behalf of a service. Each service has its own table, the local mode
sharing one database: `event_outbox` (graph), `process_outbox` (engine: a process and its event in one transaction; its
replicas deliver one at a time under the shared lock `outbox`), `domain_outbox`, `credentials_outbox`,
`preferences_outbox`, `conversation_outbox`, `mcp_outbox`, `model_outbox`. The methodology registry, whose facts are
graph data, has its events carried by the graph commit that stores them (`graphapi.Commit.Events`, from a `system:`
principal only), written in the graph's outbox after its `change.applied`. The consumers that act on
an event are durable and idempotent: the trigger manager reads `goap.change.>` and `goap.process.>` through one
durable consumer shared by the engine replicas (in process in the local mode: the relays' sinks, never the lossy
broker), handles an event id once (`platform.EventIDOf`, `outbox.EventID`) and rate-limits a trigger per change.

### 3.2 Engine clustering ([ADR 0124](adr/0124-engine-replicas-share-processes.md))

Several engine replicas share one process store (`internal/enginesvc.SQLStore`: PostgreSQL with `GOAP_DB_DSN`, SQLite
in the local mode). The work on a process is serialized across them by a shared lock (`engine.Locker`, a lease per key
in table `engine_lock`, renewed while held, `Engine.LockTTL`): the process, its supersede, the companions of a change,
the instance of a persistent agent. The store refuses the write of a replica that lost the lock of a process
(`engine.ErrFenced`); `Cancel` and a supersede reach the holder through the lock; `Engine.Recover` resumes the running
processes whose lock nobody holds (a replica stopped, or one at capacity, `MaxRuns`, left it to the pool). A step is
recorded before it runs; a run resumed after a stop closes it from what its execution wrote on the change (ended,
cut halfway: failed, nothing: run again), so a step never writes twice. Core NATS carries the hints (`engine.wake`,
`engine.interrupt`); the locks stay the arbiter.

### 3.3 Persistence

- **Local without containers**: a SQLite file per service in `.goap/local` (the local stack, migrations
  `migrations_sqlite/` per component, [ADR 0010](adr/0010-local-sqlite-mode.md), [ADR 0164](adr/0164-local-stack-replaces-goap-dev.md)).
- **Dev**: one PostgreSQL instance, **one schema per service** (`graph`, `engine`, `index`,
  `modelgw`, `preferences`, `conversations`, `mcp`) and a dedicated role per service (`deploy/postgres/init.sql`).
- **Prod**: one database (or cluster) per service; only the DSN changes (`GOAP_DB_DSN`, read from Vault).
- Migrations embedded in each service (`embed.FS`), applied at startup (advisory lock).

`graph` model (simplified):

```sql
node(id uuid, namespace, key text, type text, latest int, project text)  -- the project it was created in, a logical URL without version (ADR 0154 §9)
node_version(node_id, version, props jsonb, deleted bool, change_id → change, owner text, created_at, committed)  -- owner: a logical URL without version (ADR 0154 §9); PK (node_id, version); committed: the order of its baseline (ADR 0140)
link(id uuid, type, from_id, from_version, to_id, to_version, to_url, props jsonb, change_id → change)  -- to_url: a target in another source (ADR 0154), to_id / to_version null then
node_branch(node_id, version, branch, change_id → change, committed)  -- the branches a version joined besides the one it was written on (ADR 0032)
baseline(id uuid, name, namespace, branch, parent_id, change_id, created_at, seq)  -- seq: the order of commits; its state is a selection (ADR 0143)
branch(namespace, name, parent, fork_baseline, head_baseline, origin, status, created_at)  -- PK (namespace, name)
change(id uuid, title, intent, status, baseline_id, goal, methodology, result_baseline_id, data jsonb,
       owner_org, project_id)  -- never empty (ADR 0054)
change_impact(id uuid, seq, change_id, node_id, key, type, intent, rationale, pre_version, post_version, committed_version,
              review, reviews jsonb, via, flow, superseded, ...)  -- the projection of the impact events (ADR 0029)
change_log(seq, id, change_id, type, flow, process_id, execution, subject, by_whom, causation, at, payload jsonb)  -- facts, journal records, impact events (ADR 0030); causation: the entry it follows from (ADR 0105)
tag(id uuid, name, namespace, node_id, version, by, created_at)  -- names one node version, FK (node_id, version) → node_version (ADR 0143 §6)
smart_tag(id, name, description, owner, by, created_at, updated_at), smart_tag_version(tag_id, node_id, version), smart_tag_share(tag_id, subject, access)  -- graph/smarttagsvc, own migrations (ADR 0143 §7)
```

**Two dialects, one model** ([ADR 0074](adr/0074-sql-dialect-builders-and-schema-guard.md)): the PostgreSQL repository
(`graph/graph/postgres.go`, pgx) and the SQLite one (`sqlite.go`, `database/sql`) keep their executors, row scanning and
value encodings (uuid / text, jsonb / text, arrays, timestamps), and share the statements: `graph/graph/sqlbuild.go`
builds the text and arguments of the queries both run from one `dialect` value (placeholder, `::text` cast of a uuid,
nullable test, order tie-break), `sqlbuild_test.go` holds the text each query had in each dialect, and
`lib/sqlschematest` compares the two migration directories of a service (`TestSchemasAligned` in `graph/graph`,
`credsvc`, `mcpsvc`, `modelgw`, `prefssvc`, `registrysvc`; `graph/index` differs by design: pgvector / FTS5). `make test-pg`
runs every test against PostgreSQL (a throw-away `pgvector/pgvector:pg17` container unless `GOAP_TEST_PG_DSN` is set).

**Concurrent writers** ([ADR 0127](adr/0127-writers-of-a-change-serialized.md)): the writers of one tree of changes (a
root change and its sub-changes) are serialized by `Tx.LockChange` (a transaction-level advisory lock on the root in
PostgreSQL; SQLite and the memory store run one transaction at a time), taken before an operation reads what it checks
(`changeOpen`, `Graph.changeTx`) and by the guard at every write naming a change; a branch head moves under
`Tx.LockBranch` and only from the parent of the new baseline (`advanceBranch`). Reads take no lock.

Every `change_id` above is `NOT NULL` with a foreign key (deferred where the bootstrap needs it); `owner`, `project`,
`owner_org` and `project_id` are non-empty logical URLs of nodes of the structures (`lnk:<namespace>/<type>/<key>`, no version: a reference to a node is a link; `Graph.StructureURL` the canonical form, `Graph.StructureRef` reads a URL or a bare key), no foreign key, since the structures may live in
another graph (the platform graph, ADR 0154 §9; the guard checks them through `Graph.Directory`, the platform's organisation directory in a content graph), and `graph/graph` enforces the same rules in Go whatever the storage (its guard, ADR 0054): the constraints
are a second line, a storage without any keeps the rules.

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
- Secrets are never in environment variables in prod: `lib/platform/secrets` reads from Vault
  (KV v2, token auth in dev / Kubernetes auth in prod) with a fallback to the environment in dev.

### 3.4a Logical URLs and federation ([ADR 0154](adr/0154-logical-urls-and-graph-federation.md))

What a link points to can be named by a logical URL, `lnk:<source>/<type>/<key>` (`contracts/lnk`): a source is where
things live (a namespace of this graph, a namespace of another graph service, a directory of files), the type and the
key say which thing. Every namespace of the graph is a `graph` source of its name with no location, read in process
(`graph.Resolver`); other sources are `platform@Source` nodes (`SRC:<name>`, admin only), read by the resolver of their kind
(`Graph.Resolvers`: `graphclient.Federation` for a `graph` source with a location, `lnk.Files` for `files`). `Graph.Lnk()` is the registry,
`Graph.Resolve` reads a URL, `Node.URL()` / `Node.SourceName()` give a node's. Sources are declared on the platform and resolved per project: a project maps the source names of its URLs to
declared sources (`ProjectUnit.sources`, inherited, nearest wins), so its content can be stored elsewhere
(`Graph.ResolveIn`, `Graph.ResolveFrom`); a link targets a node version of this graph or a URL of another source (`Link.Target`).
The platform and the content are two graph services (ADR §9): the platform graph (`platform`, `organisation`, `methodology`)
is the one its callers know and forwards the calls of the content namespaces or of a change held there to the content
graph (`graphsvc.Router`): the routes are the declared sources, a `platform@Source` of kind `graph` with a location and the
`namespaces` it holds being the driver of those namespaces (`Graph.Sources`, `lnk.Source.Holds`), and the namespace
listings are merged over every graph, each naming its source (the statistics group them). The content graph
(`GOAP_GRAPH_SPACE=content`) reads its types and its organisation from the platform graph (`Graph.Directory`,
`graphsnap.Heads`), holds no root and seeds nothing.


The platform graph delivers the policies, the organisation and the projects to every graph and every service, itself
included, the same way (`Directory.FollowEvents`): each graph's `access.Directory` reads them from the platform graph (in process, else its client) and keeps
its snapshot (`Directory.Follow`) until a baseline event of the organisation's namespace (`domain.BaselineEvent`
carries its namespace) reaches `Directory.Moved`; a change of a policy is a commit on the platform graph, its outbox
sends the event, every graph refreshes.

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

| | PostgreSQL | SQLite (local stack) |
|---|---|---|
| Full text | `tsvector` + GIN | FTS5 |
| Vectors | pgvector (HNSW created for the model's dimension) | float32 BLOB, exact cosine scan |
| Facets | `jsonb` | `json_extract` |

**Changes in the index and typed search ([ADR 0095](adr/0095-changes-in-the-index-typed-search.md))**: the same table,
text index and vector index hold a second kind of document, the **change** (`kind` = `node` | `change`). The graph
publishes a `ChangeDocEvent` on `goap.changeindex.<id>` whenever a change's header or impacts are written (and a
`deleted` one when it is purged); its text is title, intent, goal, methodology, namespace and the keys / types of its
impacts, bounded (4000 runes), never the items, decisions, reviews or drafts. `Search` takes `kinds`, `types`,
`namespaces`, `states`, `branches`, `main`, `projects` (+ `include_subprojects`), `owner_units`, `statuses`,
`methodologies`, `roots_only`, a `mode` (`lexical` | `semantic` | `hybrid`), a per-request `min_similarity`, `snippet`
and `similar_to` (the documents nearest to an existing one by its stored embedding). Results are authorized before
anything is counted, totalled or quoted: a node needs `read` on its type and access to its project; a personal change
is for its subject only (administrators included); another change needs access to its project
(`access.Snapshot.MayAccessProject`; the indexer asks it through the `indexersvc.Access` seam, built from
`access.Directory`).

The indexer follows the graph's events over NATS. The compose image is `pgvector/pgvector`; the `index` schema needs `CREATE EXTENSION vector`
(`deploy/postgres/init.sql`).

### 3.5 Deployment

- `deploy/compose/docker-compose.yml`: postgres, nats (JetStream), vault (dev), all services, web,
  otel-collector, Jaeger, Prometheus, Grafana, restricted Docker API proxy (sandboxes).
- **SQLite** ([ADR 0010](adr/0010-local-sqlite-mode.md)): every service keeping a store runs on a SQLite file when
  no PostgreSQL is configured (`GOAP_SQLITE_PATH`, the local stack below); no path at all keeps the
  ephemeral memory mode. The file has one writing connection (`platform.OpenSQLite`: SQLite has one writer) and a pool of
  read-only ones (`platform.OpenSQLiteReader`, WAL): the plain reads of the engine, job and domain stores go there,
  and so do the graph's transactions in a read-only context (`platform.ReadOnly`, set by `platform.ReadOnlyRequests`
  on a GET or a Connect procedure named `Get…`, `List…`, `Export…`, `Diff…`, `Search…`, `Compare…`, `Is…`,
  `Watch…`, `Query…`; a write they try runs again on the writer). A long write, an import, then holds no reader of the IDE.
  PostgreSQL does the same in `platform/cmd/graph`: a second pool whose sessions are read-only
  (`platform.PostgresReader`, `default_transaction_read_only=on`) serves the graph's transactions of a read-only
  context on one connection with no `BEGIN` / `COMMIT` (`Postgres.WithReader`; PostgreSQL reads in READ COMMITTED, so a
  transaction gave them no snapshot, only two round trips); a write they try fails with SQLSTATE 25006 (a lock of a
  change or a branch refuses at once) and the transaction runs again on the writer. The graph test variant
  `postgres-readers` runs every transaction there first.
- **The compose stack without Docker** (`make dev`, `make local`, `.\make.ps1 dev`; `platform/cmd/goap-local`,
  [ADR 0164](adr/0164-local-stack-replaces-goap-dev.md): it replaces `goap-dev`): one process per
  binary of `platform/cmd` as the compose stack runs them, on its host ports (gateway 8080, graph 8081 ... jobs 8091,
  content graph 8092, domain registry 8096, started first: `goap-domains-rs` with the Rust graph, else the Go
  `domains`), each service on a SQLite file of its own in `.goap/local` (`GOAP_SQLITE_PATH`, read by
  `platform.OptionalSQL` in every service that keeps a store, the graph services included; `GOAP_DB_DSN` wins, neither:
  memory), an embedded NATS server (JetStream, `.goap/local/nats`) instead of the container, the secrets generated
  in `.goap/local/secrets`; the graph is Rust (`LOCAL_GRAPH=go` for the Go one), bootstrapped by its `bootstrap`
  command before it serves; before anything starts, every port of the stack (NATS and vite included) must be free,
  else it stops naming each port taken and, on Linux, the process holding it (`ports.go`: a previous run's service
  would answer the readiness checks in place of the new one); a service starts once the services whose URL its
  environment holds answer `/readyz` (`deps.go`: `service.lazy` breaks the cycles, the graph's registry, the hub's
  engine; a cycle left is refused), the others together; the processes are restarted when they stop, their output
  prefixed and kept in `.goap/local/logs`; each runs in its own process group (Setpgid, a new process group on
  Windows) so that Ctrl-C (or SIGHUP, a terminal closed) interrupts them and what they started, kills them after 10 s
  or at a second Ctrl-C, a third leaving at once; on Linux they get SIGTERM when goap-local dies (`Pdeathsig`). A Go
  service's shutdown lets its requests end for 2 s, then ends its streams (the event stream, server-sent events)
  through their context (`platform.Server`'s `BaseContext`), rather than waiting for them until its deadline. `-watch` (`make dev`,
  `make dev-backend`) polls the sources every second and, once they settle, builds the Go binaries depending on a
  changed package (`go list -deps`) and the cargo ones on a change of `rust/` or of the graph's migrations (cargo on a
  loop of its own, the Go services reloaded meanwhile; profile `-cargo-profile`, `dev-local` for `make dev`: the
  workspace's crates at `opt-level` 0, incremental, the dependencies optimized: a change of `graph-core` rebuilt in
  about 15 s where release takes minutes), replaces a
  binary only when its bytes changed and restarts its services (stopped, binary put in place, started again); a change
  of `domains/` restarts the graphs, of `methodologies/` the registry. `-web` runs vite (`npm run dev` in `web/`) under
  the same supervisor, with `--strictPort`. `make local-build` builds the binaries in `.goap/bin` (Go, then
  `goap-graph-rs` and `goap-proxy` with cargo, `LOCAL_CARGO_PROFILE`, release by default). `goap-local test` (`make local-test`) runs the stack on fresh data, then the end-to-end
  test of a running platform, `platform/e2e/stack` (`GOAP_STACK_URL`: registry, a commit on the content graph read
  back, the index following it over NATS, an export job, the engine), then stops; `goap-local bench`
  (`make local-bench`, `LOCAL_PROXY` go | rust | compare | shadow, `PERF_ARGS`) runs the graph alone behind goap-proxy
  loaded by goap-bench, then the proxy's report and, in mode compare, `goap-proxy diff-db` over the two SQLite files.
  Two graphs on one bus name their events apart (`Graph.Origin`, `graph-content` for a content graph: the bus
  deduplicates by event id) and their job kinds (`Handler.Space`: `graph.export.content`), so that each worker runs
  what its own store holds.
- **The graph service in Rust** ([ADR 0163](adr/0163-graph-service-in-rust.md), its four phases done: the compose stack runs it): the Cargo workspace `rust/`
  (outside the `go.work`, generated from the same `proto/`, its JSON timestamps written as Go's `protojson` writes them,
  `goap_proto::wkt::Timestamp`) holds `goap-graph-rs`, the graph's four services on the
  same schema (PostgreSQL or SQLite, the migrations of the Go services embedded), every method served (where it cannot
  know Go's words, a CEL guard cel-go may refuse, a script goja may, it decides in its own); the reads of the bench are written (`graph-core`: views, selection, neighbourhood, pages, change
  headers, baseline differences, over the store trait `ReadTx`; `graph-store` `read/`: the SQL of both dialects) and so
  are its writes (`graph-core` `write/`: changes, impacts, the review batch, commit, fast-forward merge, `CommitEdits`,
  `ImportDomain` (`graph-catalog` `imported`: the YAML parsed by `serde_norway`, laid into the shape of `def.Domain` by `decode`; the issues of `Domain.Validate` by `validate`, `form` and the `cel` crate; the doubles of every answer written as protojson writes them, `goap-connect`), over `WriteTx`, in one transaction with the outbox) for a platform service acting by itself in the simple world of
  ADR 0163 (no lifecycle guard; the rebase, the sub-changes, the three-way merge of branches and that of a change whose target moved meanwhile served), `GetDomain`, `ListTypes`, the YAML and JSON export of a domain (built-in ones included), the draft, publication, new version and deletion of a domain (a person's asked of the organisation's rules) (and `ValidateDomain`), the reads of the catalogue, `ListNamespaces`, `ListTags`, `ListBaselines` (a branch's history too), `ListBranches`, `GetBranch` (a change's branch too), `GetChangeImpacts`, `ListSubChanges`, `ListViewNodes`, `ResolveView`, `GetCommitState`, `ListChangeLog`, `ListChangeEvents`, `GetBaselineGraph`, `GetBaselineDelta`, `ListBaselineLinks`, `GetChangeStack`, `DerivedNodes`, `GetSharedNodes`, `GetChangeDependencies`, `GetGraphStats`, `GetGraphOverview` (with `ListNamespaces`, merged with the answers of the other graphs declared), the tags and branches (a person's too: a tag in the caller's reach, `tag:create` / `tag:delete`), the commit and rebase states and statistics of a person, a person's flows, options, decisions (the seat of a board), journal entries, dependencies, attach, detach, move, methodology, transition, upgrade, proposed impacts, rebase and branch merge, a platform service's change dependencies and journal entries (`AppendLog` (an unknown change refused in Go's words), `ListExecutions`, `AddItems` of artifacts, decisions, signals and the item types no code reads, `UpdateChange` but a criticality, `ImpactNodeCancel`, `WithdrawImpact(s)`, `ImpactLinkUpdate`, `ImpactNodeTransition` with its children states, CEL guard, guard and action algorithms (`graph-script`: QuickJS and CEL); the property, node and link validators of a domain at the acceptance and the commit; `RunAlgorithm`; the reads of a change: `ListDecisionPoints`, `GetBlackboard`, `GetChangeView`, `GetChangeGraph`; the commit of nodes following a lifecycle, the committable states as its guardian; `ProposeImpact` (reviews refused, superseded kept, as Go) and the planned creations of ADR 0121; the revert of the nodes withdrawn after their commit; the decision points; the flows and options, the work on an option or a flow branch, its adoption and the reads of a change on a flow; the review objects of ADR 0080 / 0105 and `ImpactNodeReview`; the refusals of `TransitionChange` and `UpgradeChange` with no registry; the merge and split of nodes of ADR 0077 with the origins gate of their successors; `MoveChange` and `SetChangeMethodology` over the projects of the organisation read at the head of main; the sub-changes of ADR 0103, created under a parent, committed and merged into it; the rebase of ADR 0101 / 0102, its conflicts, `ImpactNodeResolve` and `GetRebaseState`; `AttachChange` and `DetachChange` of ADR 0106; the three-way merge of branches, `PlanMerge` and `MergeBranch`; the changes of the platform's namespaces, a person's too, with the validators of the organisation (the rights of a person on the projects included) and of the platform at the commit; the owner of a node at its creation and moved; the state edits of `CommitEdits`) and `GetChange` with the check of its drafts, at a position of its log too, a person's first call creating its user as Go's `EnsureCaller` does, a person's reads of changes (the personal ones its subject's, the others those who work on their project, as Go's `PersonalScope` decides, the prompts of `ListChangeLog` judged by the ABAC rules of the organisation, run in Rust as Go's Casbin and govaluate run them, a rule `authz.Validate` refuses left out, checked against Go's own answers by `casbin_cases.gen.json`), the reach of a person's reads of the graph (the nodes of the projects it works on, those of the open namespaces whole, ADR 0161 §5), a person's writes on a change (the gates of Go's handler asked of the access snapshot: the creations, the access nodes, the reviews of others; the roles the methodology of the change allows each operation, read from the methodology's nodes in the graph, ADR 0103; the permission each lifecycle transition declares, also in a person's `CommitEdits`, whose every operation asks the roles of the change's methodology; a platform service's commit carrying its own events, raised after `change.applied`; the check of a code text, ADR 0152; the links written to a logical URL, a node of this graph or the URL kept, ADR 0154; a creation taking over a key living only on other lasting branches, ADR 0151; the merge of a change depending on others, ADR 0108; a change following a lifecycle no registry resolves, refused at its transition and merge; a checkout of a node of a project the change's does not hold refused for every caller, ADR 0161 §3), the personal changes of a person (ADR 0037: `@me`, their writes, views and purge; the settings dialog's RPCs, ADR 0122, for any caller in the platform's namespaces too), the views of a change read whole (neighbourhoods, pages, the nodes of a type), the criticality of a change and its risk register (risks, actions, hypotheses, assessments, comments, derogations and verifications, judged by the criticality policy of the organisation), the work around a change (`ReopenChangeImpacts`, `ValidateBoard`, `DiffFlows`, `CompareOptions`, `SplitChange`), the snapshots of ADR 0104 (`ExportGraph`; `ImportGraph`, each of Go's operations a savepoint, a failed step purging the change in the same call; the snapshot decoded with `encoding/json`'s errors, `graph_core::godecode`), the PROV-O export of a change (`ExportChangeProvenance`, ADR 0057), the read queries of ADR 0160 (`QueryView`: the openCypher subset parsed by `crates/cypher`, what the subset leaves out refused in Go's words, what the grammar refuses in the parser's own words at the furthest token read; the match of `graph_core::query` reads, caps and orders as Go's), the router of the two graph spaces (`route.rs`, Go's `graphsvc.Router`: a call naming a namespace or a change of another declared graph forwarded to it, one naming neither tried there when not found here, the namespace listings merged), the relays of its outboxes to NATS (`relay.rs`; a drain finding the relay at work only tells it to look again), the jobs of its exports and imports and their worker (`graph/jobs.rs`, the OKF export written as yaml.v3 writes, `graph_core::okf` / `goyaml`), the import registry (`graph/imports.rs`, Go's `graph/importsvc`), the reads at a position of a change log (`X-Goap-At-Least`, a position not reached refused as Go does), `ListChanges` filtered by the key of a unit or a project, `ListChanges` read whole, `RepublishIndex` (`Tx::republish`), `LoadDemoData` (`graph/demo.rs`, the demo data of `platform/devseed/demo.json` shared with Go), and the smart tags whole. `goap-proxy`, the Connect proxy in front of the
  The platform bootstrap is served too (`goap-graph-rs bootstrap`, `GOAP_GRAPH_BOOTSTRAP=1`, the shipped domains published at
  each start; the nodes the code states read from `boot.gen.json`, written by `platform/devseed` `TestRustBootIsCurrent`). A content graph as well (`GOAP_GRAPH_SPACE=content`: the domains,
  the organisation and the methodologies read from the platform graph, `graph_core::platform`).
  graph services: mode `go`, `rust`, `compare` (Go answers, Rust gets the same call on its own database, ids paired, every call judged
  in `calls.jsonl`; `goap-proxy diff-db` compares the two databases) or `shadow` (Go answers, the reads go to Rust too,
  one database). The performance stack goes through it: `make perf` (go), `make perf-rust` (`perf.rust.yml`, rust),
  `make perf-diff` (`PERF_DIFF_MODE` shadow or compare; `PERF_DIFF_RUST=go` compares Go with Go); `make perf-compare`
  leaves it out (see *Performance stack*). It is a binary
  of its own, which no crate of the service depends on, in an image of its own: `goap/proxy:dev` (`--target proxy`),
  the service's being `goap/rust:dev` (`--target rust`).
- A single multi-target image (`Dockerfile`, `ARG SERVICE`), static binary on `distroless`; its `services` target holds every binary (`/goap/<service>`), the one image `goap/goap:dev` the compose stack runs with an entrypoint per service (`make images`, done by `make up`), so it compiles once.
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

### 3.6b Events and presence for the web

One Connect stream, `EventService/Watch` ([ADR 0053](adr/0053-one-event-stream-and-presence.md)), carries every
platform fact (processes, changes, nodes, baselines, registry, jobs, model calls, connectors, preferences, conversations)
as a thin ordered event with resume by `(epoch, seq)`; the web reduces them into invalidation signals and never refetches
after a write. The events of a person (a personal change, a job, a model call attributed to them, preferences, a
conversation) reach that person only; credentials events never reach the web. A stream reads the hub's history from its
own cursor (`Hub.after`), so a burst (a git import writes thousands of nodes) never cuts it; only a client that stays
more than `History` events behind is told `resync`, with the seq it goes on from. The head graph of a namespace (`headGraph`,
`web/src/lib/graphEdit.ts`), which the organisation and platform views all read, is read once and shared until an event
moves the namespace or a baseline, or the client writes to the graph (`ownWrites`); a baseline event carries its
namespace, so a fast-forward of main moves it too. `Heartbeat` / `Leave` and the
`presence.*` events say who looks at which tab. `platform/cmd/events` follows `goap.>` on NATS (presence is relayed between replicas on `presence.>`).

### 3.7 Observability (OpenTelemetry)

`lib/telemetry` component ([ADR 0008](adr/0008-opentelemetry-observability.md)), enabled by the
standard `OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_*` variables:

- **traces** of all calls: HTTP (Echo, gateway), Connect (client and server, W3C context propagated),
  PostgreSQL (pgx), NATS (headers), sandboxes (the runner continues the action's trace);
- **one process = one trace**: root span `process <agent>` (the `traceparent` is kept in the
  process, background executions continue it), an `action <name>` span per action;
- **LLM calls** (`lib/telemetry/genai.go`, wired on `Router.Instrument` / `InstrumentEmbed` by `platform/cmd/modelgw`):
  span `chat <model>` per completion and `embeddings <model>` per embedding call (GenAI semantic conventions:
  `gen_ai.system` and its successor `gen_ai.provider.name`, `gen_ai.request.model`, `gen_ai.output.type`,
  `gen_ai.usage.input_tokens` / `output_tokens` / `cache_read.input_tokens` / `cache_creation.input_tokens`,
  `gen_ai.response.finish_reasons`, `gen_ai.conversation.id`, `error.type`: the HTTP status, `timeout`, `canceled`,
  `refusal` or `_OTHER`, `llm.ErrorType`), plus `goap.llm.source` / `goap.change.id` (the caller's `llm.CallMeta`),
  `goap.llm.json` / `cache` / `messages` / `behaviors`; one span per call, its retries being `gen_ai.retry` events
  (`goap.llm.attempt`, `error.type`, `goap.llm.retry_wait_ms`; `llm.WithRetryObserver`). Attributed to the process, the
  agent and the action via **baggage** propagated from the engine to the model gateway. Metrics
  `gen_ai.client.token.usage`, `gen_ai.client.operation.duration`, `goap.llm.cache_tokens` (`goap.cache.op` read / write)
  and `goap.llm.retries`, by provider, model, source, agent and action. The prompt and the answer
  (`gen_ai.system_instructions`, `gen_ai.input.messages`, `gen_ai.output.messages`, 16 KiB each) are recorded only when
  `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true`: the change log and the ledger keep them otherwise;
- **tools**: span `execute_tool <name>` (`gen_ai.tool.name`, `gen_ai.tool.type`, `goap.mcp`, `error.type`, the Connect
  code of an RPC error), metrics `goap.tool.calls` and `goap.tool.duration` by tool, MCP, agent and error; events on the
  `action` span (`engine.EventTracer`): `llm.repair` (an unreadable answer given back), `tool.refused` (a tool past
  `MaxToolFailures`) and `llm.answer` (`goap.llm.tool_rounds`, `goap.llm.repairs`, `goap.tool_calls`, `goap.items`);
- **assistant**: span `invoke_agent assistant` per turn (`gen_ai.agent.name`, `gen_ai.conversation.id`, `enduser.id`,
  `goap.project`, `goap.assistant.rounds` / `actions` / `proposals`, `goap.tool_calls`), with the gateway's `chat` spans
  of its rounds and an `execute_tool <name>` span per tool (`gen_ai.tool.type` `extension` for a screen tool);
- metrics `goap.actions`, `goap.action.duration`, `goap.tokens` (by agent / action / result);
- **timing logs** (`GOAP_LOG_LEVEL=debug`): every unary Connect call logs `rpc` with `procedure`, `duration_ms`, `code`
  (warn from `telemetry.SlowRPC`, 1 s); the assistant logs `assistant phase` lines (`send`, `send.available`,
  `send.history`, `turn`, `turn.model` per round with `system_bytes` / `history_bytes`, `turn.tools`, `turn.tool`):
  the model call is the whole of a turn but a few milliseconds.

- **Go runtime** of every service (`go.opentelemetry.io/contrib/instrumentation/runtime`: `go.memory.*`,
  `go.goroutine.count`, GC); metrics are exported every 15 s, or every `OTEL_METRIC_EXPORT_INTERVAL` ms when set;
- **profiles**: `GOAP_PPROF_ADDR` serves `net/http/pprof` on a listener of its own (`lib/platform`, any service), never on the
  service's port.

**Performance stack** (`make perf`, `.\make.ps1 perf`, `deploy/compose/perf.yml`, project `goap-perf`): the graph service alone
on PostgreSQL (with the domain registry, `domains`), `goap-bench` (`platform/cmd/goap-bench`, `platform/bench`), the
collector (`deploy/otel/collector-perf.yaml`: metrics only, the services sample no trace), Prometheus every 5 s
(`deploy/prometheus/prometheus-perf.yml`, with `postgres-exporter`) and Grafana (http://localhost:3300, dashboard
`deploy/grafana/dashboards/goap-perf.json`, provisioned in both stacks). `goap-bench` writes only through the API, as a client
would (identity headers of `system:bench`, admin; or `-token` through the gateway), on the two axes: the **domain** (it publishes
a generated domain `bench`, `bench@T000...` extending `bench@Item`, link `bench@relates`, through `ImportDomain`, a new version
at each run and every `-domain-every` versions written) and the **change** (`-workers` writers on their own nodes, each change
of `-edits` nodes, `-create` of them created with `-links` links (2 by default), the others modified, `-add-links` of the
modifications adding a link to another node (a node links to another once; a node holding `-max-links` links, 1000 by default, drops one as it gains one, `0` letting the links of the hot nodes grow without bound), each change drawn
within `-spread` (0.5 by default) of these four values, their means staying the values set, `-hot` nodes taking
`-hot-ratio` of the modifications for deep histories; `-mode commit`: one `CommitEdits`, `-mode lifecycle`: `CreateChange`,
`ImpactNodeCreate`, one `ImpactNodeCheckoutBatch` of the modified nodes (`-node-checkout`: one `ImpactNodeCheckout` each), `ImpactNodeUpdate` + `ImpactLinkCreate` per node, one `ImpactNodeReviewBatch` of the change by `system:bench-reviewer`, `CommitChange`, `MergeChange`), and every
`-probe` it measures the reads (`GetBranch`, `GetNode`, `ListNodeVersions` of a hot node, `ListNodeChanges`,
`GetNodeNeighbourhood`, `ListBaselineNodes` by type and by text, `DiffBaselines`, `ListChanges`). Its writers start once the graph knows the types of the domain it published (a graph reads the
registry on its events, else after a while, ADR 0167: the bench creates a node of the newest type in a change it purges,
until the graph accepts it, for three minutes at most, unmeasured). It records `bench.op.duration`
(by `op`, `outcome` ok / conflict / error), `bench.response.size` and the gauges `bench.versions`, `nodes`, `links`,
`changes`, `domain.versions`, `domain.types`, `errors` (Prometheus `goap_bench_*`), and a CSV per run (`.goap/perf/<run>.csv`:
one row per operation and window, the totals at its end). Runs add to the same graph (`make perf-run`, keys prefixed by the
run) until `make perf-reset`. Once its run ends `goap-bench` holds (`-hold`: its series stay reported, so the dashboard keeps
drawing them) until interrupted, or for `PERF_AUTOSTOP` (`make perf PERF_AUTOSTOP=30m`; `0`: it exits with the run).
`make perf-capture` saves what Prometheus holds (a TSDB snapshot through its admin API, with the time range it covers) in
`.goap/perf/captures/<PERF_CAPTURE>`, and `make perf-analysis` replays one (the latest by default) in a stack of its own
(`deploy/compose/perf-analysis.yml`, project `goap-perf-analysis`: Prometheus on the capture, scraping nothing, and Grafana on
http://localhost:3301, its link set to the capture's range; `make perf-analysis-down`), so a run stays readable once its
stack is down or reset. `make perf-compare` (`.\make.ps1 perf-compare`, `deploy/compose/perf.compare.yml` over `perf.yml`
and `perf.rust.yml`) measures the Go and the Rust graph services side by side: each on its schema of the same PostgreSQL
(`graph`, `graph_rs`) with its own domain registry (`domains`, `domains-rs` on `domains_rs`: each bench publishes its
versions of the bench domain; the four schemas dropped first unless `PERF_COMPARE_KEEP=1`), each loaded by its own `goap-bench` (`bench`,
`bench-rs`) with the flags `PERF_COMPARE_ARGS` (default `-seed 1` then `PERF_ARGS`, the flags of `make perf`: the same
workload for both), each through its own `goap-proxy` (`proxy` in mode go, `proxy-rs` in mode rust, :8385), at the same
time (they share the machine) or one after the other (`PERF_COMPARE_SERIAL=1`: Go, then Rust); the benches tag their
series with the resource attribute `goap.perf.impl` (`go` / `rust`, Prometheus label `goap_perf_impl`), each proxy times
the service behind it (`goap_proxy_upstream_duration_seconds`, a histogram per service, method and `side`), and cAdvisor
(`cadvisor`, profile `cadvisor` of `perf.yml`, started by `make perf-compare`) gives the CPU, memory, network and threads
of each container; drawn by the dashboard *GOAP performance: Go vs Rust* (`deploy/grafana/dashboards/goap-perf-compare.json`, http://localhost:3300/d/goap-perf-compare:
throughput, the server's latency, CPU and memory per service, p95 per call over the range and its ratio Rust / Go as
the client and as the server see them, each service's calls over time, the size and scans of each schema); reports in
`.goap/perf/compare/go` and `rust`. The Rust service exports no metrics of its own yet: the comparison is what the
client, the proxies, cAdvisor and PostgreSQL see. The web looks at the data of the bench (http://localhost:5174): a `gateway` (:8381, `GOAP_AUTH_MODE=none`, the
fixed development administrator) routing to the graph alone, and the `web` dev server; what needs another service answers
an error.

The graph service measures what the states of its baselines cost (ADR 0143, `graph/graph/statemetrics.go`): every read
of a state `graph.state.reads` (by `scope` whole / nodes and `source`: `cache` or `commits`, the selection),
`graph.state.chain` (the branches of the chain of forks selected on), `graph.state.entries` (the versions selected) and
`graph.state.duration`, drawn by the row *Baseline states* of the dashboard.

What a process keeps between calls is one kind of cache ([ADR 0156](adr/0156-one-cache.md), `lib/cache`), bounded by
the estimated bytes of its entries and by a TTL counted from the last read, the least recently read evicted first. The
graph keeps its change views, version links, baseline headers and impact validations in such caches
(`Graph.Caches`); the registry and its client keep compiled methodologies there, the scripts engine its compiled
scripts, and the gateway the state of its sessions.

The gateway also exposes `GET /api/status` (availability and latency of each service), displayed in the
IDE's status bar along with the user's running runs and their notifications.

In the IDE the **Changes** explorer is the single entry point for every modification: a change is the
blackboard, and the executions (agent processes, sub-agents nested) that work on it are listed under it.
Executions without a change appear in a "No change" group. The waiting / clarifying badge sits on Changes.

Counters are also **kept in the process** (tokens, LLM and tool calls per step and overall: the engine's own totals
and budgets) with a link to the Jaeger trace (`traceId`). The per-call view of the tokens of the whole platform is the
**ledger of the model gateway** (`llm_call`, ADR 0089): the engine, the assistant, the helper and the indexer are all in it.
The web's Tokens console, Token usage pane and the header's consumption of the day read only that ledger, for the calls
attributed to the person (their own and those made on their behalf, ADR 0114; `ListUsage` followed by cursor, `UsageSummary`;
`stores/usage.svelte.ts`, `tokenStats.ts`): the prompt link is offered for every row whose exchange can be read: an engine call of a change
reads it in the change log (`model.call`, ADR 0059), every other call from the gateway (`GetCallExchange`, table
`llm_call_exchange`, `LLMCall.has_exchange`; `GOAP_LLM_CALL_PROMPTS`, default on; own calls and administrators only; purged
with the row). `Process.usage` stays the engine's own total shown on a run.

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
           operations the connector exposes. An `AdapterDef` node of the `platform` namespace (`lib/adapter`,
           changed through a change; not a domain algorithm), for one MCP and one connector, with typed parameters. Each
           organisational unit holds an INSTANCE (an `Adapter` node of the `organisation` namespace, owned by
           the unit: the owner of its version, ADR 0054): the name of the definition and the parameter values.
           Where everything converges.
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
  `goap-graph` (read / glob / grep / links / baselines), `goap-change` (create / read / write / edit / link / unlink / cancel / remove /
  note / validate on a change, options / option / activate / evaluate / compare for its options (compare: `CompareOptions`), decisions /
  decision / rule / answer for its decision points, reviews / review_open / review_update / review_submit / review_discard for its review objects (ADR 0080); no apply, no selection, no ratification), `goap-scheduler` (start / list / get processes, triggers / fire; scope
  `agent`) and
  `goap-admin` (units, users, MCPs, connectors, domains, methodologies). Their connectors
  (`mcp/connectors/builtin`) run in the hub and act for the caller (per-type read authorization, the access gate
  on `User` / `Policy`, the engine's process authorization); the call context (`mcp.CallContext`: unit, change,
  process) lets `goap-change` work on the change of the calling process by default. Pass-through adapter
  definitions and instances on `ORG-DEFAULT` give them to every unit (`graphsvc.SeedBuiltins`, which keeps the
  definitions in sync with the code at every start).
- **Workspace of a change** ([ADR 0118](adr/0118-workspace-of-a-change.md)): the MCP `workspace` (`open`, `status`,
  `close`, `read`, `write`, `edit`, `ls`, `glob`, `grep`, `bash`, `bash_output`, `bash_kill`, `diff`, `push`) is the
  tool box of a coding agent, one workspace per change found from the call context (`InvokeRequest.change_id` /
  `process_id`, put back in the context by `connectorkit`). Defined on every platform, held by no unit by default;
  the local connector `workspace-local` (`mcp/connectors/workspace`, `platform/cmd/connector-workspace`) keeps a
  directory per change, files confined by `os.Root`, commands in a cleaned environment with a timeout but not
  isolated; an administrator gives it to a unit with an adapter. An llm action coding in
  it states the budget of its tool loop (`loop: {maxRounds, maxTokens, timeout, maxToolResultBytes, compactBytes}`):
  once spent it is asked for its final answer instead of failing, and its conversation is compacted as it grows.
  Without a model (`engine/engine/condense.go`): a tool result is tidied before the model sees it (terminal escapes,
  carriage-return redraws, trailing blanks dropped, identical lines in a row past two counted), and a result the
  conversation holds again later is replaced by a short note (the same call made again: superseded; the same output:
  identical; results of 512 bytes or more, errors never), before the older results are elided past `compactBytes`.
  `sdlc` 0.8.0 builds the components naming a repository from their sources (method `workspace_build`: workspace
  opened, each component implemented by an agent, built by its command, sent back on a failed build, pushed and
  recorded); two scoped actions of one scope chain in a plan (`chainScoped`).
  The connector `workspace-docker` (`workspace.NewDocker`) keeps the files and git on its host and runs the commands
  of each workspace in a container of its own (the unit's `image`, no network by default, hardened, limits).
- **Claude coding agent** ([ADR 0123](adr/0123-claude-coding-agent.md)): the MCP `claude-coding-agent` (`run`,
  `answer`, `status`, `cancel`; `lib/mcpbuiltin/claudeagent.go`) hands a coding task to a Claude Code session run by
  the Claude Agent SDK in the workspace of the change (`<root>/<change>`, `workspace.DirName`), held by no unit by
  default like `workspace`. Its connector (`mcp/connectors/claudeagent`,
  `platform/cmd/connector-claude-coding-agent`) starts a Node runner per turn (`runner/runner.mjs`, embedded, written into
  `GOAP_CLAUDE_AGENT_DIR` where `make claude-agent-runner` installs the pinned SDK): one JSON request in, JSON lines
  out. A question (`AskUserQuestion`) or a tool the permissions do not settle ends the turn (`needs_input` /
  `needs_approval`, through `canUseTool`); `answer` resumes the session (`resume`) with the person's decision. The
  environment is cleaned (the unit's `api_key`, `baseURL`, `env`, `passEnv`), the repository's settings load
  (`settingSources: [project]`), one session per change at a time. The model calls of a session go through the
  gateway (ADR 0123 §6): the connector declares `ConnectorInfo.calls_models`, so the hub (`mcpsvc.Service.Models`,
  `ModelEndpoint`) gives each of its calls the gateway's Anthropic endpoint and a token of the call
  (`InvokeRequest.model_url` / `model_token`, `mcp.CallContext.ModelURL` / `ModelToken`; `lib/modeltoken`, HMAC with
  `GOAP_MODEL_PROXY_SECRET`, shared by the hub and the gateway: subject `system:connector:<id>` acting for the caller's
  end user, the unit, the project, the change, the process, source `connector:<id>`), which the session takes as
  `ANTHROPIC_BASE_URL` / `ANTHROPIC_API_KEY` over the adapter's. `modelgw.AnthropicProxy` (`/anthropic/v1/messages`,
  `count_tokens` passed on with no row; `platform/cmd/modelgw` with the secret, the hub
  with `GOAP_MODEL_PROXY_URL` too) checks the token, resolves the model to a catalog model of an `anthropic`-protocol
  provider (alias, `provider/model` or name), admits it (enabled, the roles of the end user, the quota), passes the
  request as it is with the provider's key (streaming included; no global behaviour added) and writes one `llm_call` row
  per call with the tokens read from the answer (input with the cached ones) and the exchange. Without the secret the
  sessions call the model with the adapter's key, outside the ledger.
- **Adding a connector is adding a service.** A connector implements `connector.v1.ConnectorService`
  (`Describe`, `Invoke`); `mcp/connectorkit` does the rest: `connectorkit.Run("name", connector)` serves the
  protocol and registers the connector with the hub (`RegisterConnector`, renewed as a heartbeat within a
  lease, 30 s by default; the hub lists it live or expired). No hub configuration is needed; only
  `GOAP_MCP_URL`, `GOAP_CONNECTOR_URL` (its address as the hub reaches it) and, when set on both sides,
  the shared `GOAP_CONNECTOR_TOKEN`. `mcp/connectors/localfs` is the reference implementation.
- **The hub** (`platform/cmd/mcp`, `mcp/mcpsvc`) keeps only the registry of connectors (table `connector`, PostgreSQL and
  SQLite). MCPs and adapter definitions (`platform` namespace) and adapter instances and the unit hierarchy
  (`organisation` namespace) are read from the graph — a snapshot of each namespace's own head of `main`, combined,
  rebuilt when either moves. It checks the arguments against
  the tool's schema, runs the adapter, resolves the secret parameters, passes the connector only the secrets it
  declares, and returns the result. `CheckAdapter` validates an instance before it is saved (unknown MCP, definition
  missing or of another MCP, missing or unknown parameter: error; unregistered connector, secret the connector needs: warnings);
  `AdapterTemplate` generates the code skeleton.
- **Editing.** MCPs, adapter definitions and adapter instances are nodes, created, changed and removed through changes like any
  node. The first start bootstraps `ORG-DEFAULT` and `PROJ-ROOT` and seeds the policies, the built-in MCPs and the models
  (`graphsvc.Boot`, see "Startup sequence" below, ADR 0054, 0071). `document-repository` and the
  `localfs-document-repository` adapter definition are development data (`devseed.DocumentRepository`), not a platform default.
- **Scope** ([ADR 0028](adr/0028-builtin-mcps-and-connectors.md)). An MCP says where a methodology may use it:
  `action` (declared by actions), `agent` (declared by agents only, reached by their llm actions: orchestration such as
  `goap-scheduler`), or `both` (default). An action never gets an agent-scoped MCP (not planned, not callable), an
  agent never an action-scoped one; the registry reports both at validation.
- **Scheduling.** An action declares the MCPs it uses (`mcps:` on `llm` and `script` actions; a `tool` action is
  `<mcp>/<tool>`). It is available to the planner only when the unit holding the change resolves an adapter for each
  of them; otherwise it is left out (and a specialization needing one is skipped). An action can call the tools of
  the MCPs it declares and, for `llm` / `script` actions, those declared by its agent (`agents[].mcps`).
- **LLM actions** with MCPs may call the tools in a bounded loop (`MaxToolRounds`): the tools are given to the model
  as tools of the call (`llm.Request.Tools`, ADR 0098; the gateway passes them natively or through its text protocol),
  the calls come back in `llm.Response.ToolCalls` and their results go back as `llm.ToolResult`s, then the model
  answers `{"items":[...]}`; a `{"tool_calls":[...]}` written in the text is still honoured. Every call is journaled
  with its duration and error. Calls run with the principal of the process initiator (`tool:call` permission). The calls of one answer run
  at the same time, and their results go back to the model marked as data, not instructions. A failed call goes back
  with guidance to correct it (`failure n of MaxToolFailures`). After `MaxToolFailures` failures the tool is refused in
  that action without being called, and a tool the action cannot use is refused with the list of those it can. An
  answer that cannot be read (not the JSON asked for, or cut by the output limit, `llm.Response.Truncated`) goes back
  with the reason, up to `MaxRepairs` times, before the action fails. A tool loop asks for the prompt cache
  (`llm.Request.Cache`). The hub checks the arguments of a call against the tool's input schema (`mcp.Tool.CheckArgs`:
  required, type and enum of each top-level property, unknown ones when `additionalProperties: false`).
- **Web**: *Connectors* (registry, read-only), *MCPs* (platform nodes), *Adapters* (adapter definitions, with a template generated from an MCP and a connector) and *Organisation* (units; the *MCP* pane of
  a unit lists the MCPs it can use, own or inherited, built in or not, with their allowed tools, attaches or overrides
  them with an instance of an adapter definition and its parameter values, and restricts them). A new organisation
  is a new unit.
- The hub (`platform/cmd/mcp`) runs the built-in connectors in process; the others (localfs, workspace...) are
  services of their own that register over HTTP.

### 3.9b Project ([ADR 0039](adr/0039-project-organisation.md))

Organisation says who may act; Project says what the organisation is working on. `organisation@ProjectUnit`
mirrors `OrgUnit`'s hierarchy (`project_part_of`, child → parent) in the same `organisation` namespace, and
names the methodologies that apply to it, so it inherits their declared roles (ADR 0035 §2) without
redeclaring them. The root project `PROJ-ROOT` (the `root` of the project structure tag, ADR 0069) is bootstrapped self-linked
(`project_part_of` to itself) rather than rootless like `ORG-DEFAULT`; every chain walk already guards
against revisiting a node, so the self-link terminates safely.

`organisation@Assignment` is the meeting point of organisation and project — the same shape as `Adapter`:
it links an `OrgUnit` (`assigns_org`) to a `ProjectUnit` (`assigns_project`) and carries the `roles` that
unit locally holds there. `organisation@User` is a subtype of `OrgUnit` (`extends`), so a person satisfies
`assigns_org` the same way a unit or a team does, without folding `User` into the `part_of` unit tree
(`member_of` still names a user's home unit).

A change acts in a project (`domain.Change.ProjectID`), named when `CreateChange` stores it (ADR 0054, 0091): a
sub-change inherits its parent's project when unset and must stay within it when set (mirroring the `OwnerOrg`
rules of ADR 0016); **a change cannot be created without one** (`graph.ErrInvalid`: there is no default project and
no `default` flag on `ProjectUnit` any more). The edges resolve the caller's active project, an empty claim meaning
the root project: the RPC handlers, the engine (`resolveChange`: the request's project, else the run's, else the root of
`Structures`), `goap-change/create` (`project`, default the caller's active project), the assistant, the seeds; the
web's new-change form requires a project (default: the active one). The nodes a change creates are created in its
project at commit.

**Moving a change** (ADR 0091). `Graph.MoveChange(id, project)` (RPC `MoveChange`, `goap-change/move`, web "Move to
project…" on the change overview) changes the project of a *root* change, `draft` or `active`, and of its open
sub-changes, in one transaction; a sub-change cannot move alone, a `committed`, `applied` or `abandoned` change stays,
the target must be another existing project. `Graph.ProjectMoveGate` (plugged by `platform/cmd/graph` with
`graphsvc.ProjectMoveGate(authorizer, directory)`, asked outside the transaction) asks `change:move` on the project
of each change and on the target, the caller's access to both (`Snapshot.MayAccessProject`) and that the methodology of
each change of the family applies (own or inherited, `Snapshot.ApplicableMethodologies`) to **both** projects; a change
with no methodology is only held to the first rules. The nodes keep their projects (the project of a node never
changes; a draft stores none), the nodes the change creates, before or after the move, take the new one when they
commit. Each moved change logs a `change.updated` entry with a `projectId` `{from, to}` field, by the caller (the audit
trail reads "project: A → B"); the header write publishes the usual `change.updated` event. A process of the change
follows it: `Engine.observe` reads the project of the change at every cycle, so no move is refused for a running
process (roles are checked on the project the change is in now).

**Role resolution.** `pkg/access.Snapshot` resolves `ProjectChain` (mirrors `Chain`) and `ProjectRoles`
(unions the `roles` of every `Assignment` whose org is in a subject's org chain and whose project is in the
change's project chain). `access.Authorizer.Authorize` merges them into the subject's roles when
`authz.Resource.ProjectID` is set, riding the existing `hasRoleIn` step policy unscoped (the membership
test already happened) — no new Casbin function. `engine/engine` threads a project through `Process.Project`
(mirroring `Process.Org`) into `stepAllowed`'s `Resource.ProjectID`.

**Identity.** `organisation@User` is created automatically, not by an administrator's hand: the same key
(`USR:<subject>`) that ADR 0037 uses for a subject's personal unit, since `User extends OrgUnit` lets one
node serve both. `internal/graphsvc.EnsureUser` creates it on first sight, called by `resolveOwner`'s `@me`
path and by the `EnsureCaller` Connect interceptor on every authenticated call (deduplicated per process).
The active project rides the JWT as a `project` claim (`X-Goap-Project` header, `POST
/auth/dev-token/project` reissues it with the same subject/org/roles) the same way `org` and `roles`
already do; it is empty until the person or the assistant picks a project, the root project being picked by its key
([ADR 0135](adr/0135-working-context-project-and-change.md)); `graphsvc.CreateChange` and `enginesvc.StartProcess`/`AttachChange` default their project from
it when a request names none.

**Web.** One left-nav entry, "People & Organisation", sub-navigated into Organisation / Projects / Users
(not three separate entries); a project selector next to the brand mark reissues the token
(`stores/project.svelte.ts`), followed by the working change selector (`stores/workingChange.svelte.ts`, ADR 0135: the
change the person works in, chosen there, by *Work in this change* on the change tab or by the assistant, kept per
project in the preferences, carried to the assistant as `app.change`), then the option selector (`shell/OptionSelector.svelte`:
the option of the working change worked on, *Main flow* by default; picking one makes it the change's active option, so the
screens, the assistant and the engine's calls naming no flow agree; `working.option`, the change tab's scope follows it
for the working change, both ways). Together they are the **context view** every screen reads (`web/src/lib/contextView.ts`):
for the namespace the working change acts on, the change on that option; for the others, and with no working change, the
head of main. A screen reads of it only what it shows (a neighbourhood, a page of a search; `viewHoods`, `searchView`):
the node tab, the pickers, the *New object* dialog, the human tasks (the nodes of their change, `lifecycle.changeNodes`,
the rest searched) and the Graph explorer (a neighbourhood at a time, from the organisation or a node found, or what a
read query found: a subset of openCypher, `OPTIONAL MATCH` and `EXISTS` included, `GraphService.QueryView`, anchored
patterns grown a hop at a time through the neighbourhoods of the view, capped, ADR 0160, `lib/cypher` generated from the
openCypher grammar; the Graph pane of a node keeps its compact view with a filter and a query starting from the node, `NodeQuery.svelte`; both query boxes complete the types, links and attributes of the registry, `cypherComplete.ts`);
the
Baseline explorer is the exception, it shows the commit the person picks. Every such read names its view to the graph (ADR 0157: `graphView`
of the context view, `branchView` for a baseline of a branch, `forkView` for a change's fork): the graph refuses a read of
the current graph naming no branch and no change, and assumes no branch; a search of a change view is the server's, the
nodes it creates and its drafts included. Modes (ADR 0158): a switch in the header, before the context, picks what the
person does (`shell/mode.svelte.ts`, `ModeSwitch.svelte`): **project** (the project, its open changes and their tasks),
**change** (the working change; enabled only while one is selected), and for administrators **navigation** (the graph
and its history) and **administration** (domains, methodologies, projects, organisation); each mode shows its own left
tools (`TOOL_MODES`, a tool not listed shows in every mode), a tool asked for from elsewhere takes the person to a mode
showing it, and keeps its workspace: the tool selected, the view filling the editor space and the editors open are put
aside when the person switches mode and shown again as they were left on coming back (`switchTo`, `swapTabs`). The left
bar selects the tool: a navigation tool shows its navigation beside the editors (a second click hides it, Ctrl+B), a
tool flagged `fill` (`PanelView.fill`, `layout.full`) builds its view in the editor space instead and opens no
navigation, until a tab is opened. The **Tasks** tool is one, a dashboard of areas (`views/tasks/TasksDashboard.svelte`,
`stores/tasks.svelte.ts`, `web/src/lib/tasks.ts`; a page and project mode open on it), that
reads the asks of the open changes in one call (engine `ListAsks`: the open changes of a project, or of every project
the caller reads, at most the hundred latest, each ask naming its change; `Engine.Asks`) and lists the person's tasks
across changes (held by them, or addressed to them alone and held by nobody), the tasks available to them (they may
answer them, nobody holds them, they did not turn them down), the tasks of the working change, of the changes open in an
editor and of the project. Who holds a human task (an input or an approval) is the engine's, seen by
everyone (`HumanTask.Assignee` / `AssignedBy` / `AssignedAt` / `Declined`, `engine/engine/assign.go`, journal kind
`task`): `AcceptTask` (one who may answer it, nobody holding it), `RejectTask` (the holder gives it back, one it is
offered to declines it), `AssignTask` (permission `task:assign` on the project of the run: by the default rules the
administrators and owners of that project or of a project above it, `isProjectAdmin`, and the platform administrators;
to a person who may answer it, empty to take it back), `ListTaskCandidates`; while a task is held only its holder submits
or approves it, besides whoever may reassign it (`Engine.holds`); an ask carries `assignee`, `declined`, `mayAccept`,
`mayAssign`. Each task has its screen (`views/editors/TaskTab.svelte`, tab `task`: accept, turn down, answer, give to
someone); working on a task makes its change the working change,
enters change mode and opens the change on it (the change tab's `task` param unfolds its asks on it). The choice of project is a claim of the token, reissued by the gateway (`AuthMode` `none` has none to reissue: the choice stays local). Assignment has no nav entry of its own: an
"Assignments" pane on the Organisation, Project and User editors (one `AssignmentsPane.svelte`,
parametrized by which side is fixed) offers a "+ Assignment" action, and each explorer's context menu
offers "New assignment", opening the entity's tab with that pane pre-opened.

**Not implemented**: OrgUnit-side action restriction on a project (mirroring `adapter.Restriction`'s shape,
ADR 0028) — an Assignment grants roles but cannot yet narrow which actions a unit may run locally.

### 3.9b2 Structures, bootstrap and the guard of the graph ([ADR 0054](adr/0054-structures-bootstrap-and-the-graph-guard.md))

The registry, the graph service and the engine keep their responsibilities apart: the registry says which node types
build the **organisation** and the **projects** (a `structure` tag on a node type: kind, parent link type, root key;
one type per kind across the domains in force, the built-in organisation domain tags `OrgUnit` and `ProjectUnit`); the
graph service creates their roots and enforces, for every write, that it is made by a change, owned by a unit and
created in a project; the engine only executes, through changes.

- **Bootstrap** (`Graph.Bootstrap`): `ORG-DEFAULT` and `PROJ-ROOT` in one transaction, by one applied change held by
  the root unit and acting in the root project; the services run it at start, before any seed; `CreateChange` runs it
  too. Idempotent.
- **Guard** (`graph/graph/guard.go`, wrapped around every repository transaction by `graph.New`): a node version, link,
  branch membership or node origin names an existing change; a version has an owner unit and its node a project, nodes
  of their structures (stamped from the version it follows, else from its change; the project never changes, the owner
  moves through `ImpactNodeUpdate`'s owner, ADR 0076); a version is immutable once written (ADR 0079: the working state of a node in a
  change is a draft, the version is written at commit with its links in one transaction, and only a version written in
  the transaction gets links); a change names its unit and its project. Checked at the end of the transaction,
  like deferred foreign keys.
- **Ownership** is the owner of the version: `SplitByOwner` and the adapters of a unit read it (there is no `owner`
  link).
- **Readers ask the graph.** `lib/access` and `mcp/mcpsvc` never name the organisation or project types: they ask
  the graph service for its structures (`GetStructures`, `domain.Structures`: a list, each structure with its kind, type, parent, root, default flag and
  subtypes) and read units, projects, parents and roots from the answer.

#### Startup sequence ([ADR 0071](adr/0071-one-platform-bootstrap.md))

The composition of the Go graph (`platform/cmd/graph`) seeds the platform through one function, `graphsvc.Boot(ctx, g, Options)`:

1. The caller sets every hook of the graph first (`Authorizer`, `ChangeAuthorizer`, `Validators`, `DecisionPolicy`, `Types`;
   `SubChangeValidator`, `Lifecycles` (the guardian of the changes) where the registry runs in process: none since ADR 0164) and has a type
   catalogue holding the domains (`platform/cmd/graph`: `loadTypes`, the domains of the registry at
   `GOAP_DOMAINS_URL`, ADR 0167). `Options.RequireHooks` makes `Boot` panic if the access hooks are missing; without the registry hooks `Boot`
   logs one warning, "change lifecycle and activity gating are not available in this composition".
2. `Boot`: `Graph.Bootstrap` (roots), `SeedAccess` (default policies), `SeedBuiltins` (built-in MCPs, adapter definitions,
   platform roles), `SeedModels` (`Options.Models`), `SeedProtectedAliases` (the `assistant` and `helper` aliases, ADR 0084), `SeedBehaviors` (the built-in `terse` LLM behaviour, disabled, ADR 0093), `SeedSources` (the source `platform`, fixed by `PlatformSourceValidator`, and `Options.Sources`, ADR 0154), then `Options.Dev`. Each step is idempotent.
3. The registry then seeds the methodologies (`platform/cmd/registry` seeds them over RPC): they need the aliases of step 2.

Development and demo data is not part of it (`platform/devseed`: `Demo` the ALM repository, `DocumentRepository` the
document-repository MCP and its localfs adapter definition, `LocalFS` a directory as the default organisation's repository).
`platform/cmd/graph` passes the document repository as `Options.Dev`
with `GOAP_GRAPH_SEED=dev` (the local stack's default), the demo too with `GOAP_GRAPH_SEED=demo`. Otherwise an administrator loads the demo on demand:
*Settings → Demo data → Load demo data* (RPC `GraphService.LoadDemoData`, `graphsvc.Handler.Demo` = `devseed.DemoLoader`,
administrators only, idempotent). The demo is the organisation `ORG-ACME` and its teams, the project `PROJ-DEMO` (under
`PROJ-ROOT`, methodology `sdlc`) bound to `ORG-ACME` by an assignment holding the roles of `sdlc`, and the ALM repository,
written in `PROJ-DEMO`. `platform/cmd/graph` boots through its command `graph bootstrap` (run once, then it exits;
compose's `graph-bootstrap` service before `graph`), the service itself only with `GOAP_GRAPH_BOOTSTRAP=1` or a memory
store; a content graph boots nothing (ADR 0154 §9). Platform services act as `authz.System(name, roles...)`
(`system:registry`, `system:graph`, `system:trigger:<key>`).

### 3.9c User bootstrap, mandatory parenting, local auth ([ADR 0040](adr/0040-user-bootstrap-org-membership-local-auth.md))

`EnsureUser` resolves `ORG-DEFAULT` *before* creating anything (the services now bootstrap the graph before serving,
ADR 0054, so the root exists; the check stays as a guard): creating the `User` node first would
leave a permanently broken one on that race (no `member_of`, no admin) — worse, one that makes every later
subject see the namespace as "already has a `User`" and never get the first-admin bootstrap either. Once the
org is confirmed, it links the new `User` `member_of` it and grants `Roles: ["admin"]` when it is the first
`User` node the namespace has ever held; the existence check (`g.ViewNodesOfType`, the head of main of the organisation, ADR 0157) reads the state
its last change left, not an older baseline — `EnsureUser` writes by import (`ImpactNodeCreate`/`Link`, outside of
any change), which does not itself advance a baseline, so a baseline-scoped read would miss every user but
the ones incidentally folded in by some unrelated later change. A benign race between two simultaneous first
connections can only ever grant admin to more than one subject, never to none. `pkg/graph.Commit` (the
single choke point behind `CommitEdits`, seeding and the registry) now refuses to create an
`OrgUnit`/`ProjectUnit` with anything but exactly one `part_of`/`project_part_of` link, and a `User` with
anything but exactly one `member_of` (declared on the node type, `requires:`, ADR 0065) — except `ORG-DEFAULT`/`PROJ-ROOT` themselves (`checkRequiredParent`,
`graph/graph/subchange.go`, checked against the edit's own declared links before anything is written). A
second check, `checkRequiredLinks`, re-reads a *modified* node's actual live links right after the write
commits: a client can build its edit from a `headGraph`-style baseline snapshot that predates an import-written
link (again, `EnsureUser`'s own `member_of`), so its "current link to remove" can come back empty even though
one already exists — without this the "move" action (`graphEdit.moveNodeItem`, adds the new link and removes
the one it saw, in one edit) could silently leave a node with two links instead of failing loudly. "Move to…"
controls on the Organisation and User tabs build this edit from the graph's own outgoing links
(`graphEdit.currentLink`).

**Local auth.** A third gateway `AuthMode`, `"local"` (alongside `"none"`/`"hs256"`), verifies bearer tokens
exactly as `"hs256"` does; the difference is only how a token is first obtained. `POST /auth/register` and
`POST /auth/login` (`gateway/gateway`) call a new `gateway/credsvc` service (shaped like `platform/prefssvc`,
ADR 0038: its own store, its own migrations, both dialects) that keeps one bcrypt password hash per subject,
outside the graph — a failed login never touches the audited graph/change log, and no secret material ever
becomes graph data. `credsvc.Store` is a small interface so the concrete backend can move from its own SQL
table to being `platform.Secrets`/Vault-backed later with no call-site change. `POST /auth/logout` has
nothing to revoke server-side (HS256 is stateless) — it exists so a real revocation list is a change to one
function, later. `GET /api/auth/config` (unauthenticated) tells the web which sign-in UI to show: the web's
`Signin.svelte` replaces the shell when `AuthMode` is `"local"` and there is no token yet; `UserTab.svelte`'s
"Log out" toolbar action shows only on a caller's own profile and hides itself once a real SSO `AuthMode`
(OIDC/OAuth, not yet implemented) exists — a forward seam only.

**Single sources of truth** ([ADR 0068](adr/0068-server-side-single-sources-of-truth.md)). A node type of a domain
flagged `adminOnly: true` (inherited by subtypes) is written by platform administrators only: `Graph.AdminOnlyType` is
the one question the graph handler and `goap-change` ask (the connector through `engine.GraphPort`, the RPC
`IsAdminOnlyType` for a remote graph). The walk up the organisation or the projects is one resolver,
`domain.Hierarchy`, built from the structures and used by the access snapshot, the hub's adapter resolution and the
`goap-admin` listings. Authentication modes are `gateway.Mode` strategies (`none`, `hs256`, `local`; registered by
name, `Sessions()` says whether sessions are checked server-side); `/api/auth/config` answers `{authMode, signsIn}`.
The preferences service keeps an opaque JSON object per user (size-limited): the web store owns the keys.

**Sign-in by default, users declared at sign-in, the waiting unit** ([ADR 0042](adr/0042-local-sign-in-by-default-and-waiting-unit.md)).
`"local"` is the default `AuthMode` (`gateway.DefaultAuthMode`) of the gateway, the local stack and the compose
file, unless an external identity provider issues the tokens (`hs256` today); `"none"` must be asked for.
`goap-local` generates the JWT secret when none is configured (kept in `.goap/local/secrets`).
`gateway.Config.OnSignIn` declares the user before a register/login returns a token (`graphsvc.EnsureUser`
in process, `graphclient.Client.DeclareUser` from the gateway) and refreshes the `access.Directory`, so the first
calls carry the user's unit and roles. `EnsureUser` commits the `User` node and its `member_of` as one change
on `main` (journaled, moves the head the access snapshot reads — the import write described above no longer
happens), serialized per process. New users join the waiting unit, an `OrgUnit` an administrator created and
flagged `waiting` at their discretion (`access.PropWaitingUnit`, `graphsvc.NewUserUnit`; none flagged by
default: `ORG-DEFAULT`; several flagged: smallest key); administrators set it when creating a unit or from the
Organisation tab ("Make waiting unit" moves the flag in one change, "Clear" removes it). The header's user menu offers "My
profile" and, with local sign-in, "Log out"; the token box shows in `hs256` mode only.

**Keeping a session** ([ADR 0044](adr/0044-session-refresh-and-expiry.md)). `POST /auth/refresh` reissues a valid
token with a fresh expiry (`GOAP_TOKEN_TTL`, 12h), keeping its identity and sign-in time (`auth_time`), up to
`GOAP_SESSION_MAX` (7 days) after the sign-in; a 401 says `token expired` or `invalid token`. The web
(`stores/auth.svelte.ts`) refreshes a quarter of the lifetime before expiry, re-checks when the tab comes back and
follows other tabs; an expired token or a 401 for the current token (`api.reportUnauthorized`) ends the session and
the sign-in page says why, with the last subject filled in.

**Server-side sessions** ([ADR 0045](adr/0045-server-side-sessions.md)). A sign-in opens a session in the
credentials service (`auth_session`, `credentials.v1` `StartSession` / `CheckSession` / `EndSession` /
`EndSessions`); every token carries its id (`sid`). The gateway refuses the tokens of an ended session (401
`session ended`), trusting a checked state for `SessionCheckTTL` (15 s; a refresh checks afresh; 503 when the
service cannot answer an unchecked session). `POST /auth/logout` ends the session (`{"everywhere": true}`: all the
subject's sessions), a new password ends them all; the web offers "Log out on every device".

**Not implemented**: OIDC/OAuth itself (only the `AuthMode` seam is prepared).

### 3.8 Voice input ([ADR 0022](adr/0022-voice-interaction.md))

The assistant's input box accepts push-to-talk voice input. Speech-to-text runs **entirely in the browser**
(Whisper `tiny` / `base` through transformers.js in a Web Worker, WebGPU with WASM fallback;
French and English). Only the resulting text reaches the backend, as a typed message of the conversation
(`assistant.v1.Send`, ADR 0087), so voice adds no server-side load, API or scaling
concern. Audio is never uploaded or persisted. Code: `web/src/lib/voice/`. Server-side
transcription (stateless unary RPC) and live conversation (ephemeral token to a realtime
provider, or gateway WebSocket) are analyzed in the ADR but not built.

### 3.9 The assistant in the web ([ADR 0088](adr/0088-assistant-web-interface.md))

One conversation view (`views/assistant/Assistant.svelte`) is shown as the **assistant tab** and in the **Assistant
bar** on the right of the shell (`shell/AssistantBar.svelte`: closed, a rail with its pin, the assistant and the latest
discussions; a click opens it over the editor, pinned it is docked beside it, resized by its left edge either way; open,
it lists the discussions above the conversation; `Ctrl+Shift+A`, the command "Ask the assistant"). The bar holds only the
assistant and its discussions: the sections of an editor are the tabs of the editor (`components/EditorPanes.svelte`:
header, tab strip with the editor's tools, detail). The round floating launcher (`assistant/Launcher.svelte`) is no longer
mounted; its design is kept for another use. The state is `stores/assistant.svelte.ts`: conversations of the caller (newest first), the
current one and its messages, the draft; `send` collects the layered context (`assistant/context.ts`, ADR 0092: focus, screen, app, zoomed on the focused element, capped
as the server caps it; never a form value) and the tools the open screen offers (`assist/registry.svelte.ts` `describeTools`,
descriptors in `assist/catalog.ts`) and calls `assistant.v1.Send`, then polls `GetConversation` (every 0.5 s for the first 12
polls, then every 1.5 s, `pollDelay`; backing off on errors, not while the page is hidden, only while a view is shown) until the answer is `done` or `error`.
The actions of an answer (`select_project`, `open_change`, `create_change`, and the `effect` screen tools: `assistant/actions.ts`)
run once, only for a message seen pending in this page, and a screen tool reports its outcome (`ReportAction`); a reloaded
conversation replays nothing; each shows as a chip that repeats it. A proposal (`start_agent`, a `write` screen tool) is a card
(`views/assistant/ProposalCard.svelte`, decisions in `stores/proposals.svelte.ts`): Accept / Reject through `ConfirmAction`, an
accepted write runs through the screen's registered tool and is reported, an accepted write never reported shows "accepted, not
applied" with a Retry while the tool is registered, a started agent shows the status of its run read from the process.
**Screens register themselves** (`assist/registry.svelte.ts`): per tab and for as long as a view is mounted, a screen and a
focus provider, tools (implementations of the catalog, run through the screen's own edit path, never when not registered; the change tab registers `transition_change` when its lifecycle offers a transition, `changeTransition.ts` being shared with the Move button, and `commit_change` / `merge_change` when `getCommitState` allows them (ADR 0102, 0103: the summary says what can be committed and merged) and `withdraw_impacts` (several at once, all or none; an impact entity that modifies nothing carries `modifies: no`, never accepted); the Import tab registers `run_import` (ADR 0104: a definition or a graph snapshot, the screen describing the content loaded, never the content itself, `assist/importScreen.ts`)) and
fields (`use:assistField`, shared with the helper of ADR 0086); `assist/capture.ts` keeps the selection and focus from before
the person went to the assistant, `assist/recorder.ts` the last action. The
current conversation is kept in memory only (ADR 0052): a new page opens the most recent one. The whole interface depends
on `aliasFlags.assistantEnabled`: without a model for the `assistant` alias the bar lists no discussion, the commands
are hidden and the bar and the tab explain it with a link to the model catalog. The bar gives way to the field helper
(they are never open together).

## 4. Format of a methodology

Methodologies are **graph data** ([ADR 0023](adr/0023-definitions-in-the-graph.md)), edited from the frontend: a
version is a node `methodology@MethodologyVersion` (key `MV:<name>@<version>`, namespace `methodology`: scalar fields,
status) and one node per element typed by the built-in meta-domain (`methodology@Agent`, `@Action`, `@Condition`,
`@Goal`, `@Process`, `@Method`, `@Role`, one `@Step` / `@MethodStep` per step), keyed `<version key>/<kind>/<name>` and tied by `methodology@defines`. The references between elements are links
([ADR 0120](adr/0120-methodology-references-are-links.md)): a condition is the output of the activities that
`produces` it and the input of those that `requires` it (`uses`, `pursues`, `performed_by`, `nests`, `assigns`,
`aggregates`, `specializes`, `main_goal`; derived `mentions`, `carried_out_by`), a name the version does not declare (a
library's or a generated condition, another methodology's element) stays a property, the steps of a process are nodes
reached through `sub_activity` only; no element stands alone (a modelling principle, never checked by the graph). Every save, publication or deletion is
a change applied on main; editing an action versions that node only (`registrysvc.GraphStore`, through a graph
client). **Domains** are the definition of a graph (namespace, node types, link types, lifecycles, algorithms): each
graph holds them in memory (the type catalogue) to keep its data coherent, and the registry keeps their versions in its
database, the **domain registry**, a service of its own (`domain_version`, `domain_protection`, PostgreSQL or SQLite,
`domainsvc.SQLStore`, ADR 0125 §3, ADR 0167). The methodology registry validates, compiles, publishes, suspends the versions the domains in force
break, and emits the `goap.registry.methodology.*` events. Compilation is one pipeline with a policy
(`Methodology.CompileWith(CompileOptions{Lenient, Stored})`; `Compile`, `CompileLenient`, `Validate`, `ValidateStored` are `def.Issue.Severity` (`error` default, `warning`; ADR 0097): only errors refuse a compile, a validation or a publish (`Issues.HasErrors`), and `CompileWith` adds the `Compiled.Hints()` remarks as `goal` warnings, shown by the editor without blocking Save or Publish.
its policies, [ADR 0072](adr/0072-one-compile-pipeline-split-apply-compile-cache.md)); the registry caches the compiled
methodologies by (name, version, content hash, published domains in force), so the engine's per-event and per-action reads
do not recompile.

Lifecycle of a version: **draft** (editable, can be invalid: anomalies are returned
with their path, e.g. `conditions[2].expr`) → **published** (validated, immutable, the only one executable by the
engine) → **archived**. Modifying a published version means creating a new draft version (`CreateVersion`).

**Versions in use** ([ADR 0113](adr/0113-methodology-and-domain-versions.md)). A change follows a version of its
methodology (`Change.MethodologyVersion`, column `change.methodology_version`): `CreateChange` takes the latest
published one (`ChangeLifecycles.MethodologyVersion`), a sub-change of the same methodology its parent's, and the change
keeps it whatever is published later; `Graph.UpgradeChange` (operation `change.upgrade`, RPC `UpgradeChange`,
`goap-change/upgrade`) moves an open change to another version, keeping its lifecycle and state. A change created with
no methodology takes one with `Graph.SetChangeMethodology` (operation `change.methodology`, RPC `SetChangeMethodology`,
the change tab's methodology field), as a creation would have given it; `Graph.MethodologyGate`
(`graphsvc.MethodologyGate`) checks it applies to the change's project
([ADR 0135](adr/0135-working-context-project-and-change.md)). Every reader of the
methodology of a change takes the reference `<name>@<version>` (`domain.MethodologyRef`, `Change.MethodologyRef()`; a
bare name is the latest): the registry's lifecycle, goal, guards, gates, guardians, operation roles, the history fold,
and the engine, whose process runs the version it started with (`Process.MethodologyVersion`, `Engine.methodologyOf`).
A methodology version pins the domain versions it is written against (`Methodology.Domains`, `domains:` by domain
name): publishing pins every published domain in force, the registry validates and compiles a version against its
pins (`domainsFor`; a version published before pins existed follows the domains in force when it was published), a
new compatible version of a domain does not reach the published methodologies pinned on another one (one they no
longer compile against suspends them, ADR 0125 §3), and `CreateVersion` drops
the pins so a new draft moves to the latest domains. The graph keeps one type catalogue, the latest published
domains: its data follows the domain in force. Each start publishes the shipped domain and methodology files whose
version is not stored yet (`SeedDomains`, `Seed`): a new shipped version needs no reset of the database.

**Domains** ([ADR 0013](adr/0013-domains.md)). The object part of the model (node types, link types, lifecycles,
algorithms) lives in **domains**, one per namespace (`alm`, `organisation`, `platform`), versioned on their own (same
draft → published → archived lifecycle; `registry.v1.DomainService`, served by the graph, ABAC resource `domain`, administrators).
A methodology is the active part only (agents, actions, conditions, goals, processes): it names its **target namespace**
(`namespace: alm`, the domain its changes act on; it replaces `domainRef`) and the types it works on with qualified
references (`alm@Requirement`, [ADR 0012](adr/0012-node-types.md)), which may point to other domains for what it reads
or links to. Consistency is
checked at save / publish against the domain versions in force: action `expects`, CEL type literals
(`"alm@Requirement" in n.types`, `l.type == "alm@verifies"`) and builtin `params.linkTypes` must resolve. A methodology
can only be published on published domains; a published methodology compiles against the domain versions it pins
(ADR 0113), and a domain version is never refused for a methodology: one the domains in force break is suspended by
the methodology registry (ADR 0125 §3; `GetDomainUsage` lists the methodologies using a domain).

**YAML** is only an **import / export** format (`ImportMethodology`, `ExportMethodology`, `ImportDomain`, ...); the
files in `domains/` are imported and published by the domain registry at its start (`GOAP_DOMAINS_DIR`, after the
built-in domains, ADR 0167), those of
`methodologies/` by the methodology registry (`GOAP_METHODOLOGIES_DIR`), when they don't already exist (a changed file
needs a new version number).
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
    expr: artifacts.exists(a, a.target == "propagation")
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

**Processes** ([ADR 0034](adr/0034-processes-and-steps.md)) describe the route to the objective of a change as steps
and sub-steps, each done by one method, with the precision the methodology has for it:

```yaml
processes:
  - name: software_delivery                  # also the agent that runs it and its goal
    steps:
      - name: framing
        steps:
          - name: review                     # a review (ADR 0130): starts by itself on the events of its subject
            method: description_rating
            review: {subject: description, automatic: true}
          - name: confirmation               # by hand: a human task, done once submitted
            pre: {description_rated: true}
            instructions: Check the intent with the requester
            references: [{title: Delivery guide, ref: "document-repository:sdlc/guide.md", section: Framing}]
      - name: analysis                       # sub-steps; the phase's pre applies to them
        pre: {"step:software_delivery/framing/confirmation": true}
        steps:
          - {name: scope, actions: [identify_requirements, select_scope]}   # alternatives the planner chooses among
          - {name: impacts, method: impact_analysis}   # a capability, carried out by a method
      - {name: design, agent: architect, pre: {requirements_specified: true}}   # an agent plans it
      - {name: release, process: release_train}   # a nested process (or <methodology>/<process>)
```

Steps are sequenced by their conditions, not by their position. A process compiles to planner inputs: an agent and a
goal of its name, and one action per step (named by its path, `software_delivery/analysis/scope`) whose preconditions
are the step's entry — the `pre` of the steps containing it, its own, and those of what it runs (its action's `pre`,
a nested process's prerequisites) — and whose effects are its exit criteria (`done`, default: the action's effects,
the agent's goal, the sub-steps' criteria, the nested process's criteria, or a `step:<path>` condition,
`history.last["<path>"].current`, once it has run and while its run stands, which other steps can name; ADR 0109: the
engine records the run of a step that ended without error, `journal.step.ended`, before it observes the change again). `references` link a process or a step to its
reference documents (`doc:<key>`, `<mcp>:<path>`, URL). An agent or nested
process step is an action of kind `call` (ADR 0126): a sub-agent on the same change, so processes nest. The methodology is the description, `methodology.Compiled` the activable form, and the engine runs only the latter (ADR 0126 step 2): it reads its accessors (`Name`, `MainGoal`, `AppliesTo`, `Processes`, the agents, goals, actions, steps, reactions and conditions), never `Compiled.Definition()`, and turns its actions and goals into planner operators itself (`engine/engine/planners.go`; the compiler imports no planner). What is shown of the activities is compiled too (ADR 0126 step 3): `methodology.View` (`Compiled.View()`, RPC `registry.v1.GetMethodologyView`) holds the processes, the methods, their step trees with entry and exit conditions and what each condition means, producers and prerequisites included, and no value of a change; the starting points, the current state of a change and the progress of a run bind it to the change (conditions evaluated, runs laid over, method chosen, rights). The flow of a process (`ProcessGraph`) is a projection of the view, and the change screen draws the view itself: `GetChangeState` answers only where each step stands (by path), the web lays it over the view of the version the change follows, as a list or as the methodology's flow with the state of each step (display *Detailed* or *Simplified*). The starting points are a binding too (ADR 0126 step 5): `ListStartingPoints` says which steps are possible now, how to start them and whether the caller may, and their readers (the `goap-scheduler/starting_points` tool, the assistant, the change tab) read what each step is in the view of the version it names, through one pure composer (`lib/startingpoint`, `composePoints` in the web). A stored version is activated one way everywhere (ADR 0126 step 6): `registrysvc.Activate` over its definition and the domain versions it compiles against, which `GetMethodology` gives a remote client with `with_domains`. A step's `guidance`,
`checklist`, `deliverables` and `references` reach whoever carries it out: its human tasks carry the step
(`HumanTask.context`), its LLM actions get it in their system prompt (and `{{ .Step }}`), and the sub-agent of an agent
or process step inherits it. A step can also name a **capability** (`method: design`): the methodology's **methods** ([ADR 0035](adr/0035-methods-roles-operations-documents.md))
are the documentary references of how a capability is carried out in a context (`for`, `when`, `priority`, guidance,
references, deliverables) and each names the **agent** that acts; the call of the step runs the agent of the applicable
method with the highest priority, with the method's guidance in its step context.
The methodology editor draws a process as a graph (`GetProcessGraph` on the registry, from the draft as edited): the
steps that run something, laid out in layers from left to right by the edges their conditions draw (a step whose exit
criteria meet another's entry, implied edges left out), phases as colours. The **graph explorer**
(command "Explore the graph") lays out the head of main of every namespace with a force simulation (d3-force), to
navigate the organisation as it is (units, users, adapters, what they own) and the data of the domains.
**Everything is an activity.** Process, Step, Method, MethodStep and Action share one shape (inputs, goals, outputs) and are composed with the
`sub_activity` link; the `specializes` link defines **variants** (the methods that specialize a step's capability, one chosen by context), which
cuts the traceability by adding variability, so each variant has a graph of its own. An **action** is the smallest activity, not broken down further:
it has no flow, it is a node (a step is made of sub-steps, an action, alternative actions, variants, a nested process, or is done by hand).
An **agent** is not a way to do a step: it is the actor, created for the step ([ADR 0050](adr/0050-methods-own-actions-roles-drive-agents.md)). A method
owns the actions that realize it (a pool, and/or its steps, which inherit the pool) and its planner, model and MCPs; when a step names a capability
the scheduler picks the applicable method (highest priority, then the most specific context) and runs an agent instance acting as the method's
responsible role, planning over the method's actions towards its goal. The instance starts fresh and reads what it needs from the change (brief,
trace); what it produces goes on the change. A method with no steps states what it reaches (`done`; the flow then shows its actions).
A step cannot name an agent for a capability: it names the capability, and the scheduler resolves the method.
The flow shows **one level at a time** (the system of interest is a parent and its direct steps, from `CheckLevels`: each level carries A leaf step done by an action or alternative actions opens on them (kind `action`): the actions are its internal steps, with their own entry and exit; a method of actions alone opens on its pool. A step with `foreach` carries a ∀ marker (and `groupBy`), and the run's progress draws one lane per stream.
its steps, their entry and exit and the links their conditions draw). A step with sub-steps is a node; zooming into it (double-click, or
the breadcrumb to come back) changes the system of interest. A step not resolved inside (one of its own levels, or one below, is broken)
is flagged `incomplete` in the level above (gap `inner`), which is then broken too: the process cannot run through it until it is reworked.
The process tab shows it as a **Flow** (`ProcessFlowView`, the only graph view, xyflow + dagre, as in the gems project): a node per step with its entry conditions
and exit criteria as ports, links between the criterion and the condition it meets, a "Prerequisites" node for what nothing establishes;
clicking a condition traces it (the links carrying it are drawn on, the rest is dimmed). Right-clicking opens a context menu of
introspection (`web/src/lib/flowInspect.ts`, pure, rendered by `FlowInspector.svelte` in a panel inside the canvas so it works in full
screen): on a condition (a port or a link) its CEL, where it comes from at the level, the steps that set and need it and the actions
of the agents; on a step how it runs, its RACI, its entry and exit with their CEL (each, and joined as one expression), its foreach,
the methods providing its capability with their `when`, its actions in the plan, its gaps and rules broken, plus open, zoom and show
alone; on a boundary block the CEL of its conditions; on the background the level (agent, goal, inputs, outputs, order, gaps).
A method composing its own steps has the same view and graph (`GetProcessGraph` accepts its name) and the same level check, apart from the
process: a step naming a capability is a boundary node that only lists the methods specializing it (each opens its own graph), because
a specialization adds variability and cuts the traceability.
The editor also checks a process, or a method composing its own steps, **level by level** (`CheckLevels` on the registry,
`Compiled.CheckLevels`, from the draft as edited): for the root and each step with sub-steps, the entry conditions and exit
criteria of the direct steps must chain the parent's inputs to its outputs. A step that cannot be entered whatever the order
of its siblings (a missing input, a cycle) or an output no step reaches breaks the level; what a process or method needs and
none of its steps establishes is only reported (a prerequisite). The scheduler still orders the steps from the actual state;
the check only proves that an order exists. It is not listed as text: the flow draws it like gems draws a missing variable (an "Unresolved inputs"
node linked in orange to the entry conditions a step can never get, a ⚠ on the port, an "Unreached outputs" node for the outputs no step reaches). A step opens in its own editor tab (`StepTab`), where its inputs and outputs
are conditions of the methodology, created on the spot as static (a document of a type exists) or dynamic (a CEL expression).
`GetProcessProgress` computes where a run stands in its process (each step done, skipped,
active, waiting for someone, ready, to do with what it still needs, or blocked), shown on the run and the change.

`ExplainCondition` is the solver behind a dynamic condition of the run's world state: it evaluates the formula against the blackboard of the run's change keeping the value of every sub-expression (`condition.Set.Explain`, CEL state tracking) and returns the tree of parts plus the blackboard variables the formula reads; the "My processes" run view opens it in a modal (`ConditionExplainDialog.svelte`) when a dynamic condition is clicked.

See `methodologies/examples/impact-analysis.yaml` for the full executable example, and
`methodologies/methodology-improvement.yaml` (self-observation: abstract action specialized by rules
or an LLM). Action specialization fields: `specializes`, `when`, `priority`, `kind: abstract`;
node type subtyping: `extends`. An `incremental: true` action reaches its effects across several
executions: an execution that produces items without reaching them is **progress**, not a failure.

### 4.1 SDLC methodology on the ALM domain (`methodologies/sdlc.yaml`, version 0.5.6, on the shared `alm` domain of `domains/alm.yaml`)

ALM domain (demo data: `platform/devseed/demo.go`):

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
(goap, the whole cycle). Processes (ADR 0034): `software_delivery` walks the cycle phase by phase — framing by hand,
analysis once framed, in sub-steps (scope and requirements with their human alternatives, impacts, traceability, test
plan), design by the `architect` agent once the requirements are specified, the specialized `build`, the nested
`release_train` (plan, release note, review, deployment waves) entered on its prerequisites, then the application —
all sequenced by their conditions, each process linked to its reference documents. Review covers content; deployments, recorded after it, do not
require it. The permission for a specialization (e.g. `deploy_production`) is checked before its
execution: without it, the process waits for approval from an authorized person. Since the
"each element …" conditions hold on an empty set, the design, build, and delivery actions also require
specified requirements to anchor the cycle. The self-observation agent applies to its own runs just like
to any other methodology.

## 5. Repository organization

```
platform/cmd/<service>/main.go        entry points (gateway, registry, engine, graph, modelgw, indexer, mcp)
platform/cmd/goap-runner/             sandbox for executing script actions
platform/cmd/goap-local/              the compose stack without Docker, make dev (-watch rebuilds and restarts, -web vite; ADR 0164)
lib/platform/           config, logs, HTTP/Connect server, NATS, Postgres, Vault secrets
internal/<service>/          Connect handler implementation for a service (graphsvc, registrysvc…)
lib/identity/           caller identity (headers set by the gateway)
contracts/domain/                  graph model (domain axis + change axis)
graph/graph/                   Store (memory, PostgreSQL, SQLite), apply, branches / merge / rebase, the generic log of a change (`AppendLog` / `ChangeLog`); hooks (`Lifecycles` the guardian of the changes, `SubChangeValidator`, `Facets`) for the use cases it does not know (ADR 0066)
contracts/journal/                 the execution journal: records, prompts and their entries in the log of a change (ADR 0011, 0066); the graph and the domain import nothing of it
engine/observe/                 run cost analysis (journal + traces) and improvement proposals
contracts/builtins/                names of the builtin actions and the static list the compiler and the registry check (ADR 0062); a leaf package
engine/selfimprove/             the self-observation builtins (`observe.analyze`, `observe.propose`, `methodology.draft`), registered by `platform/cmd/engine`; the engine imports no observer
engine/goap/                    A* planner
engine/condition/               CEL compilation/evaluation, `expects` compilation
engine/intent/                  intent loop (lexical Ranker, LLM Ranker)
engine/engine/                  processes, agents and planners, action executors, DSL host, sub-agents, events
lib/dsl/                     script action DSL (ctx API, JavaScript and Go interpreters); the adapter script context too (`RunAdapter`)
contracts/algo/                    generic algorithm model of the domains (usages, typed params, instances, binding); knows no MCP, connector or secret
contracts/mcp/                     MCP definition (tools, scopes, call context); knows no connector, adapter or organisation
lib/adapter/                 the adapter, where organisation, MCP and connector meet: `Def`, `Instance`, `Restriction`, code template, secret params (ADR 0061)
lib/mcpbuiltin/              schemas of the built-in MCPs and their adapters (ADR 0028, 0061)
platform/layering/                test of the dependency rules between these packages
engine/sandbox/            sandbox pool, provisioners (process, docker, kubernetes), RuntimeService, runner
lib/telemetry/          OpenTelemetry: exporters, interceptors, process / action / LLM / tool spans
lib/def/              domain definition model (ADR 0060): Domain, Schema, node / link types, attributes, enums, lifecycles, algorithms; built-in domains, validation, YAML import/export
engine/methodology/             methodology model only (a methodology is a domain-typed definition): validation (localized anomalies), compilation, YAML import/export
contracts/authz/                   ABAC: identity, requests, Casbin model and enforcer, default policies
contracts/llm/                     completion and embedding contracts (implemented by platform/modelgw)
platform/jobsvc/               the job service: long runs claimed by workers, followed over SSE (ADR 0149; kit `lib/jobkit`, command `platform/cmd/jobs`)
platform/gitimport/            git importer into the `code` domain: one change per commit, directories and files as nodes, with the symbols the declarations, their calls and uses, the packages and their imports, the other git branches on branches of the graph (ADR 0147, ADR 0150, ADR 0151; command `platform/cmd/goap-gitimport`, service `platform/cmd/importer-git`, ADR 0148)
graph/importsvc/               import registry: the declared sources are listed, a run checked and created as a job (ADR 0148, 0149)
lib/importerkit/               kit of an importer service: a worker of the job service for the importers it implements (ADR 0148, 0149)
lib/jobkit/                    kit of the long runs: the job queue (in process or over RPC), the worker, the job of a runner (ADR 0149)
graph/index/                   document index (nodes and changes): hybrid search, facets, similar_to, stores (memory, SQLite FTS5, PostgreSQL pgvector)
proto/                       connect-rpc contracts (buf)
rust/                        Cargo workspace (ADR 0163): goap-proto, goap-connect, graph-core, graph-store, graph-service (goap-graph-rs, goap-domains-rs), proxy (goap-proxy)
contracts/gen/                         generated code (committed)
methodologies/               example methodologies (active part)
domains/                     domains: seeded by the domain registry at its start (alm, code), after the built-in ones of lib/domains/builtin, protected (one per namespace)
deploy/                      compose, postgres init, otel collector, prometheus, grafana, k8s (sandboxes)
web/                         Svelte frontend
docs/                        architecture, ADRs
```

## 6. Roadmap

| Step | Content |
|---|---|
| **M0 — foundation** 🟢 | Doc, domain/change model, A\* planner, CEL conditions, intent loop, engine (memory), graph (memory + Postgres), registry, modelgw (fake + Anthropic + OpenAI-compatible), gateway, compose, minimal UI |
| **M1 — engine persistence** ✅ | PostgreSQL / SQLite process store shared by the replicas, locks with fencing, crash recovery, steps never written twice, capacity per replica (ADR 0124) |
| **M2 — IAM** | organizations, users, OIDC, `org_id` isolation in the graph, ABAC on the graph service |
| **M3 — MCP** ✅ | MCP hub, connectors as separate self-registering services, MCPs and adapter instances as graph nodes, adapter definitions as platform nodes, resolved along the organisation hierarchy, `tool` actions and LLM tools, scheduling filter, secrets via Vault · remaining: more connectors, adapter validation as a node type algorithm |
| **M4 — advanced change axis** | impact propagation (recursive CTE parameterized by link types), suspect links, baseline diff, merge/rebase of concurrent changesets |
| **M5 — UX** | ✅ methodology editor (forms, localized anomalies, publishing, versions, YAML import/export), "Access" screen (ABAC policies), approvals · remaining: graph and plan visualization |
| **M6 — K8s** | Helm charts, engine HPA · ✅ OpenTelemetry observability, sandbox manifests |
| **M8 — branches and decisions** 🟡 | ADR 0009 (partially implemented) · ✅ graph: per-branch versions, 3-way branch merge, change branches merged at apply (`committed` + `MergeChange`, formerly `merge_pending`), versions joining branches instead of merge copies, baselines as selections of the versions committed (ADR 0143), written / accepted / committed views of a change, options explored on flows with an active option, compared, evaluated, the chosen one selected (ADR 0032), decision points with their loops (questions investigated by sub-agents, ratification, escalation) and the CEL variables and platform conditions on options and decisions (ADR 0009 §4) · remaining: earlier conflict detection and merge proposals by agents, the change budget; then versioned containers and releases |
| **M9 — self-observation** ✅ | ADR 0011: execution journal on the change axis (ticks, actions, LLM / tool calls, decisions, item provenance), `observer` agent (journal + OpenTelemetry traces → findings → proposals → review → draft), action specialization and type subtyping |
| **M10 — SDLC** 🟡 | `sdlc` 0.4.0 methodology on the `alm` namespace (ALM domain) (need → requirement → function → component → artifact → application → solution, data, interfaces, flows), build specialized by technology, incremental releases and deployment (dev → test → staging → production, release manager approval), incremental actions · to refine: quality (coverage, security), rollback, freezes / change windows, MCP tools (repositories, CI, artifact registry, deployment) |
| **M7 — agents** ✅ | agents (goap / utility / hybrid), JS / Go script actions with DSL, sub-agents, sandbox per process, IDE |
| **M11 — node types** ✅ | ADR 0012 / 0013 / 0023: qualified type references `<namespace>@<NodeType>`, one domain per namespace, registry as the reference of the types, with an in-memory catalogue in the graph and the engine kept in sync by its events and an existence rule, methodologies as nodes of the `methodology` meta-domain, domains in the registry's database; no `NodeType` projection, no `M:` / `D:` elements, no `Def*` types |
| **M12 — request to shipped change** (proposed) | [ADR 0033](adr/0033-from-request-to-shipped-change.md): assistant requests as intake conversations with deferred binding, proposals accepted before a change is created or its intent amended, ship as the default goal, participants, task addressees and a server-side work list, a workspace for requesters and contributors next to the authoring studio |
| **M13 — processes** ✅ | [ADR 0034](adr/0034-processes-and-steps.md): methodologies describe their route as processes of steps and sub-steps, each done by an action, alternative actions, an agent, a nested process or a person, compiled to planner inputs and nested as sub-agents on the same change; IDE process editor · remaining: step addressees and permissions, run progress shown as the process |
| **M14 — methods, roles, operations, documents** 🟡 | [ADR 0035](adr/0035-methods-roles-operations-documents.md): methods as context-specific specialized agents selected per step, guidance and RACI roles feeding access control and task addressees, process progress, continuous operations whose procedures take effect from the deployment of a change, a transverse document domain and documentation methodology, companion changes across namespaces |

## 7. Open questions

1. **Condition granularity**: should conditions be *parameterized* (per node) rather than global?
   Classic GOAP reasons over global booleans; a condition per node would blow up the state space.
   Proposal: quantified global conditions (`all`/`exists`) + actions that iterate internally.
2. **Action expectation**: is `expects` enough to express every expectation, or is a real graph
   pattern language needed (a restricted Cypher-like language)?
3. ~~**Concurrency on a baseline**~~ → [ADR 0009](adr/0009-branches-options-decisions.md): detection on the
   base version per branch, 3-way merge at apply, conflicts resolved by a human (the change stays `committed`, formerly `merge_pending`).
4. **Action cost**: static (declared) or dynamic (estimated tokens, observed latency)?
5. ~~**Human decisions**: mandatory validation or a separate `apply`?~~ → settled by [ADR 0004](adr/0004-change-application.md):
   `apply` is a plannable action conditioned by review and protected by a permission.


## Model gateway administration (platform settings)

Administrators (`admin` on the `platform` resource, Casbin) open **Platform settings** from the gear in the IDE
status bar. The gateway configuration is **graph data** ([ADR 0021](adr/0021-model-configuration-in-the-graph.md)):
`LlmProvider`, `LlmModel`, `LlmAlias` and `LlmBehavior` nodes of the `platform` namespace (`lib/llmcfg`, the built-in `platform` domain), changed
through changes like any node (the screen writes them with `web/src/lib/llmEdit.ts`). The gateway reads a snapshot of the
`platform` namespace's head of `main` and rebuilds its router when it moves. `GOAP_MODELS_CONFIG` / env keys only seed a graph that holds no provider
(`graphsvc.SeedModels`). At every start the gateway (`platform/cmd/modelgw`, in the background once the graph seeded the protected aliases) signs on to the providers whose well-known key variable is set (`modelgw.KnownKeys`: `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` / `OPENAI_KEY`, `MISTRAL_API_KEY`, `GEMINI_API_KEY` / `GOOGLE_API_KEY`; `DetectProviders`, `SignOn`): one change creating a provider named after its kind, on its kind's default address, key `env:<VAR>`, with no model and no alias (the administrator picks the models in the settings); a kind the graph already holds a provider of, in any state, is left alone. The `modelgw` database keeps the quota counters (`llm_usage`, keyed by the key of the model node and period, for the admission) the ledger of calls (`llm_call`) and the exchanges of the calls no change log keeps (`llm_call_exchange`, ADR 0089, below).

- **Providers** are pluggable: a `Protocol` (`anthropic`, `openai`, `gemini`, `fake`; `platform/modelgw/protocols.go`)
  knows how to complete and how to list models; a `Kind` is a preset on a protocol (Anthropic, Mistral AI, Google
  Gemini, OpenAI, custom OpenAI-compatible). Adding a provider type = `RegisterProtocol` / `RegisterKind`.
- **Availability**: an alias is available to a caller when it targets an enabled catalog model of a loaded provider
  (enabled, its key resolved, its address given or its kind's default) that the caller may use (`Service.Available`);
  nothing is probed. `Available` and `Providers` read the head of the graph at each call (`llmcfg.Directory.Fresh`),
  so the web and the assistant see a retargeted alias at once; the completions keep the snapshot looked at once per
  second. The assistant is offered, and `Send` accepts a message, only while the `assistant` alias is available.
- **API keys** are never stored: a provider node holds `apiKeyRef` (`env:<VAR>` or `<vault path>#<field>`, alternatives
  separated by `|`) that the gateway resolves (`platform.Secrets.Resolve`), like the secret parameters of an adapter.
- **Catalog**: `DiscoverModels` asks the provider for its models; the admin adds them to the catalog, with a global
  token quota (per day / month / total, shared by all users) and the roles allowed to call the model (none =
  any signed-in user; `admin` always). Only enabled catalog models can be called; calls without identity
  (engine, in-process) are trusted and skip the role check but still count toward the quota.
- **Provider calls** (`Router.Complete`): a transient failure (408, 409, 429, 5xx, 529, a dropped connection) is retried
  `Router.Retries` times (`DefaultRetries` 3) with exponential backoff and jitter, honouring `Retry-After`
  (`platform/modelgw/retry.go`; the Anthropic SDK retries on its own, `anthropicRetries`). `llm.Request.Cache` (set by
  callers that send the same prefix again: a tool loop, the assistant's rounds) marks the system text and the
  conversation as a cache prefix on Anthropic; OpenAI and Gemini cache on their own. `llm.Usage.InputTokens` is the
  whole prompt, cached or not, with `CacheReadTokens` / `CacheWriteTokens` beside it; `llm.Response.StopReason` is
  normalised (`end`, `max_tokens`, else the provider's word).
- **Tool calling** (ADR 0098, `platform/modelgw/tools.go`): `llm.Request.Tools` reach a provider implementing
  `NativeTools` (Anthropic, OpenAI-compatible, Gemini) through its tool API, under names every API accepts (mapped back);
  any other provider, or an OpenAI-compatible model that refuses tools (remembered, `textToolsOnly`), gets the text
  protocol run by the gateway. The exchange of a call keeps the calls and results as text (`llm.Message.Transcript`).
- **Offline evaluation** (ADR 0099): `engine/eval` runs YAML cases (`evals/`: a prompt, mocked tools, expected items,
  tool trajectory, a judge's rubric) through the tool loop of an engine LLM action, several runs each, and reports pass
  rate, tokens and latency; `platform/cmd/goap-eval` (`make eval`) runs them through the gateway as the user of `GOAP_TOKEN`
  (source `eval` in the ledger).
- **Aliases** (`default`, `fast`…) point to catalog models; the router follows the graph. The RPCs of the gateway are read-only
  (`ListProviders`, `ListCatalog`, `ListBehaviors`, `PreviewBehaviors`, `DiscoverModels`, `ListProviderKinds`, `ListModels`, `Complete`).
- **Ledger of LLM calls** (ADR 0089): every call the gateway serves or refuses (completion or embedding, whoever asked:
  engine, assistant, helper, indexer, intent) is one row of `llm_call`: time, duration, subject / project / org (the
  principal's), alias requested, provider, model, kind, input / output tokens, error, and what the caller declares
  (`llm.CallMeta`: `source`, conversation, process, change, step, call index, action, agent; in the context in process,
  as `CallMeta` on the RPC). No prompt is stored: the exchange of an engine call stays the `model.call` entry of the change
  log (ADR 0059), found by `(change_id, process_id, step, call)`. Read with `ListUsage` (filter, `after_seq` cursor) and
  `UsageSummary` (grouped by model / alias / source / subject / end_user / process / action / agent / day / hour); a caller
  reads the calls attributed to it, an administrator any; retention `GOAP_LLM_CALL_RETENTION_DAYS` (default 183, the six months of the EU AI Act, purge at
  start and daily). Every row names its **end user** ([ADR 0114](adr/0114-end-user-attribution-through-consequences.md),
  `llm_call.end_user`): the subject when a person called, else the person the platform service acted for
  (`authz.Principal.OnBehalfOf`, carried from one consequence to the next: the change event a person raised
  (`ChangeEvent.EndUser`), the trigger or companion run it set off, that run's sub-agents and calls), so a person's
  consumption includes what their actions set off.
  `TestEveryModelCallGoesThroughTheLedger` keeps the paths to a model through `Service`.
- **Global behaviours** (ADR 0093): `platform@LlmBehavior` nodes (`LLB:<name>`, `adminOnly`, lifecycle `config`; `llmcfg.Behavior`) hold an
  instruction the gateway adds to the system text of every completion matching their scope (aliases, models, sources, kinds;
  `prepend` / `append`, `order`, `enabled`, `appliesToJSON`). `Service.Complete` applies them after `admit` and before the provider
  (`llmcfg.Snapshot.Apply`, pure; the caller's request is never changed; embeddings never; 8 KiB cap, extra ones dropped and
  recorded as `!name`); a call with `JSON` true gets only the ones flagged `appliesToJSON`, wrapped so they cannot change the format.
  The ledger row records `behaviors` and an estimated `behavior_tokens` (`llm_call`, migration 0005), the stored exchange holds the
  system text as sent, the engine's `model.call` log entry the text it built plus `behaviors` / `behaviorTokens`
  (`llm.Response.Behaviors`). `ListBehaviors` / `PreviewBehaviors` (admin, like `ListCatalog`); web: settings "LLM behaviours".
  `graphsvc.SeedBehaviors` seeds `terse`, enabled by default.
  **Measured cost** (ADR 0093): `behavior_tokens` is the cost measured on the model when known (two minimal calibration calls per model and
  behaviour through the gateway, ledger source `calibration`, subject `system:modelgw`, no behaviour applied; stored in `llm_behavior_cost` /
  `llm_model_baseline`, migration 0006, keyed by instruction hash + provider + model; lazy, background, deduplicated, backed off on failure;
  measured again after `GOAP_LLM_BEHAVIOR_COST_TTL_DAYS`, default 30), else the four-bytes estimate (`behavior_tokens_estimated`). `ListBehaviors`
  carries `costs`, `MeasureBehaviors` (admin) measures now.
- **Protected aliases** (ADR 0084): `assistant` (the conversational assistant) and `helper` (the contextual field helper)
  are `LlmAlias` nodes flagged `protected`, present on every install (`graphsvc.SeedProtectedAliases`, from `Boot`; no
  model configured: the alias exists with no target, resolves to nothing and is not in `ListModels`, i.e. not available).
  `llmcfg.ProtectedAliasValidator` (a `graph.NodeValidator`) refuses a change that retires one, renames it or drops its
  flag; retargeting stays free. The web hides the removal of a protected alias and its staged deletions of a model or
  provider skip it (`aliasFlags.assistantEnabled` / `helperEnabled` in `stores/modelChoices.svelte.ts`).


## Node lifecycle (ADR 0014)

A domain is composed of **node types, link types and lifecycles**. A lifecycle is a named state machine
(states, each committable unless flagged `notCommittable`, transitions with permission, CEL guard, required attributes/links and,
for documents, allowed child states) that node types name and subtypes inherit. The state is stored on
each node version. A node is edited in any state while it is in a change (ADR 0078); a state flagged
`notCommittable` (a draft) keeps the change from commit, so the change moves the node to a committable state before it is
applied. Changes are attached to the nodes they modify (several changes may be attached to one node;
conflicts appear at Apply). A document type embeds nodes through `contains` links and its transitions
validate the states of its children in the result baseline. The rules live in `graph/graph/lifecycle.go`
(checked when a change impact writes a version and again by `Apply`); the type catalogue resolves the lifecycles
(ADR 0012).
