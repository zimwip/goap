# ADR 0030 — One log per change

**Status**: accepted, implemented · **Date**: 2026-09 · Extends ADR 0011 (execution journal), ADR 0017 (the blackboard
as a log), ADR 0029 (event-sourced change impacts). Replaces their three separate stores.

## Context
What happens to a change was recorded in three places, each append-only but with its own table and its own order:
the facts of the blackboard (`change_item`, ADR 0017), the execution journal with the scheduling of the runs
(`execution`, ADR 0011) and the events of the change impacts (`change_event`, ADR 0029). The flow branch of a fact or
of a journal record was inside its JSON. So:

- there was no single order: the audit of a change merged three lists by timestamp, and entries of the same instant
  (an action, the facts and the impact events it produced) had no reliable order;
- filtering on what matters for an audit (the kind of entry, the flow branch, the process, the action run) meant
  reading everything and filtering in memory.

## Decision
A change has **one log**: every fact, journal record and impact event is an entry of the same insert-only table,
`change_log`, in one order.

### 1. The table
| column | |
|---|---|
| `seq` | the position in the log (one sequence for every change: the order within a change is total) |
| `id` | the id of the fact, record or event |
| `change_id` | |
| `type` | `<stream>.<kind>`: `fact.artifact`, `fact.decision`, `fact.flow`…, `journal.process.started`, `journal.schedule`, `journal.tick`, `journal.action`, `journal.approval`, `journal.process.ended`, `impact.declared`, `impact.written`… |
| `flow` | the flow branch (`''` = the main flow); a flow event belongs to the flow it opens, adopts or discards |
| `process_id` | the process of a journal record |
| `execution` | the action run behind the entry (a journal record is its own) |
| `subject` | what it is about: the action, the fact type, the change impact, the flow operation |
| `by_whom` | the principal, the agent or the component |
| `at` | when |
| `payload` | the whole fact, record or event (JSON) |

Indexed by change and position, by change and type, by change and flow, by process, by action run. Nothing updates or
deletes an entry. `change_item`, `execution` and `change_event` are dropped (the platform starts from an empty store:
no data to move). `change_impact` stays, as the projection of the impact events (ADR 0029).

### 2. Storage
The repositories store entries: `Tx.AppendLog`, `Tx.Log(filter)`, `Tx.LogCounts(filter)` (entries per type, counted
by the store). Facts, journal records and impact events are written and read through them (`pkg/graph/changelog.go`);
their domain types are unchanged. `domain.LogFilter` selects on the columns: types (exact, or a stream as a prefix
ending with a dot: `journal.`), flows (`''` for the main flow), processes, action run, position (`AfterSeq`, to follow
the log) and a limit. The SQL repositories build the `WHERE` clause from it; the memory repository applies
`LogFilter.Match`.

### 3. Reading the log
`ListChangeLog(change, types, flows, processes, execution, after_seq, limit)` returns the entries (the payload as
JSON) and the number of entries of each type matching the request without its types, so a client filtering on types
still shows what each type would add. The **Audit** pane of the change reads the log in its order and sends its
filters (sources, flow, action run) to the server; it reads the action runs and the flow events of the whole change
too, to name the runs and draw the flow branches whatever the filters.

## Consequences
- **Order**: an audit is the log in its order, not a merge by timestamp.
- **Filtering**: by type, flow, process and action run in the store, without reading the payloads.
- **Compatibility**: `Change.Items`, `Graph.Journal`, `ListExecutions` and `ListChangeEvents` read the same log.
  Journal records read back from the memory repository are decoded from JSON, as they always were from SQL (numbers of
  `Data` are floats).
- **Migrations**: PostgreSQL `0017_change_log`, SQLite `0016_change_log` (they replace the `change_event` migration of
  ADR 0029, which never left its branch).

**Note (ADR 0037)**: a change that landed nothing can be purged with its whole log (`Graph.PurgeChange`); nothing that is part of the graph is ever deleted.
