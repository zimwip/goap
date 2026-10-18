# ADR 0136 — Each service emits its own events, through an outbox

**Status**: accepted, done · **Date**: 2026-10 · Builds on ADR 0026 (node index), 0036 §3 (transverse
methodologies), 0053 (one event stream), 0106 (methodologies integrating methodologies), 0110 (reactive activities),
0124 (engine replicas)

## Context

An inventory of the events of the platform (October 2026) found the event-driven approach sound in its vocabulary
(`contracts/events`, checked against the web) and fragile in its emission:

- **The events of a change are emitted by two callers, not by their owner.** The graph's RPC layer
  (`graphsvc.Handler.publish`) emits them in the distributed deployment, a decorator of the engine
  (`engine.EventingGraph`) and `goap-dev`'s `changePublisher` in the local one. Neither covers the same operations:
  `change.signal` is never emitted in the distributed deployment, `change.transitioned` and `change.committed` are
  never emitted in `goap-dev` when the engine passes a gate, so `release-based` is never woken there by the `sdlc`
  changes it integrates. `Apply` emits `change.applied` and never `change.committed`.
- **No event is guaranteed.** Every publisher publishes after its commit and drops the error (`_ = Publish`): a broker
  down or a cancelled request context leaves the write and loses its event, with nothing to replay it.
- **The engine's events are not durable for the engine itself.** The triggers and the companions read
  `goap.process.>` through core NATS: lost during a restart, handled once by each replica.
- **The other services emit nothing.** The credentials service starts and ends sessions, sets passwords; the hub
  registers connectors and sees them go; the conversation service appends messages (the web polls it, ADR 0085); the
  preferences service saves a document; the model gateway refuses a call over quota. None of it reaches the bus.
- Some subjects carry no event (`goap.change.<id>.flow_opened`, `option_*`, `decision_*`, `question_answered`: a flow
  or a decision, read as an empty `ChangeEvent` by the trigger consumer and the web hub).

## Decision

### 1. The owner of a fact emits its event

An event says that a fact of a service's state happened; it is emitted by the service that owns that state, at the
place the state is written, whoever asked for the write:

| Owner | Events |
|---|---|
| graph (`graph/graph`) | the changes (`change.created`, `edited`, `attached`, `detached`, `item_added`, `signal`, `committed`, `transitioned`, `applied`, `purged`), the technical `change.updated` / `changeindex`, the nodes, the baselines |
| domain registry (`graph/domainsvc`) | `goap.registry.domain.saved` / `published` / `deleted` |
| methodology registry (`platform/registrysvc`) | `goap.registry.methodology.saved` / `published` / `deleted`; `suspended` / `resumed` are recomputed, not stored (§4) |
| engine (`engine/engine`) | the processes (`started`, `attached`, `step`, `step_completed`, `waiting`, `stuck`, `completed`, `failed`, `cancelled`, `superseded`, `log`) |
| credentials (`gateway/credsvc`) | `goap.credentials.<subject>.registered`, `password_changed`, `session_started`, `session_ended` (one per session, `everywhere` ends them all) |
| preferences (`platform/prefssvc`) | `goap.preferences.<subject>.updated` (the keys changed, never the values), `reset` |
| conversations (`platform/convsvc`) | `goap.conversation.<id>.created`, `renamed`, `deleted`, `message_appended`, `message_updated`, `action_updated`, `summarized` (the end of the web's polling) |
| hub (`mcp/mcpsvc`) | `goap.mcp.connector.<id>.registered`, `lost` (its liveness expired), `back` |
| model gateway (`platform/modelgw`) | `goap.model.call.<seq>.recorded` (a ledger row: subject, end user, source, alias, model, tokens, outcome; never the exchange), `goap.model.quota.<subject>.exceeded` |

No caller emits an event on behalf of a service: `graphsvc.Handler.publish`, `engine.EventingGraph` and
`goap-dev`'s `changePublisher` go. An event never carries a secret, a password hash, a token, a prompt or a value of
the preferences; it names what changed and who did it (`actor`, `endUser` of ADR 0114, `command` of ADR 0053), and a
reader who needs more asks the owner.

The subjects that carry no event (`flow_*`, `option_*`, `decision_*`, `question_answered`) are no longer published:
the web follows them through `change.updated`, a methodology through `change.item_added` (decision points are items).

### 2. Every event goes through an outbox

A service writes its events in an outbox table of its own database **in the transaction that writes the fact**, and a
relay publishes them afterwards. The outbox is generic (`lib/outbox`):

- table `event_outbox (id BIGINT identity / INTEGER PRIMARY KEY, subject, payload, headers, created_at,
  published_at)`, in both dialects of each service (`migrations/`, `migrations_sqlite/`, `TestSchemasAligned`);
- `outbox.Append(ctx, tx, events...)` takes the service's own transaction (`*sql.Tx`), never opens one;
- `outbox.Relay` reads the unpublished rows in `id` order, publishes each to the bus with `Nats-Msg-Id`
  `<service>:<id>` (JetStream deduplicates a row published twice within its window), marks it published, and is woken
  right after a commit (in process) or by its poll interval (`GOAP_OUTBOX_POLL`, 1 s); one relay at a time per
  outbox (`Relay.Lease`, a lease shared by the replicas). Published rows are deleted after
  `GOAP_OUTBOX_RETENTION` (24 h). A row that cannot be published stays and is retried with a backoff: the order of a
  service's events is kept.
- The headers keep what the publishers put on a message today (trace context, actor, command), captured from the
  context at `Append`.

The engine's outbox is `process_outbox` (its tables live in the database it may share with others in the local
mode; `outbox.SQL.Table`), written by `EventStore.PutEmitting` with the process; the replicas sharing it deliver one at
a time (`outbox.Relay.Lease`, the engine's shared lock `outbox` of ADR 0124); concurrent writes of different
processes may reach the bus in the order their transactions committed rather than their rank, the order of one
process's events is kept.

In the local mode every service shares one SQLite database, so each outbox has a table of its own (`outbox.SQL.Table`):
`event_outbox` (graph), `process_outbox` (engine), `domain_outbox` (domain registry), `credentials_outbox`,
`preferences_outbox`, `conversation_outbox`, `mcp_outbox` (hub), `model_outbox` (model gateway). A composition starts
each relay with `outbox.Start` (the poll and the retention from the environment); a service given no sink (no bus)
writes no event.

The methodology registry keeps its facts in the graph (`registrysvc.GraphStore`, ADR 0023), not in a database of its
own: its events **ride on the graph commit** that stores the version (`graphapi.Commit.Events`, proto
`CommitEditsRequest.events`, a `CarriedEvent{subject, payload}`), written in the graph's outbox in the landing
transaction, after its `change.applied`, and delivered by the graph's relay. The graph takes carried events from a
platform service only (`system:` principal; the registry commits as `system:registry` for the person it acts for), on
a `goap.` subject outside the graph's own families (`goap.change.`, `goap.changed.`, `goap.changeindex.`,
`goap.node.`, `goap.baseline.`), with a JSON payload; a write the graph holds already (`ErrNothingToCommit`) emits
nothing. The memory store of the registry's tests has no graph: the service publishes there itself.

