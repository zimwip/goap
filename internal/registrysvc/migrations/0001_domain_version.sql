-- Registry: the domain versions (ADR 0023). A domain is the model of a namespace (node types, link types, lifecycles,
-- algorithms); each graph holds it in memory (the type catalogue) to check its data. The definition is the JSON of
-- methodology.Domain; the times are RFC 3339 (UTC), as in the local mode.
CREATE TABLE domain_version (
    name         text        NOT NULL,
    version      text        NOT NULL,
    status       text        NOT NULL,
    definition   jsonb       NOT NULL,
    created_at   text        NOT NULL,
    updated_at   text        NOT NULL,
    published_at text        NOT NULL DEFAULT '',
    updated_by   text        NOT NULL DEFAULT '',
    PRIMARY KEY (name, version)
);
