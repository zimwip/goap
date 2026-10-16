-- Append-only per-process log (ADR 0031): turn-by-turn state (e.g. the
-- intent/clarification dialogue) that would otherwise force a full rewrite
-- of the process row on every turn. Independent of any change.
CREATE TABLE process_log (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    process_id TEXT NOT NULL,
    type       TEXT NOT NULL,
    payload    TEXT,
    at         TEXT NOT NULL
);
CREATE INDEX process_log_process ON process_log (process_id, seq);
