# ADR 0180 — A log entry holds what moves: payloads the fold cannot derive, encoded in CBOR with a dictionary

**Status**: accepted, §1 and §2 built · **Date**: 2026-11 · **Builds on**: ADR 0029 (impacts are event-sourced), ADR 0030 (one log per
change), ADR 0105 addendum (a review is folded from its deltas), ADR 0128 (domains declare their item types), ADR 0142
(a payload leaves its envelope), ADR 0144 (one entry per commit), ADR 0174 (the version is the record of an impact)

## Context

The change log is the largest table of the graph and grows with every write. On the lifecycle bench at 150K versions
(482K entries) `change_log` holds 286 MB, of which its payloads are 191 MB (`pg_column_size`), and 98 MB of indexes.

Measured offline on that log (read only, the encoder and the decoder of each variant in Go):

| Payloads | Whole | What moves only (§1) |
|---|---|---|
| `jsonb` (today) | 200 MB | — |
| JSON | 163 MB | 110 MB |
| CBOR | 141 MB | 95 MB |
| CBOR, UUIDs as 16 bytes | 114 MB | 76 MB |
| same, zstd with a dictionary per entry type | 50 MB | **36 MB** |
| same, keys as integers (a static schema, protobuf-like) | 46 MB | 34 MB |

Three remarks settle the design:

- **Most of a payload repeats what something else says.** The time of the entry (`createdAt` = `at`), its author
  (`producedBy`, a review's `by` = `by_whom`), what the entry type implies (`op`, `review: proposed` of a proposal,
  `intent: created` of a creation), what the version an impact starts from gives (the `key` and `type` of a modification),
  defaults (`post: {version: 0}` of a draft, an empty `fromBase`), the type and status of an item the entry type names.
  Removing them halves the log whatever the encoding.
- **A static schema does not fit the general case.** Items are declared by domains (ADR 0128), may be added on the fly,
  and keep free keys beside their attributes; a schema built from the declarations would tie reading the history to the
  registry forever. A self-describing encoding works the same for every item.
- **Entries are small** (230 bytes for an `updated`, 1.3 KB for a `created`), under the 2 KB PostgreSQL starts to
  compress at: nothing is compressed today. On such entries a dictionary trained per entry type takes what repeats (key
  names, types, structure ids) and gets within 10 % of a static schema.

## Decision

### 1. An entry holds only what the fold cannot derive

A payload carries what moves; whatever its envelope (ADR 0142: id, change, impact, flow, execution, by, at, causation),
its entry type, the version it starts from or a default gives is not written, and the fold puts it back
(`domain.ImpactEventsOf`, the web's `decodeImpactEvents`). A general rule, like the deltas of a review (ADR 0105
addendum): a new entry type writes the delta of its gesture and nothing its reader can find elsewhere.

- **The node belongs to the impact.** Every impact entry names its impact (`subject`); the entry that declares the impact
  (`proposed`, `created`) names its node, the others do not repeat it. The column `change_log.node` and its index go.
  The changes acting on a node (`NodeChanges`) are read from its impacts: the committed ones from the versions of the
  node (each names its change and impact, ADR 0174), the open ones from the views of the open changes.
- **A review writes the defaults its entries share** (the outcome and the comment most of them have) on its delta, an
  entry what differs from them, often its impact alone (`ReviewEffectiveComment` already reads a comment so).

### 2. A payload is CBOR, compressed with a dictionary of its entry type

The payload column becomes bytes (`bytea` / `BLOB`): CBOR (RFC 8949, deterministic encoding), UUIDs as 16-byte strings
(tag 37), then a zstd frame. A dictionary is trained per entry type from entries already written, stored immutable
(`log_dictionary`: id, entry type, bytes) and named by the frame (its dictionary id); an entry type with none yet is
compressed without one, and a new dictionary applies to the entries written after it, never rewriting the others. No SQL
reads inside a payload today; none may.

Cost measured (fastest zstd level): 3 to 23 µs to encode an entry, 1.5 to 18 µs to decode it, CBOR included, against 2
to 16 µs for JSON; a write is counted in milliseconds.

## Consequences

- The log falls from about 286 to about 130 MB on the bench (payloads 200 → 36 MB, the `node` column and its 32 MB
  index gone); the gain grows with the history.
- Reading a payload needs the decoder of the graph: `psql` no longer shows it as JSON (a debugging function decodes it).
  The PROV-O export (ADR 0057) and `ListChangeLog` keep serving JSON.
- §1 can be built without §2, and is first: it is independent of the format.
- The floor of a review is 16 bytes per impact: a review of 1000 impacts is about 17 KB.

## §1 as built

- `change_log.node` and `change_log_node` are gone (migration `0039_log_names_no_node`); `Tx.NodeChangePage` reads the
  versions of the node (`node_version_pkey`: `change_id`, `min(log_seq)`), `Graph.NodeChanges` adds the open changes from
  their views (`draftState.firstOn`: the first event of an impact naming the node).
