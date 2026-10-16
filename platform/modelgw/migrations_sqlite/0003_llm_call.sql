-- The ledger of LLM calls (ADR 0089): one row per call the gateway served or refused, whoever asked. No prompt, no
-- answer: the exchange of an engine call stays in the change log. at_ms is the time in Unix milliseconds (UTC), so a
-- day or an hour is an integer division in both dialects; step and call_index are -1 for a call of no process.
CREATE TABLE llm_call (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    at_ms           INTEGER NOT NULL,
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    subject         TEXT    NOT NULL DEFAULT '',
    project         TEXT    NOT NULL DEFAULT '',
    org             TEXT    NOT NULL DEFAULT '',
    alias           TEXT    NOT NULL DEFAULT '',
    provider        TEXT    NOT NULL DEFAULT '',
    model           TEXT    NOT NULL DEFAULT '',
    kind            TEXT    NOT NULL DEFAULT 'complete',
    input_tokens    INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    error           TEXT    NOT NULL DEFAULT '',
    source          TEXT    NOT NULL DEFAULT 'other',
    conversation_id TEXT    NOT NULL DEFAULT '',
    process_id      TEXT    NOT NULL DEFAULT '',
    change_id       TEXT    NOT NULL DEFAULT '',
    step            INTEGER NOT NULL DEFAULT -1,
    action          TEXT    NOT NULL DEFAULT '',
    agent           TEXT    NOT NULL DEFAULT '',
    call_index      INTEGER NOT NULL DEFAULT -1
);
CREATE INDEX llm_call_subject_at ON llm_call (subject, at_ms);
CREATE INDEX llm_call_source_at ON llm_call (source, at_ms);
CREATE INDEX llm_call_model_at ON llm_call (model, at_ms);
CREATE INDEX llm_call_process ON llm_call (process_id);
CREATE INDEX llm_call_at ON llm_call (at_ms);
