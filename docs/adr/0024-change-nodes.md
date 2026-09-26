# ADR 0024 — Change nodes: the change is attached to node versions (pre / post)

**Status**: accepted, implemented (see Implementation status) · **Date**: 2026-09 · Refines ADR 0011 (journal), 0014 (attachment), 0015 (pre/post
impacts, shared versions) and 0009 (merge). Supersedes the `impact` and `proposal` item kinds.

## Context
`ChangeSet.Items` mixes two different things:

1. **References to the graph**: `impact` (pre: a released version of the reference baseline) and `proposal`
   (post: a modification that will produce a new version). Pre and post are two items tied by
   `Impact.Post`, and the proposal is an intermediate object that only exists until `Apply` turns it
   into a node version.
2. **Blackboard facts** with no node: `decision`, `artifact`, `merge`, `flow`.

The change is the place where a node version is explained, as in plm-core where the version is attached
to the change itself. Today that explanation is split between an item, its proposal payload and the
`change_node` attachment (ADR 0014). It is also impossible to say "this node is impacted, here is why"
without also having a proposal, and it is impossible to read a node version and know which change
produced it and why it was accepted.

## Decision

### 1. A change holds change nodes and facts, two separate lists
- `ChangeSet.Nodes []ChangeNode`: the node versions the change reads, modifies or creates.
- `ChangeSet.Facts []ChangeItem` (the former `Items`, renamed): `decision` (about a non-node choice),
  `artifact`, `merge`, `flow`. A fact has no `target` / `post` / `proposal`.
- The `impact` and `proposal` kinds disappear. CEL exposes `changeNodes` (with `intent`, `review`,
  `hasPost`, `pre`, `post`, `landed`) instead of `impacts` / `proposals`; `facts` and the kind filters
  (`decisions`, `artifacts`, `merges`) stay.

### 2. `ChangeNode` is the link change → node, and it carries the meaning
```
ChangeNode {
  id        ChangeNodeID
  pre       NodeRef?   // released version of the reference baseline (empty when intent = created)
  key       string     // node key; needed while pre and post are both empty (created, not yet realized)
  type      string     // node type, same reason
  intent    created | modified
  rationale string     // why: what an impact analysis produces; required at creation
  post      NodeRef?   // version created by this change, on the change branch; empty until realized
  landed    NodeRef?   // version on the target branch once the change is applied (see §6)
  review    proposed | accepted | rejected
  reviews   []Review   // history {status, by, comment, at}
  via       ChangeNodeID?  // for a removal: the parent change node that realizes it (§4)
  producedBy, derivedFrom, execution   // provenance, as on items today
}
```
- **An impact is a change node with `pre`, `intent`, `rationale` and no `post`.** No final node has to be
  defined first. Later an action or a human realizes it: the new version is created on the change branch
  (`reason = revise` / `create`, `parents = [pre]`) and `post` is filled in. Until then the change node is
  *planned*; with `post` it is *realized*.
- `intent` is what the analysis declares; the realization must match it: `created` has no `pre`,
  `modified` has one, `post.parents` contains `pre`.
- The pre side keeps the ADR 0015 §8 rule: when the type has a lifecycle, `pre` is a non-editable
  (released) version; the post version is editable and must reach a non-editable state before `Apply`
  (ADR 0014 §3).
- Applies to one namespace, the change's (ADR 0015 §2); `pre` may point to another namespace only as a read
  (cross-namespace impact analysis), with no post.
- A node appears **once** per change (`pre` is unique in `Nodes`); several proposals on the same node
  become successive versions on the branch, the change node's `post` being the last one.

### 3. Accepting requires a comment, and the node version remembers its origin
- `review` moves `proposed → accepted | rejected` only with a **non-empty comment**; the transition is
  appended to `reviews` (who, when, comment). Accepting or rejecting without a comment is refused. The
  former `decision` item about a node is replaced by this; `decision` facts remain for choices that are
  not about a node.
- The node version itself records where it comes from: `Node.ChangeID` (already there), plus
  `Node.ChangeNode` (id of the change node) and `Node.Comment` (the acceptance comment, else the
  rationale). Together with `Parents` and `Reason` (ADR 0009) the state of a node is understandable from
  the node alone: *which change, why, accepted by whom and with what remark, from which version*.
  `Apply` copies the comment onto the version when it lands (§6); a merge version (§5) also lists both
  change nodes it merges.
- `Apply` applies only `accepted` change nodes; a `proposed` one blocks it, a `rejected` one is dropped
  (its post version on the branch is abandoned with the change, or reverted before the merge).

