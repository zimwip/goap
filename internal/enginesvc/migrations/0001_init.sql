-- Agent processes, as JSON documents.
CREATE TABLE process (
    id         text PRIMARY KEY,
    status     text NOT NULL,
    created_at text NOT NULL,
    body       text NOT NULL
);
CREATE INDEX process_created ON process (created_at DESC);
