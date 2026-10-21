# ADR 0141 — The links of a version are a set, shared or written as a delta

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0032 (versions and branches), 0079 (drafts and
versions at landing), 0140 (a baseline is computed from the versions that landed)

## Context

The outgoing links of a node belong to its version (the versioning rule of the graph): every new version copied all
the links of the version it came from. A commit was bound to the links of the nodes it touched, not to what it changed.
On the bench of ADR 0140 the versions per change stayed at about 50 while the link rows per change went from 159 to
380: the nodes many changes touch gain links over time (a parent gaining a child), and each of their versions copied
them all again, whether the change edited a property or added one link.

## Decision

1. **The links of a version are a link set.** The rows of `link` written under a version (`from_id`, `from_version`)
   are the rows of its set. A version holds its links one of three ways (`node_version.links`, `links_base`,
   `links_depth`, migration 0012; `graph.LinkSet`, `Tx.LinkSetOf`):
   - its own whole set (`links` NULL, `links_base` NULL): the rows it wrote;
   - the set of an earlier version of the node as is (`links` = that version): a version that changes only attributes
     writes no link;
   - its own rows as a delta over the set of an earlier version (`links_base`, `links_depth` deltas deep): a row per
     link it adds, and a row per link it takes out (`link.removes`, the id of the row it drops).
2. **The writer decides** (`Graph.putLinks`, `graph/graph/linkset.go`), given the links of the new version and the
   versions it was made from (its parents; a revert the version of the fork first): the same links as one of them (by
   type, target and properties, `diffLinks`) share its set; else a delta over the set of the first when the delta is at
   most half the links and the set is less than `maxLinkDepth` (16) deep; else the links whole. A link kept keeps its
   id. `Tx.PutLinkSet` and `Tx.DropLink` write it, for a version written in the transaction only (the guard).
3. **Reads compose the set.** The links a version holds are the rows of the chain of its sets (its set, then the set it
   is a delta over, at most 16 deep) minus the rows a set of the chain drops, read as leaving that version: one
   recursive query (`dialect.heldLinks`) for `OutLinks`, `LinksFrom` and `LinkPage`. The links entering a version are
   the rows naming it, the sets holding each (the row's, then the deltas over a set holding it that do not drop it) and
   the versions holding those sets (`sqlLinksTo`): one link per version holding it, so a link read as entering a node
   may come back with the same id from several versions of the source (`InLinks`, `LinksTo`). `Link(id)` reads a row as
   leaving the version that wrote it.

## Consequences

- A version that edits a node's properties writes no link; one that adds a link to a node of N links writes one row,
  not N + 1.
- The link ids of a version are those of the rows it holds: an unchanged link keeps its id across versions. The draft
  of a node still gives its links ids of its own; a link of the version checked out is found by its stored id through
  the version's set (`linkWork`).
- Reading the links of a version walks a chain of at most 16 sets, by the primary key of `node_version` and the
  `link_from` index; the entering links follow the deltas down (`node_version_links`, `node_version_links_base`).

## Addendum (2026-10-21): skip deltas, and the entering links read in two parts

A long run of the bench (2.5 M versions, hot nodes taking a fifth of the modifications and a third of those adding a
link) showed the delta over the previous set going wrong for a node whose links grow. Each delta adds one row. Every
`maxLinkDepth` sets the node writes its links whole, so its rows per version grow with its links. That run stored 20 M
link rows for 4 M links written, 7.9 rows per version and rising. The changes touching such a node paid it at commit,
and CommitChange's p95 went from 60 ms to 6 s while its p50 stayed flat.

1. **The order of a set.** `node_version.links_seq` (migration 0019, `LinkSet.Seq`) is the order of a set among the
   sets of its node. A new set follows the one held by the version it was checked out from, else the previous
   version, so its order is that set's order plus one. A version sharing a set carries the set's order.
2. **Skip deltas.** The set of order k is written as a delta over the set of order `k & (k-1)` (`baseSet`), found on
   the chain of the set it follows. It is written whole when k is a power of two, when the chain does not reach that
   set, or when the delta would have as many rows as the links.
   - Its chain is as deep as the bits set in k, less one: at most 16 under 2^17 sets.
   - Over K sets, a delta holds the changes of about log2(K)/2 sets.
   - The node writes O(K log K) rows of changes plus its links at each power of two, instead of its links every 16 sets.
   - The order, not the version number, picks the base. The versions sharing a set (an edit of the properties) would
     otherwise leave the chain deep and the deltas long.
3. **Entering links.** `linksToRest` joins the versions holding a set in two parts: the version whose set it is (by the
   key of `node_version`), and the versions sharing it (`node_version_links`). One join with an `OR` read every version
   of the source node for each set. On a node linked from a hot node, `InLinks` went from 1015 ms to 17 ms on the same
   data. This is the 20-26 s p95 of GetNode and GetNodeNeighbourhood.
