-- Methodologies for the local development mode (SQLite): one row per
-- version, the structured definition as a JSON document (queryable with the
-- SQLite JSON functions, e.g. json_extract(definition, '$.actions')).
CREATE TABLE methodology (
    name         TEXT NOT NULL,
    version      TEXT NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('draft', 'published', 'archived')),
    definition   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    published_at TEXT,
    updated_by   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (name, version)
);
