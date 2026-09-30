-- Local sign-in credentials (ADR 0040): one row per subject that registered without an external identity
-- provider. The graph never sees this table; deleting a row cannot be undone (no history, no journal).
CREATE TABLE credential (
    subject       text        PRIMARY KEY,
    password_hash text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
