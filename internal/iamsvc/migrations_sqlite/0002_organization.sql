-- Organisations (tenants). The default organisation owns every pre-existing change.
CREATE TABLE organization (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
INSERT INTO organization (id, name) VALUES ('default', 'Default organisation');
