-- Agent processes of the local development mode, as JSON documents.
CREATE TABLE process (
    id         TEXT PRIMARY KEY,
    status     TEXT NOT NULL,
    created_at TEXT NOT NULL,
    body       TEXT NOT NULL
);
CREATE INDEX process_created ON process (created_at DESC);
