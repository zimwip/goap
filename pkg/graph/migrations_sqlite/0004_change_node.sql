-- Attachment of a change to the nodes it edits (ADR 0014).
CREATE TABLE change_node (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    change_id    TEXT NOT NULL REFERENCES change_set(id),
    node_id      TEXT NOT NULL REFERENCES node(id),
    base_version INTEGER NOT NULL,
    UNIQUE (change_id, node_id)
);
CREATE INDEX change_node_node ON change_node (node_id);