### 4. No node deletion: a removal is a modification of the parent
- A node is not deleted by a change. Removing a child is a `modified` change node on its **parent**: the
  parent's version loses the link to the child (outgoing links belong to the source version, ADR 0003).
  `delete_node`, `remove_link` and `add_link` are not operations any more: a link change is a
  `modified` change node on the source node.
- An impact analysis still has to say "X goes away". It records a `modified` change node on X with the
  rationale and `via` = the parent's change node, the one that is really realized. X keeps its versions.
- A real deletion is a garbage-collection step, outside changes: a node that no version of any node links
  to any more (orphan) can be removed (`Node.Deleted` tombstone) by the platform. It is not user
  intent and never appears as a change node.

### 5. Several changes on the same node version: detection, then a mandatory merge
Every change works on its own branch (ADR 0015 §5, made the rule): its post versions have `pre` as
parent, so **`pre` is the common ancestor** of the 3-way merge with whatever landed meanwhile.

**Detection, as early as possible**
- On `AddNodes`, any other open change holding a change node with the same node (any version) makes both
  sides `shared`. `GetSharedNodes` / `ListNodeChanges` (ADR 0014 §4) report it, and the journal records it.
- When a change lands on a branch, every open change with a change node on the same node whose `pre` is no
  longer the head of the target branch is marked `diverged` (ADR 0009 §2).

**What has to be done, according to the state of the diverged change node**
| State of the change node | What happens |
|---|---|
| *planned* (no `post`), i.e. an impact | No content to merge. `pre` is moved to the new head, the `rationale` is kept and flagged **to re-check** (`recheck = true`, with the version it was written against). The planner replans if needed. |
| *realized*, properties and links disjoint | Auto-merge at `Apply` (3-way per property, links per source version): a `merge` version is created, `parents = [post, head]`, `reason = merge`. |
| *realized*, same property or same link changed | **`merge_pending`**: a `merge` fact is created and attached to the change node (`base = pre`, `theirs` = head, `ours` = post, proposed merge, conflicting keys; computed by `graph.rebase` or proposed by an agent). A human validates it (ABAC `change:merge`, one comment mandatory, as in §3). Apply resumes with the resolution. |
| parent lists (children / links) | Merged as a 3-way set: additions and removals from both sides are kept, an item removed by one and modified by the other is a conflict. This is what makes "a removal is a parent modification" mergeable. |

Rules:
- **A merge is required, never skipped.** A change that is `diverged` cannot be applied until each of its
  diverged change nodes is auto-merged or resolved. The order of application decides who merges: the
  first to apply lands as is, the following ones merge onto it.
- A merge does not delete the earlier work: the `post` of each change stays; the `landed` version (§6) is
  the merge version and its `parents` give the traceability to both.
- Superseded facts follow ADR 0009 §2 (`superseded` marks replaced facts, the change replans what became
  obsolete). A *rebase* (rebuilding `post` on the new head instead of merging) remains possible and is
  the choice of the human or the agent; it is a new realization of the change node, the old `post`
  is kept in `reviews` history as superseded.
- Building a change on the unmerged versions of another one is still not supported (ADR 0015 §6).

### 6. Landing
`Apply`: accepted change nodes → their `post` is merged onto the target branch; `landed` is set (equal to
`post` if it fast-forwards, the merge version otherwise); the result baseline is the merge baseline.
`landed` is what impact analysis of later changes reads as `pre`.

## Consequences
- Graph model: `change_node` is the change node table (`intent`, `rationale`, `pre_version`, `post_version`,
  `landed_version` on one `node_id`, `review`, the `reviews` history as JSON, `via`, `recheck`, `key`,
  `type`). The ADR 0014 attachment table is renamed `change_attachment` and kept until the items that fill
  it are retired (step 2). `node_version` gets `change_node` and `comment`. Both SQL dialects (PostgreSQL `migrations/`, SQLite
  `migrations_sqlite/`), proto `graph.v1` (`ChangeNode`, `Review`; `Item` loses `target`, `post`,
  `proposal`), and `gen/` regenerated.
- Migration of existing data: an `impact` item becomes a change node (intent from its proposal, else
  `modified`); the `post` link or proposal becomes `post`; a `decision` on a proposal becomes a `review`
  entry with the decision text as its comment (empty comments are filled with "migrated"); a
  `delete_node` proposal becomes a `modified` change node on the parent with the removal, or is dropped
  with a warning when there is no parent link.
- Code: `pkg/domain/change.go` (types, `Proposal` and `Op*` removed), `pkg/graph/apply.go` and
  `walk` (realization from change nodes, merge at apply), `pkg/graph/namespace.go`, the engine bindings
  and `pkg/dsl`, the builtins `graph.propagate` / `graph.apply`, the impact analysis actions of
  `methodologies/`, `pkg/observe`, and the web views of a change.
