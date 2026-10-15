-- The rolling summary of the older part of a conversation, written by the assistant (compaction): the messages up to
-- up_to (a seq) are summarised in summary. Times are unix microseconds.
CREATE TABLE conversation_summary (
    conversation_id TEXT    PRIMARY KEY REFERENCES conversation (id) ON DELETE CASCADE,
    summary         TEXT    NOT NULL DEFAULT '',
    up_to           INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL
);
