-- Algorithms (ADR 0018): scripts of a fixed type with typed parameters, and their
-- instances (parameter values), declared by shared domains. Plugs on node types
-- (validators) live in the node type meta, plugs on transitions in the lifecycle
-- definition.
CREATE TABLE domain_algorithm (
    domain_id  uuid  NOT NULL REFERENCES domain(id) ON DELETE CASCADE,
    position   int   NOT NULL,
    name       text  NOT NULL,
    definition jsonb NOT NULL,
    PRIMARY KEY (domain_id, position)
);

CREATE TABLE domain_algorithm_instance (
    domain_id  uuid  NOT NULL REFERENCES domain(id) ON DELETE CASCADE,
    position   int   NOT NULL,
    name       text  NOT NULL,
    definition jsonb NOT NULL,
    PRIMARY KEY (domain_id, position)
);
