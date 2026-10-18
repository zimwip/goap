-- The outbox of the domain registry (ADR 0136): its events, written with the domain version they are about.
CREATE TABLE domain_outbox (
    seq          bigserial   PRIMARY KEY,
    subject      text        NOT NULL,
    payload      jsonb       NOT NULL,
    headers      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX domain_outbox_pending ON domain_outbox (seq) WHERE published_at IS NULL;
