# ADR 0169 — A service waits for another by its announcement

**Status**: accepted, implemented (Go and Rust) · **Date**: 2026-10

## Context

At their start several services need another one: the graphs need the domain registry's built-in domains, the
engine its type catalogue, the methodology registry the platform graph to seed its methodologies, the registry and the
guardian both to check the methodologies against the domains in force. Each tried in a loop whose delay doubled up
to a minute (the Rust graph every second, sixty times), so a service started a few seconds before the one it needs
could wait close to a minute after that one was there. `goap-local` hides it by starting a service once those it names
answer `/readyz`; compose and a cluster start them in any order.

## Decision

1. **A request, then listen.** A service waiting for another tries once at start; while it fails it listens on NATS
   and tries again as soon as the service it needs announces it serves, the doubling delay (from a second up to
   `platform.AwaitMaxDelay`, 30 s) kept only as the fallback of an announcement lost or never made (no bus).
   `platform.Events.Await(ctx, subjects, try, retry)` subscribes before the first try, so an announcement made while it
   tries is not missed; a nil bus is the delays alone.
2. **The announcements.** Core NATS, outside the GOAP stream (at most once, nothing kept), published once the server
   listens (`platform.Server.Announce(events, subject)`; `Server.RunContext` binds its listener before serving):
   `service.ready.domains` (the domain registry, `domains` and `goap-domains-rs`, after publishing its built-in and
   shipped domains), `service.ready.graph.platform` (the platform graph, `graph` and `goap-graph-rs`; again once an
   in-process bootstrap is done) and `service.ready.graph.content` (a content graph). Payload `{"service": name}`.
3. **Who waits on what.** The Go graph's `loadTypes` (`service.ready.domains`, `goap.registry.domain.published`; the
   domain client invalidated before each try), the Rust graph's `Registry::wait_for_builtins` (`service.ready.domains`),
   the engine's type catalogue (`service.ready.domains`), the methodology registry's seed and `Recheck` and the
   guardian's `Recheck` (`service.ready.domains`, `service.ready.graph.platform`).

## Consequences

- A service is ready about as soon as the one it needs; the backoff remains for a bus that is down.
- A new start-time dependency waits with `Await` on the announcement of the service it needs, and a service others
  wait for announces itself with `Announce`.
- An announcement says only that the service listens; the try decides whether what is needed is there.
