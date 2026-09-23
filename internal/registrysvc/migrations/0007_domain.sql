-- Shared domains: the object part (node and link types) of the model,
-- versioned like methodologies and referenced by methodology.domain_ref
-- ("<name>" or "<name>@<version>").
CREATE TABLE domain (
    id           uuid PRIMARY KEY,
    name         text        NOT NULL,
    version      text        NOT NULL,
    description  text        NOT NULL DEFAULT '',
    status       text        NOT NULL CHECK (status IN ('draft', 'published', 'archived')),
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    published_at timestamptz,
    updated_by   text        NOT NULL DEFAULT '',
    UNIQUE (name, version)
);

CREATE TABLE domain_node_type (
    domain_id   uuid   NOT NULL REFERENCES domain(id) ON DELETE CASCADE,
    position    int    NOT NULL,
    name        text   NOT NULL,
    description text   NOT NULL DEFAULT '',
    properties  text[] NOT NULL DEFAULT '{}',
    extends     text   NOT NULL DEFAULT '',
    PRIMARY KEY (domain_id, position)
);

CREATE TABLE domain_link_type (
    domain_id uuid NOT NULL REFERENCES domain(id) ON DELETE CASCADE,
    position  int  NOT NULL,
    name      text NOT NULL,
    from_type text NOT NULL DEFAULT '',
    to_type   text NOT NULL DEFAULT '',
    PRIMARY KEY (domain_id, position)
);

ALTER TABLE methodology ADD COLUMN domain_ref text NOT NULL DEFAULT '';
