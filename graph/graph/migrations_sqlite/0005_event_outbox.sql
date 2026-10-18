-- the events of the graph, written in the transaction of their fact and published by the relay (ADR 0136)
CREATE TABLE event_outbox (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    subject      text        NOT NULL,
    payload      text        NOT NULL,
    headers      text        NOT NULL DEFAULT '{}',
    created_at   text        NOT NULL,
    published_at text
);
CREATE INDEX event_outbox_pending ON event_outbox (seq) WHERE published_at IS NULL;
