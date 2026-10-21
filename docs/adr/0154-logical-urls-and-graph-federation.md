# ADR 0154 — Logical URLs and the federation of graphs

**Status**: proposed; step 1 implemented · **Date**: 2026-10

## Context

A link of the graph points to an exact version of a node by its internal id (`domain.Link.To`, a `NodeRef{ID,
Version}`, a foreign key to `node_version`). That id means something in one database only: a link cannot leave the
graph. Yet what a change is about often lives elsewhere: the requirements of a partner held in another GOAP graph, a
file of a repository, a document of a shared drive. Two graphs of one enterprise (one per entity, or per security zone)
cannot name each other's nodes; the git importer writes files as nodes because a link has no other way to point to one.

What a person means by a link is rarely "version 7 of node 4f1c…": it is "the requirement REQ-12 of the ALM of that
entity". The identity a reader understands is the source of the thing, its type and its key. Keys are already unique
per namespace (`NodeIDByKey(namespace, key)`), and a type already belongs to the namespace of its domain (ADR 0012).

## Decision

1. **A logical URL names what a link points to**: `lnk:<source>/<type>/<key>` (`contracts/lnk`, `lnk.URL`, `Parse` /
   `String`, a string in JSON). The type is named in its domain (`Requirement`, not `alm@Requirement`): the namespace
   is a property of the source, never of the URL. The key keeps its slashes (a path is a key); what a path segment
   cannot carry is percent-encoded (`#`, `?`, spaces), so the text round-trips. `?v=<n>` pins a version, for the
   sources that keep versions; without it the URL is the logical identity of the thing, whatever its version.
2. **A source says where things live** (`lnk.Source`): its name (the `<source>` of the URLs), its kind (which resolver
   reads it), the namespace of the graph it reads, its location in terms of services (the base URL of a graph service,
   a root directory), the branch, and opaque parameters for the resolver. A source is declared by a `platform@Source`
   node (`SRC:<name>`, `platform` 2.8.0, `adminOnly`, lifecycle `config`: retired, it no longer resolves): what
   describes the enterprise is graph data (CLAUDE.md, rule 4), and only administrators say where the platform reads.
3. **One kind for graphs, `graph`; this graph is the one with no location**: every namespace of the graph is a graph
   source of its own name with no location, with no declaration (`lnk:alm/Requirement/REQ-1` is the node `REQ-1` of the
   namespace `alm`). A declared `graph` source with no location names a namespace (and a branch) of this graph under
   another name; with a location, a namespace of another graph service. A source no one declared falls back on the
   namespace of that name, which finds nothing when no such namespace holds the key. Resolving checks the type: the
   node's type, or one it extends (`TypeCatalog.IsA`). There is no separate kind for this graph: whether a graph is
   read in process or over the network is where it lives, not what it is.
4. **A resolver reads one kind of source** (`lnk.Resolver`): `Resolve(ctx, source, url) → Target` and `URLOf(ctx,
   source, target) → url`. One resolver serves every source of its kind (each graph service, each root directory): the
   call names the source. A `lnk.Target` is the level of abstraction every source shares: the kind of the target
   (`node`, `file`, `directory`, or one of the resolver), its type (a qualified node type for a node), its key, a title,
   and what is specific to the kind (the node in its graph, the location of a file, the properties read). So a link may
   point to things that are not nodes: a file by its path, a web document by its address.
5. **A registry dispatches** (`lnk.Registry`): the sources declared in process, `Lookup` for the ones the graph declares,
   the resolvers by kind; the graph's is `Graph.Lnk()` (`Graph.Resolve`, `Graph.LinkURL`), built at its first use from
   the graph resolver (`graph.Resolver`: in process with no location, else the `graph` resolver of `Graph.Resolvers`),
   the `platform@Source` nodes on main and `Graph.Resolvers`, a seam the
   compositions plug like the others (`graphsvc.Resolvers()`: `platform/cmd/graph`, `goap-dev`).
6. **Kinds shipped**: `graph`, this graph in process or another graph service (`graphclient.Federation`: one client per location, the
   node read by its key with `GetNode`, so federation needs no new RPC); `files`, a root directory
   (`lnk.Files`: the key is the slash path under the root, type `File` or `Directory`, read through an `os.Root`, never
   outside it). A new kind is a resolver registered under its name, nothing in the graph.
7. **A working node carries its source**: `domain.Node.Source` names the source a node was read through when it is not
   this graph (the federation resolver sets it); `Node.SourceName()` is that source, else the node's namespace, a
   source of this graph; `Node.URL()` its logical URL, `Node.Target()` the node as a target. Every node, read here or elsewhere,
   so has a URL with no lookup.

