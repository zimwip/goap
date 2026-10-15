-- Registry: the domain versions (ADR 0023), local mode. See migrations/0001_domain_version.sql.
CREATE TABLE domain_version (
    name         TEXT NOT NULL,
    version      TEXT NOT NULL,
    status       TEXT NOT NULL,
    definition   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    published_at TEXT NOT NULL DEFAULT '',
    updated_by   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (name, version)
);
