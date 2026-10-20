# ADR 0145 — One reader of the heads of the graph per process

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

## Decision

1. **One reader of the heads per process**: `graphsnap.Heads` holds one `Head` per namespace, read whole once (with
   its suspect links) then moved by the nodes landed since, looked at the graph at most once per `TTL` (one second;
   negative: at every read), or at once after `Moved(namespace)`. `Heads.Head` gives the baseline of a namespace's
   head, `Heads.State` its state, with or without the suspect links (`Head.State(suspects)`).
2. **A view is a projection of heads**: `graphsnap.View[T]` names its namespaces and a `Build` over their states, and is
   built again only when one of their baselines moved; `Get` / `Fresh` / `Peek`. `graphsnap.Cache[T]` is a view of one
   namespace. The access, the models, the hub and the registry are views; each takes `Heads` (a field of
   `access.Directory`, `llmcfg.Directory`, `mcpsvc.Directory`; `registrysvc.GraphStore.ShareHeads`), nil meaning a
   reader of its own.
3. **Each composition makes one reader and gives it to every view**: `goap-dev` (`graphPart.heads`), `platform/cmd/mcp`
   (hub and access), `platform/cmd/modelgw` (models and access), `platform/cmd/registry` (methodologies, access, MCP
   scopes). A service with one view (`platform/cmd/graph`, the gateway, the engine, the indexer, the events and
   conversations services) keeps the one of its directory.

## Consequences

- A process holds and moves each namespace once; a new view of the head is a `View` over the process's `Heads`, never a
  read of a baseline of its own.
- The hub now looks at its heads once per second when it shares the process's reader, like the access; on its own it
  still looks at every read.
- Next: move the heads on the graph's baseline events (`Heads.Moved`) rather than once per TTL, once those events name
  their namespace.
