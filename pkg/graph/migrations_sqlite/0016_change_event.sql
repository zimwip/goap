-- ADR 0029: the change impacts are event-sourced. The log is insert-only; change_impact is its projection.
CREATE TABLE change_event (
    id         TEXT PRIMARY KEY,
    change_id  TEXT    NOT NULL REFERENCES change(id),
    seq        INTEGER NOT NULL,
    impact_id  TEXT    NOT NULL DEFAULT '',
    op         TEXT    NOT NULL,
    flow       TEXT    NOT NULL DEFAULT '',
    execution  TEXT    NOT NULL DEFAULT '',
    by_whom    TEXT    NOT NULL DEFAULT '',
    payload    TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    UNIQUE (change_id, seq)
);
CREATE INDEX change_event_impact ON change_event (change_id, impact_id);
