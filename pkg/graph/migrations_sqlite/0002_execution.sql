-- Execution journal of the changes (ADR 0011).
CREATE TABLE execution (
    id         TEXT PRIMARY KEY,
    change_id  TEXT    NOT NULL REFERENCES change_set(id),
    process_id TEXT    NOT NULL,
    seq        INTEGER NOT NULL,
    kind       TEXT    NOT NULL,
    action     TEXT    NOT NULL DEFAULT '',
    started_at TEXT    NOT NULL,
    payload    TEXT    NOT NULL
);
CREATE INDEX execution_change ON execution (change_id);
CREATE INDEX execution_process ON execution (process_id);
