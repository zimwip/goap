-- the events of the graph, written in the transaction of their fact and published by the relay (ADR 0136)
CREATE TABLE event_outbox (
    seq          bigserial   PRIMARY KEY,
    subject      text        NOT NULL,
    payload      jsonb       NOT NULL,
    headers      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX event_outbox_pending ON event_outbox (seq) WHERE published_at IS NULL;
