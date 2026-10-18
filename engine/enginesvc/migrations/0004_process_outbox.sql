-- The outbox of the engine (ADR 0136): the events of a process, written with it, delivered by the relay.
CREATE TABLE process_outbox (
    seq          bigserial   PRIMARY KEY,
    subject      text        NOT NULL,
    payload      jsonb       NOT NULL,
    headers      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX process_outbox_pending ON process_outbox (seq) WHERE published_at IS NULL;
