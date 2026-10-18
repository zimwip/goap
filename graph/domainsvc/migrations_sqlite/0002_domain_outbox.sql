-- The outbox of the domain registry (ADR 0136): its events, written with the domain version they are about.
CREATE TABLE domain_outbox (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    subject      text        NOT NULL,
    payload      text        NOT NULL,
    headers      text        NOT NULL DEFAULT '{}',
    created_at   text        NOT NULL,
    published_at text
);
CREATE INDEX domain_outbox_pending ON domain_outbox (seq) WHERE published_at IS NULL;
