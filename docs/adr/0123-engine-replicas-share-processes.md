# ADR 0123 — Engine replicas sharing the processes

**Status**: accepted, implemented (steps 1 to 3) · **Date**: 2026-10 · Builds on ADR 0010 (local SQLite mode), ADR 0031
(trigger leadership across replicas); replaces the "engine clustering" target of `docs/architecture.md` §3.2

## Context

Several engines must be able to join a pool and execute the runs. The triggers were ready for it (a durable JetStream
consumer shared by the replicas, a KV lease for the cron, ADR 0031), the runs were not:

- `cmd/engine` kept its processes in memory (`NewMemoryStore`, "PostgreSQL store: milestone M1"): a replica did not see
  the runs of another;
- the work on a process was serialized by mutexes of the process (`Engine.lock`, the `companions` and `instances`
  mutexes): two replicas could run one process at once, and two events of one change handled by two replicas could
  start two companion runs (a check then a create);
- `Cancel` and a supersede cancelled the run of the replica that received them only;
- a run left by a replica that stopped was lost (`SQLiteStore.Interrupted` failed it at the next start).

Exactly-once execution does not exist across machines. The aim is one executor at a time per process, a write of an
executor that lost its turn refused, and a run left by a stopped replica resumed.

## Decision

1. **One lock service, shared** (`engine.Locker`: `Acquire(key, ttl)` takes or renews a lease and returns what another
   replica asked of the holder, `Release`, `Interrupt(key, by)`), set on `Engine.Locks`. `Engine.lock(key)` keeps its
   mutex of the process and, with `Locks` set, takes the shared lock too (`Engine.hold`): it waits while another replica
   holds it, renews it every tenth of its lease (`Engine.LockTTL`, default 30 s, `GOAP_ENGINE_LOCK_TTL`) and releases
   it. Every section serialized before is serialized across the replicas with no change of its code: a process
   (its id), a supersede (`supersede:<id>`), the companions of a change (`companions:<change>`, which was one mutex for
   all changes), the instance of a persistent agent (`instance:<key>`, idem).
2. **Fencing.** The store refuses a write to a process whose lock another replica holds (`engine.ErrFenced`): a
   replica that could not renew its lease in time (a pause, a partition) and was taken over cannot overwrite the new
   holder's state. A lease lost at renewal cancels the local run.
3. **Interruptions reach the holder.** `Cancel` and a supersede cancel the local run and ask the holder through the
   lock (`Engine.interrupt`); the holder sees it at its next renewal, marks the process as being cancelled and cancels
   its run, which lets go without failing (as a local cancellation did). Latency: a tenth of the lease.
4. **Recovery.** `Engine.Recover(ctx, every)` resumes the orphans (`engine.OrphanFinder.Orphans(before)`: running
   processes written before an instant whose lock nobody holds): at once for those written before the start, then
   every interval for those idle for a lease. A sub-process its parent does not know yet (the parent's step was cut
   before recording it) is failed: the parent, resumed, runs its step again. `Interrupted` is gone.
5. **The store.** `internal/enginesvc.SQLStore` (was `SQLiteStore`) is the store, the lock service and the orphan
   finder, on SQLite (`migrations_sqlite/`, the local mode) and PostgreSQL (`migrations/`, `Dollar`), aligned
   (`TestSchemasAligned`): table `engine_lock (key, holder, lease_until, interrupt)`, `process.updated_at` (leases and
   instants in unix milliseconds: comparable in both dialects). `Holder` names the replica (`NewHolder`: host, pid, a
   random part); the clocks of the replicas must agree to well under a lease.
6. **Wiring.** `cmd/engine` uses PostgreSQL when `GOAP_DB_DSN` is set (memory otherwise, one replica) and then sets
   `Locks` and runs `Recover`. `goap-dev` with `GOAP_STORE=sqlite` does the same on its file and drops the locks the
   previous start left (`ReleaseAll`, the local mode being the one process on its file), so its runs left running are
   resumed at once instead of failed.
7. **Pub/sub is a signal, not the arbiter.** JetStream delivers at least once: who executes is decided by the lock,
   never by the reception of a message. The event triggers keep their competing consumers (ADR 0031).

## Consequences

- A run is resumed from its last saved state: the step in flight when a replica stopped runs again. Conditions being
  derived from the change (ADR 0109), a step whose effects landed is not repeated by the planner; one cut halfway runs
  again (its model calls are paid twice, the ledger shows both). Idempotency keys on the graph writes and the tool
  calls of a step are the next step (below).
- A replica runs what it was asked to run (an RPC, an event it consumed, a schedule); the others take over only what
  is left. The load is spread by the load balancer and the competing consumers, not by a work queue.
- Every lock is a database write (and a renewal every few seconds while held): cheap at the scale of runs whose steps
  take seconds; the in-process mutex stays the fast path within a replica.
- `Store.Put` outside the lock of a process from another replica than its holder is refused where it used to race.

## Not done (next steps)

- **Idempotency keys** `(process, execution, step)` on the change operations of a step and in `InvokeRequest` for the
  connectors that can honour them, so that a step replayed after a recovery does not write twice.
- **A work queue** (`SKIP LOCKED` on runnable processes, a NATS wake-up signal) to spread the runs by capacity rather
  than by where they were asked.
- **Immediate interruption** through a NATS message (`goap.engine.cancel.<id>`) instead of the renewal tick.
- Virtual actors (protoactor-go) only if the latency of the database locks becomes the limit: they would replace the
  locks' placement, not the fencing nor the persistence.
