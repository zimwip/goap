-- Assistant conversations (ADR 0085): per user, kept outside the graph. Times are unix microseconds.
CREATE TABLE conversation (
    id         TEXT    PRIMARY KEY,
    subject    TEXT    NOT NULL,
    title      TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX conversation_subject_updated ON conversation (subject, updated_at, id);

CREATE TABLE conversation_message (
    id              TEXT    PRIMARY KEY,
    conversation_id TEXT    NOT NULL REFERENCES conversation (id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    role            TEXT    NOT NULL,
    body            TEXT    NOT NULL DEFAULT '',
    actions         TEXT    NOT NULL DEFAULT '[]',
    process_id      TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'done',
    error           TEXT    NOT NULL DEFAULT '',
    context         TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    UNIQUE (conversation_id, seq)
);
