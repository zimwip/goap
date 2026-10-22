# ADR 0161 — Access rights and data segregation: roles as action lists, projects own the data, the lifecycle opens it

**Status**: proposed · **Date**: 2026-10 ·
Builds on ADR 0039 (projects, Assignments), ADR 0043 / 0047 (project and platform roles), ADR 0054 (owner and project
of a version), ADR 0103 (operation catalogue), ADR 0138 (deny by default), ADR 0157 (every read names its view).
Revises ADR 0138 §2 (the `owner` / `admin` flags become built-in roles).

## Context

Two questions are mixed today: what a person may **do** (an action) and which nodes a person may **see** (the data).

- **Roles.** A methodology role lists the operations on a change it allows (`methodology@Role.operations`, the
  generated catalogue of `contracts/operations`). Every other right is a Casbin rule per `resource:action`
  (`object:create`, `tool:call`, `process`, `prompt:inspect`...), mostly `onProject` or `mayRun`. The built-in roles
  (`admin`, `reader`) are granted only platform-wide, and the rights on a project are two flags of an Assignment
  (`owner`, `admin`, ADR 0138). "Platform role" means both *of the fixed catalogue* and *valid everywhere*.
- **Reads.** Only `GetNode` and `TagVersion` check the node's project (`graphsvc.Handler.mayReadNode`).
  `GetNodeNeighbourhood`, `ListBaselineNodes`, `ListViewNodes` and `ListBaselineLinks` return nodes of any project.
  The index filters by type and project (`indexersvc.readFilter`).
- **Segregation.** A node has an immutable project (`Node.Project`) and a responsible unit (`Node.Owner`, ADR 0054,
  used by the adapters and `SplitByOwner`). Neither says who may see it beyond the project: the data of company A and
  company B are apart only if they never share a project, and there is no way to keep a draft to its author or to
  open a released node to other projects.
- **Services.** A `system:` principal reads everything (`worksOn`), also when it acts for a person (`OnBehalfOf`).

## Decision

### 1. Two axes, decided together

**Capability** (may this person perform this action) comes from roles. **Reach** (may this person see this node
version) comes from the node's project, its responsible unit, its creator and the scope of its state. A call passes
only when both hold. A role never widens reach; reach never grants an action.

### 2. Roles are action lists

- **One action catalogue.** `contracts/operations` grows from the operations on a change to every action a person
  performs (`node:read`, `object:create`, `process:start`, `tool:call`, `prompt:inspect`, `assignment:write`,
  `project:stop`, `node:share`, `node:reown`...), each with its kind (read, doer, reviewer, administer). The generator
  keeps refusing an action a code path asks for that no one declared.
- **Two sources of roles, one shape.** Built-in roles are `platform@Role` nodes shipped with the code and seeded like
  the built-in MCPs (`admin`, `reader`, `project-owner`, `project-admin`); methodology roles are `methodology@Role` of a
  methodology version. Both list actions.
- **Definition and grant are separate.** An Assignment grants any role at one scope: no project, platform-wide; a
  project, that project and the ones below it. A built-in role may be granted on one project. A methodology role is
  grantable only on a project where its methodology applies (checked at commit, next to `ProjectScopeValidator`).
- **The ADR 0138 flags become built-in roles.** `project-owner` and `project-admin` carry the actions the flags gave
  (`assignment:flag`, `project:stop`, `project:move`...); the validators keep their rules ("always an owner").
- **Casbin keeps the conditions, not the grants.** The allow side becomes "the action is in one of the caller's roles
  here"; the deny rules stay (not on one's own change, not the producer, four eyes). The role names written in the
  defaults (`release_manager`, `derogation_signatory`) move to the methodologies that use them.

### 3. The project owns the data, the organisation answers for it

- **Ownership is the project.** Every node belongs to one project (`Node.Project`). Who works on a project is said by
  its Assignments: an Assignment of unit A to project P means "the users of company A work on P". Units match
  downwards in the organisation, projects downwards in the project tree, so an Assignment on a parent project reads
  its sub-projects (kept: a steering project sees its parts).
