-- ADR 0030: one log per change. The facts of the blackboard, the records of the execution journal and the events of
-- the change impacts (ADR 0029) are entries of the same insert-only log, in one order (seq). What an entry is (type),
-- its flow branch, process, action run and subject are columns, to filter on them directly; payload is the whole entry.
-- change_impact stays, as the projection of the impact events.
CREATE TABLE change_log (
    seq        bigserial   PRIMARY KEY,
    id         text        NOT NULL UNIQUE,
    change_id  uuid        NOT NULL REFERENCES change(id),
    -- <stream>.<kind>: fact.artifact, journal.schedule, impact.written...
    type       text        NOT NULL,
    flow       text        NOT NULL DEFAULT '',
    process_id text        NOT NULL DEFAULT '',
    execution  text        NOT NULL DEFAULT '',
    subject    text        NOT NULL DEFAULT '',
    by_whom    text        NOT NULL DEFAULT '',
    at         timestamptz NOT NULL,
    payload    jsonb       NOT NULL
);
CREATE INDEX change_log_change ON change_log (change_id, seq);
CREATE INDEX change_log_type ON change_log (change_id, type, seq);
CREATE INDEX change_log_flow ON change_log (change_id, flow, seq);
CREATE INDEX change_log_process ON change_log (process_id, seq);
CREATE INDEX change_log_execution ON change_log (execution);

DROP TABLE change_item;
DROP TABLE execution;
