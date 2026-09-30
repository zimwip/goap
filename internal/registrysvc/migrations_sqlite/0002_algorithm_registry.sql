-- Registry: the platform-wide algorithm registry (ADR 0041), local mode. See migrations/0002_algorithm_registry.sql.
CREATE TABLE algorithm_registry (
    name           TEXT NOT NULL,
    definition     TEXT NOT NULL,
    source_domain  TEXT NOT NULL,
    source_version TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (name)
);
