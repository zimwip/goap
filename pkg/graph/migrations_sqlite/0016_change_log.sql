-- ADR 0030: one log per change. The facts of the blackboard, the records of the execution journal and the events of
-- the change impacts (ADR 0029) are entries of the same insert-only log, in one order (seq). What an entry is (type),
-- its flow branch, process, action run and subject are columns, to filter on them directly; payload is the whole entry.
-- change_impact stays, as the projection of the impact events.
CREATE TABLE change_log (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    id         TEXT NOT NULL UNIQUE,
    change_id  TEXT NOT NULL REFERENCES change(id),
    type       TEXT NOT NULL,
    flow       TEXT NOT NULL DEFAULT '',
    process_id TEXT NOT NULL DEFAULT '',
    execution  TEXT NOT NULL DEFAULT '',
    subject    TEXT NOT NULL DEFAULT '',
    by_whom    TEXT NOT NULL DEFAULT '',
    at         TEXT NOT NULL,
    payload    TEXT NOT NULL
);
CREATE INDEX change_log_change ON change_log (change_id, seq);
CREATE INDEX change_log_type ON change_log (change_id, type, seq);
CREATE INDEX change_log_flow ON change_log (change_id, flow, seq);
CREATE INDEX change_log_process ON change_log (process_id, seq);
CREATE INDEX change_log_execution ON change_log (execution);

DROP TABLE change_item;
DROP TABLE execution;
