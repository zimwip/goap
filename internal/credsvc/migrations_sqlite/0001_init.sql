-- Local sign-in credentials (ADR 0040): one row per subject that registered without an external identity
-- provider. The graph never sees this table; deleting a row cannot be undone (no history, no journal).
CREATE TABLE credential (
    subject       TEXT PRIMARY KEY,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
