# ADR 0142 — Impact events carry what they change, not the node

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0029 (change impacts are event-sourced), 0030
(one log per change), 0079 (drafts and versions at landing), 0141 (link sets)

## Context

On the bench of ADR 0140 (120,000 versions), `change_log` weighed about ten times `node_version` (793 MB against
84 MB), while a change touching each node a few times should cost twice at most. By event type, the largest share
was `impact.checkedOut` (1.8 KB on average, 142 MB): the draft a checkout installs is a copy of the version it was
checked out from, and the event embedded it whole, properties and links. Every payload also repeated what the columns
of its log entry already hold: the event id, the change, the impact, the flow, the execution, the caller, the time,
the causation (about 200 bytes per entry over 800,000 entries), and the change impact of a `proposed` / `created`
event repeated its id.

## Decision

1. **The payload of an impact event leaves its envelope to the columns** (`domain.ImpactEntry`, `marshalImpact` in
   `contracts/domain/impactpayload.go`): no `id`, `changeId`, `impactId`, `seq`, `flow`, `execution`, `by`, `at`,
   `causation`; no `id` on the change impact of `state` when it is the event's impact; no `changeId`, `impactId`, `flow`
   on its draft (the fold sets them from the event). `domain.ImpactEventOf(entry)` reads an event back with its
   envelope; every reader of the log goes through it (the graph's `decodeImpacts`, the transitions' phases, the PROV-O
   export, the graph tests), the web's `decodeLogEntry` puts the envelope back the same way.
2. **A checkout of a stored version records the version, not its content.** The `checkedOut` event of a draft that
   copies its base as is carries `fromBase` (`domain.BaseCopy`) and a draft holding only its `base` (and its
   execution); its links get ids derived from the event and the stored link (`domain.BaseLinkID`, a UUID v5), so the
   draft the log rebuilds names the links the later edits name; the links to a version the change holds a draft of are
   listed in `fromBase.drafted` (the checkout turns them into links to the draft). The graph hydrates such events when
   it reads the log (`decodeImpacts`: the versions and their link sets read in one batch, `domain.CopyDraft`); a reader
   outside the graph sees the base only (`ImpactEvent.Hydrated`). A checkout from the draft of a parent flow, a
   creation and an adoption still carry their draft whole: there is no version to name.
3. Edits stay patches: `updated` carries what it changed (`props`, `unset`, `owner`, `addLink`, `updateLink`,
   `removeLink`), a `transitioned` its `state` `{from, to}` and the properties its actions set.

## Consequences

- The log of a change grows with what the change does, not with the size of the nodes it touches.
- Reading the drafts of a change from the log reads the versions its checkouts name (once per fold: the draft cache
  keeps the result).
- The database starts from zero (no migration of stored payloads); `ImpactEventOf` still reads a payload that carries
  its envelope.
- Not done: the previous value of each property an `updated` event sets (a direct record of what it replaced, at the
  cost of its size); the second `landed` event a node gets when its change merges (ADR 0102).