The hub's `lost` is a fact it records: a connector whose lease ran out is marked lost (`connector.lost_ms`, both
dialects) with its event in one transaction by `Service.Sweep` (`Service.Watch`, a fifth of the lease), one replica
winning; a registration after it is `back`.

`goap-dev` runs the same relays over its in-process bus (`eventsvc.Hub` and the trigger manager): the local mode has
the same events, from the same places, as the distributed one. A memory store (graph tests, `graph.NewMemory`) keeps
its outbox in memory with the same semantics.

### 3. Consumers are durable and idempotent

- Every consumer that acts on an event (the trigger manager, the companions and relays of the engine, the indexer,
  the registries' reloads) reads it through a durable JetStream consumer shared by the replicas of its service, and
  acknowledges it only once handled; a failed handling is redelivered.
- The trigger manager and the companions read `goap.process.>` through a durable consumer too, so a process event is
  handled once across the engine replicas and survives a restart; the broker in memory stays for the live views.
- An event carries its outbox id (`<service>:<id>`), so a handler that may see it twice (redelivery) can tell.
- The trigger safeguard against loops stays (a trigger never reacts to its own output); the rate limit becomes per
  trigger **and change**, so two changes created within two seconds both fire it.
- Core NATS stays for what is a hint, not a fact: `engine.wake` / `engine.interrupt`, presence.

### 4. What is not a stored fact

The log lines of a run (`goap.process.<id>.log`) are a live display nothing keeps: the engine publishes them directly
(`Engine.Events`), outside the outbox.

An event computed from the state rather than written with it (`methodology.suspended` / `resumed`, recomputed from the
domains in force at each start and on each domain event) has no outbox row: it is emitted when computed, and its
consumers read the state again on a resync.

## Steps

1. (done) `lib/outbox`; the graph writes all its events (§1) in its outbox, the handler and `EventingGraph` stop emitting;
   the dead subjects go.
2. (done) The engine: its process events in the outbox of the process store (`enginesvc.SQLStore`, same transaction as the
   `Put`), durable consumers for the triggers and the companions, the rate limit per change.
3. (done) The domain registry (graph database, `domain_outbox`) and the methodology registry (its events ride on the
   graph write that stores the version).
4. (done) Credentials, preferences, conversations (the event stream delivers a conversation's events to its owner, the
   web reloads on them and keeps a slow poll only as a fallback), hub, model gateway.
5. (done) `docs/architecture.md` §2.11 and §3.1, `docs/architecture-overview.html`.

## Consequences

- An event is emitted when, and only when, its fact is committed: no lost event, no event without its fact, the same
  events in every deployment.
- A new write path of a service emits its events without anyone remembering to.
- An event can be published twice (a relay stopped between publish and mark); JetStream's deduplication absorbs it
  within its window, and the consumers are idempotent beyond it.
- Each service database gains a table, and each service a relay goroutine.
