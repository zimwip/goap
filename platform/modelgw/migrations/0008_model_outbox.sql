-- The outbox of the model gateway (ADR 0136): the events of the ledger of calls, written with their row, delivered by
-- the relay.
CREATE TABLE model_outbox (
    seq          bigserial   PRIMARY KEY,
    subject      text        NOT NULL,
    payload      jsonb       NOT NULL,
    headers      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX model_outbox_pending ON model_outbox (seq) WHERE published_at IS NULL;