8. **Sources are declared on the platform, resolved per project.** A source is declared once, by a `platform@Source`
   node; which declared source holds the things a project's URLs name is the project's: `organisation@ProjectUnit`
   `sources` (`organisation` 1.16.0), an object of the name a URL gives a source to the declared source holding it
   (`{"alm": "content-eu"}`), inherited by the sub-projects, the nearest project naming a source winning
   (`access.Snapshot.ProjectSources`; a project naming none takes its parents', the root's a default, and a source no
   project maps is read as named). A URL keeps its name wherever it is stored: the mapping says where, never what
   (`lnk.Mapping`, `Registry.ResolveIn`: the target keeps the URL's source). A node belongs to a project, so its links
   resolve in it (`Graph.ResolveFrom`, `Graph.ResolveIn(project, url)`). The graph reads the mappings through the seam
   `Graph.ProjectSources`, plugged by the compositions from the organisation directory they already hold
   (`access.Directory.ProjectSources`): read at boot, kept by following the head of the organisation (each read asks
   for the head and reads the delta, ADR 0145), so a project edited is in force at the next resolution.
9. **Two graph spaces.** The platform and the content are two graph services, two instances of the same binary on
   their own databases: the **platform graph** holds what describes the enterprise and the platform (`platform`,
   `organisation`, `methodology`), the **content graph** the domains' content (`alm`, `code`, ...). The domains stay
   in the one domain registry (ADR 0125 §3), served by the platform graph, read by both. The content graph is a
   source of the platform (a `platform@Source` of kind `graph` with the content graph's location, mapped by the root
   project), so a project may put its content in another content graph (another database, another region) by
   mapping its sources elsewhere. The content graph reads the organisation from the platform graph (its access
   directory over `graphclient`, the platform graph's node events on the bus keeping it current) and builds its
   in-memory directory of projects and sources at boot. What the split asks of the code, in order:
   1. the owner unit and the project of a version and of a change become keys of the platform graph (`node.project_id`,
      `node_version.owner_id` lose their foreign keys to `node`; the guard checks them against the directory instead
      of the transaction);
   2. links become URLs (step 2), so a content node may name a node of the platform graph (an owner, a project, a
      methodology) and the reverse;
   3. the clients route by namespace (`graphclient.Router`: a namespace to the graph serving it, from the sources of
      the platform; the engine, the hub, the registry and the gateway take it where they take a `graphclient.Client`
      today), a change acting on one namespace (CLAUDE.md, rule 2) runs on one graph;
   4. the deployment runs `graph-platform` and `graph-content` (compose services, one database each, the gateway
      routing by namespace), `goap-dev` keeping both in one process over one database.

## Steps

1. **Done (this ADR)**: the contract (`contracts/lnk`: URL, source, target, resolver, registry, the files resolver), the
   graph resolver and the declared sources in the graph, the federation resolver over the graph client, the source of a
   node, the `platform@Source` type. Links are unchanged: `Graph.LinkURL` gives the logical URL of a link's target.
2. **Logical targets of links.** A link's target becomes its URL: `Link.Target lnk.URL` (proto `Link.target`), the
   writes taking a URL (`LinkWrite.Target`; a `NodeRef` still accepted, turned into its URL). A link to a node of a
   graph source is always versioned (for now): its URL carries `?v=`, the version it was written against, so the
   suspect links, the lineage, the rebase and the merge keep re-pointing it as they do today. Storage, greenfield (the
   database starts from zero, no migration): `link.to_url` in `0001_schema.sql` of both dialects (indexed), `to_id` /
   `to_version` kept for a target in this graph as the resolution of its URL (the in-links read stays as it is) and null
   for any other source. A link set (ADR 0141) compares links by type and URL. A link of a draft to a node created in the
   same change is the URL of that node's key (the draft reference goes), versioned at commit.
3. **Resolution at read.** A view of a node (`NodeView.out`) resolves the URLs of its links in one batch per source
   (`Registry.ResolveAll`), a target not found or a source unreachable shown as such, never an error of the read;
   the web draws a target of another source with its source (a chip), opens a node of another graph read-only, and a
   file with the reader of its kind. `ResolveLink` (RPC) serves the web and the MCPs.
4. **Federation trust.** A graph reading another acts as itself: a service token per source (`Source.Params`, a secret
   reference resolved like an adapter's, ADR 0061), the remote graph authorizing the reading graph as a `system:`
   principal `.For` the person (ADR 0114). Until then the federation client forwards the caller's identity, which only a
   graph of the same platform trusts.
5. **Per-project sources** (done): the mapping of §8, the seam `Graph.ProjectSources`, `Graph.ResolveIn` /
   `ResolveFrom`.
6. **Graph spaces**: the four moves of §9, each its own change.
7. **The other readers of links**: the OKF export writes a link to another source as its URL, the snapshot import /
   export carries URLs (a snapshot of one graph imported in another keeps its links to the first), the index stores the
   URL of each link target, the git importer may point a `Symbol` to its file by URL instead of a node.

## Consequences

- A link may leave the graph: to another graph, a file, any source a resolver reads. The type/key tuple is the identity
  every source shares, the namespace being the source's.
- Nothing changes for the links already written until step 2, which changes the schema in place (greenfield, no
  migration) and makes link identity logical while keeping graph targets versioned: floating links can come later,
  by dropping the `?v=` of a URL, without another change of storage.
- A target in another source can disappear or change without the graph knowing: its links are resolved when read and a
  missing target is a state of the link (step 3), not a broken foreign key. Integrity across sources is not checked at
  commit.
- Keys become part of the public identity of a node: renaming a key breaks the URLs pointing to it, as it already broke
  imports and the references by key.

## Decided since

- **Graph links are versioned** (Thomas, 2026-10-21): a link to a node of a graph source always names its version, for
  now. A floating link (a URL with no `?v=`, resolved to the version the reader's branch holds) is left for later.
- **Files are a declared source with its resolver**: no source of files is implied; an administrator declares one, and
  the resolver of its kind tells how its files are read. `lnk.Files` (an `os.Root` the service reads) is one such
  resolver; a resolver may as well go through an MCP (a `mcp` kind calling a tool of the hub for the unit, the files
  of a unit, ADR 0019), which follows rule 5 (what is available comes from who) in a distributed deployment.
- **Storage is greenfield**: step 2 changes `0001_schema.sql` of both dialects, no migration.
- **Sources declared on the platform, resolved per project; two graph spaces** (Thomas, 2026-10-21): §8 and §9.
