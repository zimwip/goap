-- Assistant conversations (ADR 0085): per user, kept outside the graph. Times are unix microseconds.
CREATE TABLE conversation (
    id         text   PRIMARY KEY,
    subject    text   NOT NULL,
    title      text   NOT NULL DEFAULT '',
    created_at bigint NOT NULL,
    updated_at bigint NOT NULL
);
CREATE INDEX conversation_subject_updated ON conversation (subject, updated_at, id);

CREATE TABLE conversation_message (
    id              text    PRIMARY KEY,
    conversation_id text    NOT NULL REFERENCES conversation (id) ON DELETE CASCADE,
    seq             integer NOT NULL,
    role            text    NOT NULL,
    body            text    NOT NULL DEFAULT '',
    actions         jsonb   NOT NULL DEFAULT '[]',
    process_id      text    NOT NULL DEFAULT '',
    status          text    NOT NULL DEFAULT 'done',
    error           text    NOT NULL DEFAULT '',
    context         text    NOT NULL DEFAULT '',
    created_at      bigint  NOT NULL,
    UNIQUE (conversation_id, seq)
);