- A declaration (`proposed`, `created`) leaves out `createdAt` when it is the entry's time (the graph stamps the entry
  with it), `producedBy` when it is the entry's author, `review: proposed`, and `intent: created` on a creation
  (`statePayload`; `ImpactEventOf` and the web's `decodeLogEntry` put them back). Times are kept to the microsecond
  (`Graph.now`), the precision of the PostgreSQL column.
- A fact leaves its id, type, time and execution to the columns, and its flow, target and author when the columns hold
  them as is (`domain.FactEntry` / `domain.ItemOf`; the web's `decodeLogEntry`); every reader of a fact goes through them.
- A review delta writes what most of its entries set alike as `defaults` (`outcome`, `comment`, `by`), an entry what
  differs (`null` for a field it leaves unset), an entry with its impact alone as the impact's id (`ReviewDelta.Data`,
  `ReviewDeltaOf`, the web's `ReviewFold`).
- An edit (`updated`) leaves out its post, the draft reference of its impact's node: the fold of the view puts it back
  (`draftState.apply`).

Lifecycle bench at 150K versions, PostgreSQL, the same machine one run after the other:

| | Write rate | `change_log` (table + indexes) | Payloads | Database |
|---|---|---|---|---|
| main (reference) | 497 v/s | 403 MB | 200 MB | 761 MB |
| §1 | 516 v/s | 319 MB (−21 %) | 152 MB | 676 MB |

By type of entry (payloads): `proposed` 35 → 24 MB, `updated` 39 → 23 MB, `fact.review` 20 → 9 MB, `created` 62 → 54 MB.

## §2 as built

- The repositories encode and decode at their boundary (`graph/graph/logcodec.go`): `Tx.AppendLog` / `AppendLogs` take
  the entry's JSON and store its CBOR, `Tx.Log` gives the JSON back; the graph, its readers, `ListChangeLog`, PROV-O and
  the web see JSON only, the memory repository keeps it as is.
- The stored form starts with one byte: `0x01` CBOR, `0x02` a zstd frame (its four-byte magic left out), anything else
  JSON (the entries written before: migration `0040_log_payload_cbor` turns the PostgreSQL column into `bytea`
  holding their text; SQLite keeps its column, a blob stored as is). A payload is compressed only when that is shorter.
- CBOR, transcoded in one pass each way with no tree in between (`graph/graph/logcbor.go`): the order of the JSON kept
  (Go's encoder writes a value the same way every time), containers with their lengths, a UUID in its canonical
  lower-case form as tag 37 over 16 bytes (any other spelling stays text), an integer as an integer (a bignum beyond 64
  bits), other numbers as float64; back, the JSON Go's encoder writes, byte for byte. 0.8 µs to encode an entry, 0.55 µs
  to decode it (6.8 and 9.3 µs through a generic CBOR library and a tree, the first version). The entries that version
  wrote (keys sorted, floats shortened) read the same.
- `GOAP_LOG_COMPRESS=0` keeps the payloads as CBOR, uncompressed and with no dictionary trained
  (`Postgres.CompressLog` / `SQLite.CompressLog`); the compressed entries still read.
- Dictionaries: a process trains one for an entry type once it wrote `logTrainAt` entries of it without one (1000),
  a second after 50 000 more, none after; from the last 2000 entries of the type, 16 KiB, in the background
  (`logCodec.train`), stored in `log_dictionary` with its id (a sequence on PostgreSQL), the frames written after it
  naming that id. A reader meeting an id it does not know reads the dictionaries again (another process trained it).
  Nothing is rewritten: an entry keeps the dictionary it was written with.
- zstd at its fastest level, no checksum (the database keeps the rows).

Lifecycle bench at 150K versions, PostgreSQL, one run after the other (§1 run again for the rate):

| | Write rate | `change_log` (table + indexes) | Payloads | Database |
|---|---|---|---|---|
| main before §1 | 497 v/s | 403 MB | 200 MB | 761 MB |
| §1 | 453 v/s | 319 MB | 152 MB | 676 MB |
| §1 + §2 | 424 / 446 v/s | 197 MB (−51 % from main) | 38 MB | 553 MB |

Then, with the transcoder in one pass, one run after the other, the CPU of the graph profiled over 40 s:

| | Write rate | `change_log` | Database | Codec (CPU of the graph) |
|---|---|---|---|---|
| §1 | 422 v/s | 319 MB | 676 MB | — |
| §2, zstd | 446 v/s | 196 MB | 553 MB | 3.8 % (zstd 3.3 %, CBOR 0.3 %) |
| §2, `GOAP_LOG_COMPRESS=0` | 436 v/s | 246 MB | 604 MB | 0.35 % |

The rates are within the noise of the machine (4 cores shared by PostgreSQL, the graph and the bench); what zstd costs
is mostly the reset of its dictionary's tables at each entry.

By type of entry (payloads, §1 → §2): `created` 54 → 13 MB, `committed` 25 → 6.3 MB, `updated` 23 → 6.1 MB,
`proposed` 24 → 5.7 MB, `checkedOut` 16 → 3.8 MB, `fact.review` 9.3 → 3.3 MB. The write rate is within the noise of the
machine (§1 measured 516 then 453 v/s); the codec costs a few microseconds an entry. Ten dictionaries, 46 KB in all.
