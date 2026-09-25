-- Node lifecycle (ADR 0014): a domain is composed of node types, link types
-- and lifecycles. A node type names its lifecycle; its document and
-- change-control declarations are one JSON document.
ALTER TABLE methodology_node_type ADD COLUMN meta jsonb NOT NULL DEFAULT '{}';
ALTER TABLE domain_node_type ADD COLUMN meta jsonb NOT NULL DEFAULT '{}';

CREATE TABLE domain_lifecycle (
    domain_id  uuid  NOT NULL REFERENCES domain(id) ON DELETE CASCADE,
    position   int   NOT NULL,
    name       text  NOT NULL,
    definition jsonb NOT NULL,
    PRIMARY KEY (domain_id, position)
);

-- lifecycles of a methodology that embeds its domain
CREATE TABLE methodology_lifecycle (
    methodology_id uuid  NOT NULL REFERENCES methodology(id) ON DELETE CASCADE,
    position       int   NOT NULL,
    name           text  NOT NULL,
    definition     jsonb NOT NULL,
    PRIMARY KEY (methodology_id, position)
);
