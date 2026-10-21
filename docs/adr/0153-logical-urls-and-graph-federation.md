# ADR 0153 — Logical URLs and the federation of graphs

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
3. **The self resolver is always there**: every namespace of the graph is a self source of its own name, with no
   declaration (`lnk:alm/Requirement/REQ-1` is the node `REQ-1` of the namespace `alm`). A declared source of kind
   `self` names a namespace (and a branch) under another name. A source no one declared falls back on the self source
   of that name, which finds nothing when no such namespace holds the key. Resolving checks the type: the node's type,
   or one it extends (`TypeCatalog.IsA`).
4. **A resolver reads one kind of source** (`lnk.Resolver`): `Resolve(ctx, source, url) → Target` and `URLOf(ctx,
   source, target) → url`. One resolver serves every source of its kind (each graph service, each root directory): the
   call names the source. A `lnk.Target` is the level of abstraction every source shares: the kind of the target
   (`node`, `file`, `directory`, or one of the resolver), its type (a qualified node type for a node), its key, a title,
   and what is specific to the kind (the node in its graph, the location of a file, the properties read). So a link may
   point to things that are not nodes: a file by its path, a web document by its address.
5. **A registry dispatches** (`lnk.Registry`): the sources declared in process, `Lookup` for the ones the graph declares,
   the resolvers by kind; the graph's is `Graph.Lnk()` (`Graph.Resolve`, `Graph.LinkURL`), built at its first use from
   the self resolver (`graph.SelfResolver`), the `platform@Source` nodes on main and `Graph.Resolvers`, a seam the
   compositions plug like the others (`graphsvc.Resolvers()`: `platform/cmd/graph`, `goap-dev`).
6. **Kinds shipped**: `self`; `graph`, another graph service (`graphclient.Federation`: one client per location, the
   node read by its key with `GetNode`, so federation needs no new RPC); `files`, a root directory
   (`lnk.Files`: the key is the slash path under the root, type `File` or `Directory`, read through an `os.Root`, never
   outside it). A new kind is a resolver registered under its name, nothing in the graph.
7. **A working node carries its source**: `domain.Node.Source` names the source a node was read through when it is not
   this graph (the federation resolver sets it); `Node.SourceName()` is that source, else the node's namespace, the
   self source; `Node.URL()` its logical URL, `Node.Target()` the node as a target. Every node, read here or elsewhere,
   so has a URL with no lookup.

## Steps

1. **Done (this ADR)**: the contract (`contracts/lnk`: URL, source, target, resolver, registry, the files resolver), the
   self resolver and the declared sources in the graph, the federation resolver over the graph client, the source of a
   node, the `platform@Source` type. Links are unchanged: `Graph.LinkURL` gives the logical URL of a link's target.
2. **Logical targets of links.** A link's target becomes its URL: `Link.Target lnk.URL` (proto `Link.target`), the
   writes taking a URL (`LinkWrite.Target`; a `NodeRef` still accepted, turned into its URL). Storage: `link.to_url`
   (both dialects, indexed), `to_id` / `to_version` kept for a self target as the resolution the commit made (the
   version the link was written against: the suspect links, the lineage and the in-links read stay as they are) and
   null for any other source. A link set (ADR 0141) compares links by type and URL; a rebase or a merge no longer
   re-points a link to a self node at a new version, since its URL did not change. A link of a draft to a node created
   in the same change is the URL of that node's key (the draft reference goes).
3. **Resolution at read.** A view of a node (`NodeView.out`) resolves the URLs of its links in one batch per source
   (`Registry.ResolveAll`), a target not found or a source unreachable shown as such, never an error of the read;
   the web draws a target of another source with its source (a chip), opens a node of another graph read-only, and a
   file with the reader of its kind. `ResolveLink` (RPC) serves the web and the MCPs.
4. **Federation trust.** A graph reading another acts as itself: a service token per source (`Source.Params`, a secret
   reference resolved like an adapter's, ADR 0061), the remote graph authorizing the reading graph as a `system:`
   principal `.For` the person (ADR 0114). Until then the federation client forwards the caller's identity, which only a
   graph of the same platform trusts.
5. **The other readers of links**: the OKF export writes a link to another source as its URL, the snapshot import /
   export carries URLs (a snapshot of one graph imported in another keeps its links to the first), the index stores the
   URL of each link target, the git importer may point a `Symbol` to its file by URL instead of a node.

## Consequences

- A link may leave the graph: to another graph, a file, any source a resolver reads. The type/key tuple is the identity
  every source shares, the namespace being the source's.
- Nothing changes for the links already written until step 2; step 2 needs a migration of `link` (both dialects) and
  makes link identity logical: a link to a node no longer moves when the node gets a new version, which is what the
  rebase and the merge have been doing by hand.
- A target in another source can disappear or change without the graph knowing: its links are resolved when read and a
  missing target is a state of the link (step 3), not a broken foreign key. Integrity across sources is not checked at
  commit.
- Keys become part of the public identity of a node: renaming a key breaks the URLs pointing to it, as it already broke
  imports and the references by key.

## Open questions

- Pinned or floating by default: a link written in a change, should its URL keep the version it was written against
  (`?v=`) or float to the version the reader's branch holds? Step 2 keeps both: the URL floats, the resolution recorded
  at commit pins.
- Should `files` sources be readable by the graph service at all in a distributed deployment, or only through a
  connector of the MCP hub (the files of a unit, ADR 0019)? A `connector` kind resolving through the hub would follow
  rule 5 (what is available comes from who).
