-- The protected domains (ADR 0167), local mode. See migrations/0003_domain_protection.sql.
CREATE TABLE domain_protection (
    name         TEXT NOT NULL PRIMARY KEY,
    protected_by TEXT NOT NULL DEFAULT '',
    protected_at TEXT NOT NULL
);
