-- Flows on change nodes (ADR 0025): the version records the action run that wrote it, a change
-- node the flow that declared it and whether an adopted flow replaced it. A flow may declare
-- a change node for a node the main flow declared (the stale one, superseded once the flow is adopted):
-- the node is unique per flow among the change nodes that are not superseded.
ALTER TABLE node_version ADD COLUMN execution TEXT NOT NULL DEFAULT '';

CREATE TABLE change_node_new (
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
    via             TEXT REFERENCES change_node_new(id),
    recheck         INTEGER NOT NULL DEFAULT 0,
    produced_by     TEXT NOT NULL DEFAULT '',
    derived_from    TEXT NOT NULL DEFAULT '[]',
    items           TEXT NOT NULL DEFAULT '[]',
    execution       TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    flow            TEXT NOT NULL DEFAULT '',
    superseded      INTEGER NOT NULL DEFAULT 0
);
INSERT INTO change_node_new (id, seq, change_id, node_id, key, type, intent, rationale, pre_version, post_version, landed_version, review, reviews, via, recheck, produced_by, derived_from, items, execution, created_at)
    SELECT id, seq, change_id, node_id, key, type, intent, rationale, pre_version, post_version, landed_version, review, reviews, via, recheck, produced_by, derived_from, items, execution, created_at FROM change_node;
DROP TABLE change_node;
ALTER TABLE change_node_new RENAME TO change_node;
CREATE INDEX change_node_node ON change_node (node_id);
CREATE UNIQUE INDEX change_node_live ON change_node (change_id, node_id, flow) WHERE superseded = 0;
