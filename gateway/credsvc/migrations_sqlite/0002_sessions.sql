-- Sign-in sessions (ADR 0045): one row per sign-in, its id carried by every token of the session (claim sid).
-- The gateway refuses the tokens of a session that ended (signed out, password changed) or expired.
CREATE TABLE auth_session (
    id         TEXT PRIMARY KEY,
    subject    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT
);
CREATE INDEX auth_session_subject ON auth_session (subject);
