-- Attachment of a change to the nodes it edits (ADR 0014): base_version is
-- the version the change starts from. Several open changes may attach the
-- same node; their conflicts are detected when they are applied.
CREATE TABLE change_node (
    seq          bigserial PRIMARY KEY,
    change_id    uuid NOT NULL REFERENCES change_set(id),
    node_id      uuid NOT NULL REFERENCES node(id),
    base_version int  NOT NULL,
    UNIQUE (change_id, node_id)
);
CREATE INDEX change_node_node ON change_node (node_id);
