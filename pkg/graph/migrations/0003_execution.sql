-- Execution journal of the changes (ADR 0011): ticks, action executions,
-- model / tool calls and human decisions of the processes working on a change,
-- in recording order (n).
CREATE TABLE execution (
    n          bigserial,
    id         uuid PRIMARY KEY,
    change_id  uuid        NOT NULL REFERENCES change_set(id),
    process_id text        NOT NULL,
    seq        int         NOT NULL,
    kind       text        NOT NULL,
    action     text        NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL,
    payload    jsonb       NOT NULL
);
CREATE INDEX execution_change ON execution (change_id, n);
CREATE INDEX execution_process ON execution (process_id, n);
