# ADR 0145 — One way to read the graph at a commit

**Status**: accepted; implemented · **Date**: 2026-10 · Builds on ADR 0143 §4 (a view of the head follows it by deltas)

## Context

Several services keep a view of the head of the main branch of a namespace in memory and answer from it: the access
(`lib/access`, the organisation namespace), the hub (`mcp/mcpsvc`, the organisation and platform namespaces), the model
gateway (`lib/llmcfg`, the platform namespace) and the methodology registry (`platform/registrysvc`, the methodology
namespace). ADR 0143 §4 made each of them follow the head by deltas (`graphsnap.Head`, `Graph.BaselineDelta`), but each
still held and moved its own copy: `goap-dev` kept the organisation twice (access, hub) and the platform twice (hub,
models), and the registry, the hub and the model gateway each looked at their heads once per view. The hub looked at
two heads on every request, with no TTL.

A separate service holding the views and answering them over the network was considered and left: the access is asked
on every request of every service, a network hop there costs more than the copy it saves, and a view of the head is
built from a few hundred configuration nodes, not the graph.

## Decision## Decision

1. **One method gives the graph at a commit**: `graphsnap.Reader.At(namespace, commit, suspects)` returns the state of
   a namespace at a baseline; `Reader.Head(namespace)` names the commit at the head of its main branch. A commit never
   changes, so the state at one is a pure function of it and nothing can go stale: the reader keeps, per namespace, the
   last state it gave (one copy, not one per commit), and reaches the next commit asked from it by the nodes landed in
   between (`Graph.BaselineDelta`), else by reading that commit whole (an earlier commit, another branch, too large a
   delta). It keeps nothing about the head: there is no TTL, no refresh, no "fresh" read.
2. **A view is a projection of heads**: `graphsnap.View[T]` names its namespaces and a `Build` over their states; each
   `Get` asks the reader for the heads (one query per namespace) and builds the view again only when one of them is
   another commit than the one it was built from (`Peek` gives the last one without asking). `graphsnap.One` is the
   view of one namespace. The access, the models, the hub and the registry are views; each takes the process's
   `Reader` (a field of `access.Directory`, `llmcfg.Directory`, `mcpsvc.Directory`;
   `registrysvc.GraphStore.ShareReader`), nil meaning a reader of its own.
3. **Each composition makes one reader and gives it to every view**: `goap-dev` (`graphPart.reader`),
   `platform/cmd/mcp` (hub and access), `platform/cmd/modelgw` (models and access), `platform/cmd/registry`
   (methodologies, access, MCP scopes). A service with one view keeps the one of its directory.

## Consequences

- A process holds each namespace once; a new view of the graph is a `View` over the process's `Reader`, a read of the
  graph at a commit is `Reader.At`, never a reading logic of its own.
- Every read of a view sees the head as it is: a grant, a model or an adapter is seen by the next request. The price is
  one look at the head per read (an indexed query; an RPC for the services that are not the graph).
- The `TTL` of the directories, `Directory.Fresh`, `Cache.Fresh` and the model gateway's `syncNow` are gone.