- Docs: `docs/architecture.md` (§2.1 change axis, §2.3 CEL bindings), ADR 0011 (journal items), ADR 0015
  §8 (pre/post: superseded by this ADR), `docs/dsl.md` and its copy in `web/src/lib/help/dsl.md`.
- Sub-changes (ADR 0016) are unchanged: each holds its own change nodes; a parent change reads those of its
  sub-changes through `via`-free references.
- Risk: broad refactor across domain, graph, engine, proto and UI; to be done in steps (types + storage
  with migration, then apply / merge, then engine bindings and methodologies, then UI), each keeping
  `make test` and `make lint` green.

## Open points
- Whether `landed` needs its own column or can be derived (`post` when fast-forward, the merge version
  otherwise); kept explicit here for traceability.
- Retention of a rejected change node's post version: abandoned with the branch (chosen) versus a
  reverting version; only matters when the change is not on its own branch.

## Implementation status
- **Step 1 (done)**: types, storage (both dialects), `AddNodes`, `RealizeNode`, `ReviewNode`, `ListChangeNodes`.
- **Step 2a (done)**: `WriteNode` creates the post versions on the change branch (own branch required);
  `Apply` refuses a change node still `proposed` or accepted without post, drops the rejected ones, checks
  validators, leftover editable states and the transitions (permission, requirements, guards, actions) of
  the post versions, then the existing branch merge lands them (auto-merge, or `merge_pending` and
  `MergeChange`). `landed` and the origin (`changeId`, `changeNode`, `comment`) are set on the landed
  version. Planned change nodes of other open changes on the same node move to the new head with
  `recheck`. `SharedNodes` counts change nodes. A change may still carry `impact` / `proposal` items
  next to change nodes, but not on the same node.
- **Bridge (done)**: the impact / proposal items stay how producers write them; `Graph.Change`,
  `Blackboard` and `ListChangeNodes` return, next to the stored change nodes, the ones derived from the
  items (one per node: `create_node` → `created`; `update_node`, `transition_node`, `delete_node`,
  `add_link`, `remove_link` and impacts → `modified`; rationale from the impact `reason`; review from the
  decisions; `Items` lists the items it stands for; the id is the id of its first item). At `Apply` the
  derived change nodes are stored with `post` (and `landed` on the branch the change applies to; a change
  with its own branch lands when merged), and the produced versions record `changeId`, `changeNode` and
  `comment`. Platform merge changes (`data.merge`) are not bridged. A node is changed either through items
  or through a change node written directly, not both (`ErrConflict`).
- **Read side (done)**: CEL `changeNodes` binding (docs/architecture.md §2.3); `ChangeSet.nodes` and
  `Node.change_node` / `comment` in `graph.v1`; RPCs `AddChangeNodes`, `WriteChangeNode`,
  `ReviewChangeNode` (a comment is mandatory; NodeType, User and Policy nodes keep their `AddItems`
  gates); the change tab lists the change nodes with the review action, and the node history shows the
  comment of each version.
- **Producers, first part (done)**: `Graph.Commit` (RPC `CommitEdits`) makes a change of node edits: it
  opens the change on a branch of its own, declares a change node per edit (rationale mandatory, the title
  by default), writes the versions in link order (what a link points to first; a link to a node created by
  the same commit is given by key), accepts them as the producer and applies. A concurrent move of a node
  gives `ErrConflict` and abandons the change, so the producer reads again and rebuilds. The registry store,
  the metamodel projection (`Sync`, `ApplyNodeTypes`, `LinkToType`, `BackfillInstanceOf`, `CreateObject`)
  and the seeds use it, no longer items.
  - A change whose target branch has not moved since its branch was forked **fast-forwards**: the versions
    of the branch become versions of the target (no merge version), `landed` is `post`. Otherwise the 3-way
    merge of §5 applies.
  - `NodeWrite.Retire` / `NodeEdit.Retire` is the tombstone of a node a projection no longer owns (an
    orphan). It is the platform clean-up of §4, not an intent: the change node stays `modified`.
  - `instanceOf` links may be written from a node of another namespace (ADR 0015 §3).
- **DSL actions (done, next to the item calls)**: script actions get `impactNode`, `createNode`, `writeNode`,
  `reviewNode` and `changeNodes` (docs/dsl.md). The calls are buffered like items (`dsl.Result.Nodes`, the
  sandbox protocol carries them as `nodes_json`) and applied in order by the engine (`applyNodeOps`, through
  `GraphPort.AddNodes` / `WriteNode` / `ReviewNode`, over RPC for the graph service client); the change node
  records the action and the execution that produced it. They need a change with a branch of its own and are
  refused on a flow branch (change nodes do not belong to a flow). `addImpact`, `proposeNode`… keep working:
  the methodologies and their conditions still use them.
