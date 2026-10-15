-- Append-only per-process log (ADR 0031): turn-by-turn state (e.g. the
-- intent/clarification dialogue) that would otherwise force a full rewrite
-- of the process row on every turn. Independent of any change.
CREATE TABLE process_log (
    seq        bigserial PRIMARY KEY,
    process_id text NOT NULL,
    type       text NOT NULL,
    payload    text,
    at         text NOT NULL
);
CREATE INDEX process_log_process ON process_log (process_id, seq);
