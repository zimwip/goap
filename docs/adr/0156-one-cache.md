# ADR 0156 — One cache

**Status**: accepted; implemented · **Date**: 2026-10

## Context

The services kept what they computed between calls in caches each written their own way. The graph had five:

- the views of the changes, bounded in bytes and time;
- the states and headers of baselines, a FIFO by count;
- the validations of the impacts, emptied at random;
- the links of versions, two generations of maps.

The registry dropped its compiled methodologies all at once when full, and its client kept them forever. A domain
republish added a new set to the client each time, which was a leak. The scripts cache emptied itself when full. The
gateway kept ended sessions forever and swept the others past 10000.

A cache is extra logic to reason about. Each of these needed its own reading to know how much memory it could hold and
for how long.

## Decision

1. **One cache type**, `lib/cache` (`Cache[K, V]`). Its bounds are `Bounds{Bytes, TTL}`:
   - it holds at most `Bytes` of entries, by the size function it is given (an estimate, plus `EntryBytes` per entry);
   - an entry no one has read (or written) for `TTL` goes;
   - when room is needed, the least recently read entry goes first;
   - a value larger than the whole cache is not kept.

   It offers `Get`, `Put`, `Update` (a read and a write under its lock: "keep only a newer view"), `Remove`, `Clear`,
   `Len` and `Bytes`. A value is handed out as stored, so a caller that changes what it reads copies it.
   `cache.Of` and the constants `WordBytes`, `SlotBytes` and `StructBytes` are the shared estimates.
2. **Every cache of the platform is one.** The bounds are configurable where they were before:

   | Cache | Bytes | TTL | Configured by |
   |---|---|---|---|
   | Graph: views of changes | 64 MiB | 5 min | `Graph.Caches.Drafts` |
   | Graph: version links | 32 MiB | 5 min | `Graph.Caches.Versions` |
   | Graph: baseline states | 64 MiB | 5 min | `Graph.Caches.States` |
   | Graph: baseline headers | 4 MiB | 5 min | `Graph.Caches.Headers` |
   | Graph: impact validations | 16 MiB | 5 min | `Graph.Caches.Validations` |
   | Compiled methodologies (registry and its client) | 64 MiB | 10 min | `registrysvc.CompiledBounds` |
   | Compiled scripts (`lib/dsl`) | 32 MiB | 10 min | — |
   | Gateway sessions | 4 MiB | the longest session | — |

   - For the graph, a zero field of `Graph.Caches` takes the value in `DefaultCaches`. This replaces `DraftCacheBytes`,
     `DraftCacheTTL` and `VersionCacheSize`. The bounds are read at each write, so `Caches` may be set after `New`.
   - A compiled methodology is estimated at eight times the size of its definition. A compiled script is estimated at
     four times its source.
   - The gateway still checks an open session again after `SessionCheckTTL`, and an ended session still never reopens.
     Only an entry no request has named for the longest session is forgotten.
3. **A new cache is a `lib/cache`.** It names its bounds and a size function. Nothing else keeps data between calls
   without bounds in bytes and time.

What is not a cache stays as it is:

- The heads that `graphsnap.Reader` follows: one per namespace read, moved forward by deltas. They are a state, not a
  copy, and dropping one would cost a whole read.
- The single-slot views built over those heads.
- The maps of locks and presence.

## Consequences

- One reading tells what a process can hold. The figures are estimates, which err high.
- An entry read often stays. An entry not read for the TTL leaves even when there is room.
- The registry client no longer grows with each domain republish.
- The validations of a change no longer go at random.