- **Removed (done)**: the ADR 0014 attachment table (`change_attachment`, migrations `0012` / `0011`) and its
  repo methods, replaced by the change nodes: `GetChangeNodes` returns their pre versions, `ListNodeChanges`
  and `SharedNodes` read the stored change nodes and the ones derived from the unapplied changes' items
  (`Tx.OpenChangeIDs`); and the `Graph.Impacts` view with the `GetImpacts` RPC (a change node carries pre,
  post and landed).
- **Methodologies (first two)**: `test-design` and `impact-analysis` work on change nodes.
  - `Apply` ignores an accepted change node with intent `modified` and no version written: a **confirmed
    impact** (impact analysis does not modify the repository); a `created` one accepted but not written is
    refused. `WriteNode` opens the branch of the change on its first write, so the engine's changes need no
    `OwnBranch`.
  - Change node operations travel as items of kind `changeNode` (`ItemInput.changeNode`, the `dsl.NodeOp`
    JSON): from LLM output, from human input (the human form declares impacts and reviews change nodes, with a
    mandatory comment), next to the ones a script buffers. `graph.propagate` declares the propagated impacts as
    change nodes when the change has some; prompt templates get `.ChangeNodes` (with `.Pre` / `.Post`); the CEL
    `expects` accept `forEach: changeNodes`; a step and its journal record list the change nodes it declared
    (`Step.Nodes`, `ExecutionRecord.Nodes`), and the observer counts them as outputs.
- **`methodology-improvement`**: an improvement is a change node on an element of the observed methodology
  (`M:<methodology>/<kind>/<name>`, platform namespace): `modified` for an existing element, `created` for a
  specialisation or a tool request; the rationale is "title: why" and the version written on the change branch
  holds the proposed properties (the change is never applied: it is the recommendation). The human review accepts
  or rejects them with a comment, and `methodology.draft` builds the draft from the accepted ones (a patch of the
  properties the version changes).
- **`sdlc`**: conditions, scripts and prompts work on change nodes (the delivery test runs end to end, with the
  final baseline checked). A node created by the change is its working copy: the editable rule of ADR 0014 applies
  to the versions a change starts from, not to a node it creates, so a created node may be written again in its
  initial state. A deployment is created, written and accepted in the same wave (it is recorded once done), so it
  never waits for the review. Change nodes carry the links of the version written (`ChangeNode.links` in the DSL,
  `post.out` in CEL) and a `props` field (the version written, else the one the change starts from).
- **Platform merge (done)**: `MergeBranch` no longer proposes `merge_node` items: it writes the merge versions itself
  (3-way merge of the properties and of the links, editable leftovers refused as before) as a change of the platform
  with one accepted change node per merged node (`Landed` = the merge version, whose `ChangeNode` and `Comment`
  say what was merged, and with which conflicts). `OpMergeNode`, `NodeDraft.from` / `ancestor` and the applier's
  merge branches are gone.
- **Sub-changes (done)**: `SplitByOwner` splits along the change nodes of the parent (ADR 0016 §5).
- **Remaining**: no shipped methodology writes items any more. What still reads or writes the item kinds: the
  item forms of the DSL and of LLM / human input (kept for methodologies outside the repository), the flows of
  items (`OpenFlow` stale closure, `MaterializeFlow`), the bridge (`changenode_bridge.go`), the item validation
  (`ValidateBoard`), and the UI (change tab, flow graph). Then removing the `impact` / `proposal` kinds and
  `Items` → `Facts`. The `merge` fact attached to a change node is not done either.

## Final state
The item kinds are gone. Removed: the `impact` and `proposal` kinds with `Proposal` / `NodeDraft` / `LinkDraft`, the
bridge that derived change nodes from items, `Rebase` / `Divergences` / `MaterializeFlow` (Go, RPC, proto), the DSL
item calls (`addImpact`, `propose*`, `decide`, `impacts()`, `proposals()`), the CEL `impacts` / `proposals`, the graph
namespace and impact checks, and the `instanceOf` link: `Node.Type` is the direct attribute of a node. A change holds
change nodes and facts (`decision`, `artifact`, `merge`, `flow`); every producer (registry, metamodel projection, seeds,
merge, engine, DSL) writes change nodes, through `Graph.Commit` or `AddNodes` / `WriteNode` / `ReviewNode`. Drafts that
still held proposal items lose them (no data migration). The Go field is still named `Items`; the unused
`change_item.target_id` / `target_version` columns stay.
