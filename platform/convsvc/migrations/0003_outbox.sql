-- The outbox of the conversations (ADR 0136): the events of a conversation, written with it, delivered by the relay.
CREATE TABLE conversation_outbox (
    seq          bigserial   PRIMARY KEY,
    subject      text        NOT NULL,
    payload      jsonb       NOT NULL,
    headers      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX conversation_outbox_pending ON conversation_outbox (seq) WHERE published_at IS NULL;
