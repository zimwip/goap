-- The outbox of the preferences (ADR 0136): the events of a document, written with it, delivered by the relay.
CREATE TABLE preferences_outbox (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    subject      text        NOT NULL,
    payload      text        NOT NULL,
    headers      text        NOT NULL DEFAULT '{}',
    created_at   text        NOT NULL,
    published_at text
);
CREATE INDEX preferences_outbox_pending ON preferences_outbox (seq) WHERE published_at IS NULL;
