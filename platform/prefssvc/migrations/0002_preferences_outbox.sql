-- The outbox of the preferences (ADR 0136): the events of a document, written with it, delivered by the relay.
CREATE TABLE preferences_outbox (
    seq          bigserial   PRIMARY KEY,
    subject      text        NOT NULL,
    payload      jsonb       NOT NULL,
    headers      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX preferences_outbox_pending ON preferences_outbox (seq) WHERE published_at IS NULL;
