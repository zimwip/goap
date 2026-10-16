-- The rolling summary of the older part of a conversation, written by the assistant (compaction): the messages up to
-- up_to (a seq) are summarised in summary. Times are unix microseconds.
CREATE TABLE conversation_summary (
    conversation_id text    PRIMARY KEY REFERENCES conversation (id) ON DELETE CASCADE,
    summary         text    NOT NULL DEFAULT '',
    up_to           integer NOT NULL DEFAULT 0,
    updated_at      bigint  NOT NULL
);
