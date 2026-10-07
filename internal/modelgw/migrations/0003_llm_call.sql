-- The ledger of LLM calls (ADR 0089): one row per call the gateway served or refused, whoever asked. No prompt, no
-- answer: the exchange of an engine call stays in the change log. at_ms is the time in Unix milliseconds (UTC), so a
-- day or an hour is an integer division in both dialects; step and call_index are -1 for a call of no process.
CREATE TABLE llm_call (
    seq             bigserial PRIMARY KEY,
    at_ms           bigint  NOT NULL,
    duration_ms     bigint  NOT NULL DEFAULT 0,
    subject         text    NOT NULL DEFAULT '',
    project         text    NOT NULL DEFAULT '',
    org             text    NOT NULL DEFAULT '',
    alias           text    NOT NULL DEFAULT '',
    provider        text    NOT NULL DEFAULT '',
    model           text    NOT NULL DEFAULT '',
    kind            text    NOT NULL DEFAULT 'complete',
    input_tokens    bigint  NOT NULL DEFAULT 0,
    output_tokens   bigint  NOT NULL DEFAULT 0,
    error           text    NOT NULL DEFAULT '',
    source          text    NOT NULL DEFAULT 'other',
    conversation_id text    NOT NULL DEFAULT '',
    process_id      text    NOT NULL DEFAULT '',
    change_id       text    NOT NULL DEFAULT '',
    step            integer NOT NULL DEFAULT -1,
    action          text    NOT NULL DEFAULT '',
    agent           text    NOT NULL DEFAULT '',
    call_index      integer NOT NULL DEFAULT -1
);
CREATE INDEX llm_call_subject_at ON llm_call (subject, at_ms);
CREATE INDEX llm_call_source_at ON llm_call (source, at_ms);
CREATE INDEX llm_call_model_at ON llm_call (model, at_ms);
CREATE INDEX llm_call_process ON llm_call (process_id);
CREATE INDEX llm_call_at ON llm_call (at_ms);
