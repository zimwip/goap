-- Sign-in sessions (ADR 0045): one row per sign-in, its id carried by every token of the session (claim sid).
-- The gateway refuses the tokens of a session that ended (signed out, password changed) or expired.
CREATE TABLE auth_session (
    id         text        PRIMARY KEY,
    subject    text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE INDEX auth_session_subject ON auth_session (subject);
