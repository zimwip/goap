-- The events of the hub (ADR 0136): a connector reported lost keeps the time of the report (0: live or not reported
-- since its last registration), and the outbox holds the events written with the registrations, delivered by the relay.
ALTER TABLE connector ADD COLUMN lost_ms bigint NOT NULL DEFAULT 0;
CREATE TABLE mcp_outbox (
    seq          bigserial   PRIMARY KEY,
    subject      text        NOT NULL,
    payload      jsonb       NOT NULL,
    headers      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX mcp_outbox_pending ON mcp_outbox (seq) WHERE published_at IS NULL;
