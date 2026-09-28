-- ADR 0029: the change impacts are event-sourced. The log is insert-only; change_impact is its projection.
CREATE TABLE change_event (
    id         uuid PRIMARY KEY,
    change_id  uuid        NOT NULL REFERENCES change(id),
    seq        int         NOT NULL,
    impact_id  text        NOT NULL DEFAULT '',
    op         text        NOT NULL,
    flow       text        NOT NULL DEFAULT '',
    execution  text        NOT NULL DEFAULT '',
    by_whom    text        NOT NULL DEFAULT '',
    -- the whole event (domain.ImpactEvent) as JSON
    payload    jsonb       NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (change_id, seq)
);
CREATE INDEX change_event_impact ON change_event (change_id, impact_id);
