-- The outbox of the engine (ADR 0136): the events of a process, written with it, delivered by the relay.
CREATE TABLE process_outbox (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    subject      text        NOT NULL,
    payload      text        NOT NULL,
    headers      text        NOT NULL DEFAULT '{}',
    created_at   text        NOT NULL,
    published_at text
);
CREATE INDEX process_outbox_pending ON process_outbox (seq) WHERE published_at IS NULL;