- **Responsibility is the organisation.** `Node.Owner` is renamed `Node.Responsible` (the model, the protocol, the
  web): the unit answering for the version, moved by `NodeUpdate.Responsible`. Two new fields record who acted:
  `Node.Creator` (the subject and its unit at creation, immutable) and `Node.Editor` (per version: the person whose
  edits made it, the end user when an agent edited for them; the first version's editor is the creator), stamped at
  commit. Creator and responsible unit narrow reading inside the project (§4); the editor is attribution only.
- **Acting needs ownership.** A change checks out and edits only nodes its project (or a sub-project) owns, and the
  author needs the action in a role on that project. Seeing a node of another project gives no action on it; linking
  to it from a node one owns needs only reading it (the link belongs to the source version).
- **Reowning is administrative.** `node:reown`, an action of an administrative methodology
  (`Methodology.Administrative`), held by an administrator of both projects or a platform administrator, moves a node
  to another project, recorded as a change entry `project {from, to}`. A reowned node is wholly the new project's: the
  old project loses every right on it, its earlier versions included.
- **Physical separation** is a content graph an administrator allocates to a project by hand (the project's
  `sources`, ADR 0154 §8). The same rules apply inside that graph.

### 4. The lifecycle state declares how far a version is seen

| Scope | Who reads a version in that state |
| --- | --- |
| `personal` | its creator alone, inside the owning project |
| `private` | the members of its responsible unit (and the units below it), inside the owning project |
| `project` (default) | everyone who works on the owning project or a project above it |
| `tree` | everyone who works on a project of the owning project's parent tree, siblings included |
| `public` | every identified person |

- **Declared by the domain.** A lifecycle state gains `visibility:` next to `notCommittable`
  (`domain.LifecycleState.Visibility`); a node type without a lifecycle takes a type-level `visibility:`, inherited
  through `extends`. The built-in namespaces (organisation, platform, methodology) declare `public`, which replaces
  their special case in `mayReadNode`. Publishing is a transition, governed by its guards, gates and roles; there is no
  per-node override.
- **Leaving `personal` is the creator's act.** A transition out of a `personal` state to a wider scope is performed by
  the creator only, through `node:share`, whatever the lifecycle says: neither an administrator nor an agent widens a
  personal node.
- **Stamped at commit.** `writeVersion` stamps the scope on the version (`node_version.visibility`, both dialects) from
  its type and state as the domain says then. A later domain change applies to the versions written after it.
- **Read only.** A scope opens reading, never an action (§3).
- **The last release.** A reader the version a view holds does not reach gets the node's last version, on the branch
  up to the view's baseline, whose scope reaches them, else nothing: the selection of ADR 0143 with one more predicate.
- **Drafts never leave their change.** A draft is read through its change only (ADR 0153); scopes apply to committed
  versions.

### 5. One read filter, at the view

- **Where.** The reach filter lives in the view reader (ADR 0157: `openWorld`, `ViewNode`, `ViewNeighbourhood`,
  `ViewNodes`), as a seam `graphsvc.Boot` requires like the access hooks, so the handler, `graphapi.Port`, the MCP
  tools and the engine get it at once. History reads (versions, diffs, change-stacks) check each version they return.
  Commit, merge, rebase, guardians and validators never go through it: they judge the whole graph.
- **Lists and counts.** Paged reads push it into the selection, so totals and type counts count only what the caller
  reaches. Links to a version out of reach resolve to the last release in reach, else are dropped (no stub: a stub
  tells the node exists); back links from nodes out of reach are dropped. `graph.propagate` walks what the initiator
  reaches and reports how many links lead outside it, never their keys.
- **Search.** Index documents carry `Visibility`, `Creator` and `Responsible` next to `Project`; the store filters
  before ranking and counting.
- **Who reads.** Rights are decided outside the model: an agent, the assistant or the helper inherits the rights of the
  person it acts for, so a model exchange holds nothing that person could not read, and `prompt:inspect` follows the
  same rule. A `system:` principal acting for a person (`OnBehalfOf`) reads as that person; only a service acting by
  itself reads everything.
- **The platform administrator does not see everything.** The admin floor covers administration (organisation,
  Assignments, methodologies, domains); an administrator reads a project's data only by assigning themselves to it,
  which is journaled like every Assignment.
- **Performance.** No Casbin call per node. From the access snapshot, a **reach set** is compiled per principal (the
  projects worked on with their sub-projects, the trees reached, the subject and units for `personal` and `private`),
  kept in a `lib/cache` keyed by subject and organisation head and dropped by the baseline events the directory
  follows (ADR 0154 §9). A version is checked by set lookups over stamped keys (project, visibility, creator,
  responsible unit); SQL gets `IN` lists. The last-release fallback adds one predicate on the fold index, measured by
  `BenchmarkNavigation` on a graph where most nodes are in work.

## Phasing

1. **Close the gap.** The read filter in the view reader with the project rule only; neighbourhoods, pages, type lists
   and links filtered, counts after the filter; `system:` acting for a person reads as that person; a change writes
   only nodes its project owns.
2. **Creator, editor, scopes.** `Node.Creator`, `Node.Editor`, `node_version.visibility` stamped at commit (both
   dialects), `visibility:` on lifecycle states and node types, `node:share`, the reach set, the index fields; the
   rename to `Responsible`.
3. **The last release** in the view reader and the index, links resolved to it.
4. **Roles as action lists.** The catalogue extended, built-in roles grantable per project, the flags folded into
   built-in roles, Casbin left with the deny conditions, `node:reown`.

Phase 4 is independent of 2 and 3.

## Consequences

- A domain decides how far its nodes are seen, state by state; a methodology decides who may move them there.
- Company A and company B on one piece of work are sibling projects under a joint one; their released nodes reach each
  other through `tree`.
- Administrators lose the implicit read of every project's content; support work goes through an Assignment.
- Every read path that returns nodes goes through the view reader; a new one that does not is a leak.
- **Personal nodes are personal.** When their creator leaves, nothing passes to anyone: the nodes whose every version
  is `personal` are deleted, with their versions and the links they hold. It is the one exception to "a node is never
  deleted" (ADR 0079): no one else ever read them, and no one else could link to them. A node that once left
  `personal` is shared and is kept whole, its earlier personal versions included.
