-- Shared domains (node types and link types), versioned like methodologies;
-- the definition is a JSON document.
CREATE TABLE domain (
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
