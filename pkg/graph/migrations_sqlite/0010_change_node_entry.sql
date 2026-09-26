-- Change nodes (ADR 0024); the former attachment table (ADR 0014) is kept as
-- change_attachment until the items that fill it are retired.
ALTER TABLE change_node RENAME TO change_attachment;
DROP INDEX change_node_node;
CREATE INDEX change_attachment_node ON change_attachment (node_id);

CREATE TABLE change_node (
    id              TEXT PRIMARY KEY,
    seq             INTEGER NOT NULL,
    change_id       TEXT NOT NULL REFERENCES change_set(id),
    node_id         TEXT REFERENCES node(id),
    key             TEXT NOT NULL,
    type            TEXT NOT NULL,
    intent          TEXT NOT NULL,
    rationale       TEXT NOT NULL,
    pre_version     INTEGER,
    post_version    INTEGER,
    landed_version  INTEGER,
    review          TEXT NOT NULL DEFAULT 'proposed',
    reviews         TEXT NOT NULL DEFAULT '[]',
    via             TEXT REFERENCES change_node(id),
    recheck         INTEGER NOT NULL DEFAULT 0,
    produced_by     TEXT NOT NULL DEFAULT '',
    derived_from    TEXT NOT NULL DEFAULT '[]',
    items           TEXT NOT NULL DEFAULT '[]',
    execution       TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    UNIQUE (change_id, node_id)
);
CREATE INDEX change_node_node ON change_node (node_id);

ALTER TABLE node_version ADD COLUMN change_node TEXT;
ALTER TABLE node_version ADD COLUMN comment TEXT NOT NULL DEFAULT '';
