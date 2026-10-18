-- The outbox of the conversations (ADR 0136): the events of a conversation, written with it, delivered by the relay.
CREATE TABLE conversation_outbox (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    subject      text        NOT NULL,
    payload      text        NOT NULL,
    headers      text        NOT NULL DEFAULT '{}',
    created_at   text        NOT NULL,
    published_at text
);
CREATE INDEX conversation_outbox_pending ON conversation_outbox (seq) WHERE published_at IS NULL;
