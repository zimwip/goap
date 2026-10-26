-- The protected domains (ADR 0167): a protected domain is refused every write over RPC; only the local command line of
-- the process owning the database (`graph domains ...`) writes it. One row per protected domain, whatever its version.
CREATE TABLE domain_protection (
    name         text        NOT NULL PRIMARY KEY,
    protected_by text        NOT NULL DEFAULT '',
    protected_at text        NOT NULL
);
