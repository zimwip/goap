-- Registry: the platform-wide algorithm registry (ADR 0041). A domain still declares its algorithms directly; on
-- publish, each is centralized here so another domain can reference it as "platform@<name>" instead of redeclaring
-- it. The definition is the JSON of algo.Algorithm; the times are RFC 3339 (UTC), as in the local mode.
CREATE TABLE algorithm_registry (
    name           text NOT NULL,
    definition     jsonb NOT NULL,
    source_domain  text NOT NULL,
    source_version text NOT NULL,
    created_at     text NOT NULL,
    updated_at     text NOT NULL,
    PRIMARY KEY (name)
);
