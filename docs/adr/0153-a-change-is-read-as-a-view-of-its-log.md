# ADR 0153 — A change is read as a view of its log

**Status**: accepted; implemented · **Date**: 2026-10

## Context

A change is its log (ADR 0029, 0030): the impacts, the drafts and the items are folds of its entries. The graph
already kept one fold per change, cached and extended entry by entry (`draftState`, ADR 0079 §2), and a write folded
its own events into it (`emitEntry`). An audit of the operations on a change (the second performance round) found the
rule only half kept:

- what the fold did not hold was folded again from the whole log on each call: the phase each impact was written in
  (`phases`, on every edit, checkout, create, transition and review of a change that moved), what a flow other than the
  main one sees (`ImpactsSeenBy` over every impact event), what stale runs left (`DraftSeenBy`), the pending conflicts
  (`RebaseState`, `ImpactNodeResolve`), the moves of a draft at commit (`walkTransitions`), the adoption of a flow, and
  the history facets (`LogFacets`, the whole log read for every blackboard and every commit);
- the guard read a change whole (its facts and its impacts) to learn its unit and its project;
- a write gave its caller no position: the caller could not tell the view it reads next holds its write;
- some operations opened their work twice (`resolve` after a declaration, a transition after its checkout), and
  several looked a draft up by scanning a deep copy of every draft (quadratic in the impacts).

## Decision

1. **One view per change, folded once.** The view (`draftState`) holds, beside the impacts, drafts, conflicts and items:
   the impact events folded, in the order of the log (what a flow, a stale run or the moves of a draft are read over);
   the phase of each impact (the state it started its draft in, folded with the moves of the change: entries are
   folded in their order, a fact and an impact event alike, `foldEntries`); and the position of each move. Every
   reader of these goes through the view (`frozen`, `reopenPhase`, `flowNodes`, `seenDrafts`, `adoptNodes`,
   `walkTransitions`, `RebaseState`, `ImpactNodeResolve`, `ChangeEvents`, `draftsOf`); nothing on these paths reads the
   log whole.
2. **Facets are built per call.** A log facet (`LogFacet`, the history) is a cache of the log for one call: the call
   that needs it reads its entries and folds them once, uses it for what it does, and nothing of it is kept for the next
   call (stateless: the log is the state). The engine does the same when the graph sends no history (`withHistory`).
3. **The guard reads headers.** The unit and the project of a change are read from its header (`ChangeHead`), never
   from its log.
4. **Positions.** A change is read at a position, `Change.Seq` (proto `Change.seq`): the last impact event or fact its
   view folded. A write records the position it left each change's log at in the caller's watermark
   (`graphapi.WithWatermark`, noted when the transaction commits); a read may require a position
   (`graphapi.AtLeast`), and a view that does not reach it is refused with `graphapi.ErrBehind` (a conflict) rather than
   answered with an older state. Over RPC the answer carries the watermark in `X-Goap-Change-Seq` and the request the
   positions in `X-Goap-At-Least` (`<change>:<seq>` pairs), set by `graphsvc.Handler.Positions` and read back by
   `graphclient` into the context's watermark; the reason `behind` travels with the error (`rpcerr`). The fold stays
   synchronous: a write folds its events into the view before it returns, so a read after it in the same process
   always reaches the mark; the mark is the contract, so that a cache, a replica or a later asynchronous fold can be
   checked against it.
5. **A view bounded at a position.** `Graph.ChangeAt(id, seq)` (RPC `GetChange.at_seq`, `graphclient.ChangeAt`) is the
   fold of the log up to `seq`: the impacts, the items and the state of the change as they stood then, the rest of the
   header as it is now. A position at or past the head is the head view.
6. **Domain side unchanged.** A node version is a whole replica of the node: a read picks the version, never rebuilds
   it from history. The links of a version are the one sanctioned chain (ADR 0141), read in one query. The split reads
   the links entering every version of a source in one query (`LinksInto`).

## Consequences

- An operation on a change with a long log costs what its own events touch: no read of the log beyond the entries
  after the view, no decode of the facts or the impact events again.
- The view holds the decoded impact events of the changes it caches (bounded by `DraftCacheSize`).
- The web reads the positions (`web/src/lib/flux/positions.ts`): the transport notes the ones a graph answer carries
  (`X-Goap-Change-Seq`), the reducers the ones the change events carry (`change.updated`: `Change.Seq` of the write,
  `Event.version` on the stream), `getChange` the one it read; every graph request naming a known change (`changeId`,
  `id`, `parentId`) asks for it in `X-Goap-At-Least`, and a read refused as behind is tried again three times. The
  gateway lets both headers and the error reason through CORS.
- A facet costs one read of its entries of the log per call that needs it; no cache to keep in step with the log,
  and every replica answers the same.
