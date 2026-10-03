# ADR 0053 — One event stream for the web (flux), and presence

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0026, 0029, 0030, 0052.

## Context

The web refetched by hand after every write (`refreshChanges()`, `reload++`, `loaded` guards that never invalidated),
polled (15 s / 30 s / 60 s / 2 s) and kept copies of the same state in several views. Only engine process events
reached it (`EngineService.WatchEvents`, no sequence, no resume). What another user, an agent or the engine wrote was
invisible until a manual refresh, and nothing said who else was looking.

## Decision

- **One stream.** `goap.events.v1.EventService/Watch` (`internal/eventsvc`, `cmd/events`; in `goap-dev` the hub is fed
  in process) carries every fact the services publish as a thin, ordered `Event{epoch, seq, type, kind, id, namespace,
  branch, project, change_id, version, label, actor, command_id}`: `process.*` (with the process / log line),
  `change.*`, `node.written|deleted`, `baseline.advanced`, `methodology.*`, `domain.*`. The graph publishes
  `change.updated` (`goap.changed.<id>`) for any write of a change header, impact or log, so every mutation is an event.
- **Resume.** A hub keeps a ring of recent events. A client resumes with `(epoch, after_seq)`; a gap the ring cannot
  cover, another epoch (restart, another replica) or a stream cut for being slow yields `resync`: whoever holds state
  treats it as stale. `heartbeat` events say the stream is alive, and carry the last seq.
- **Visibility.** Events of a personal change (ADR 0037) go to its subject only; process events follow the process's
  `read` rule (ABAC); the others are open to signed-in callers, as the graph's reads are. Events are thin: a client
  refetches through the services, which decide again.
- **Commands.** Every request carries `X-Goap-Command` (`web/src/lib/flux/commands.ts`); `eventsvc.CommandInterceptor`
  stamps the events a request causes with it and its caller (`actor`), so a client knows the echo of its own writes.
  Over the bus the stamp rides NATS message headers (`platform.Stamp`, `X-Goap-Actor` / `X-Goap-Command`), set by
  `Events.Publish` from the request context and read back by `cmd/events`; the graph and registry services mount the
  interceptor. A draft also ignores events for 3 s after its own write.
- **Front** (`web/src/lib/flux/`): `events.svelte.ts` owns the stream (reconnect, resume, resync); `reducers.svelte.ts`
  turns events into invalidation signals (`signals.svelte.ts`: `stamp(key)` read in the `$effect` that loads, `touch`)
  and re-reads the shared catalogs, tools and projects when theirs move; views depend on a stamp instead of `reload++`.
  A draft with unsaved changes that someone else saved or published shows "take theirs / keep mine" instead of being
  overwritten. Per-run streams, the 15 s / 60 s polls and the refetch-per-event of the process list are gone.
- **Presence.** Each page sends `Heartbeat{tab_id, kind, id}` (the active tab) every 10 s and `Leave` on exit; the hub
  keeps an in-memory map with a 30 s TTL and sends `presence.snapshot` on connect, then `joined | moved | left`
  (live only, not numbered). The web shows who else is on a tab (initials on the tab and in the editor corner) and the
  people online (header). No Yjs: the platform's conflicts are settled by changes and merges (ADR 0032), not by a CRDT.
- **Several `events` replicas.** Presence is relayed over core NATS on `presence.beat|leave|hello` (outside `goap.>`,
  so the GOAP stream does not persist it); each replica expires tabs by TTL, and a replica that starts sends `hello`,
  which the others answer with the tabs connected to them. Event sequence numbers stay per replica: a client that
  reconnects to another one gets `resync`.
- Not done: cursors / selections.
