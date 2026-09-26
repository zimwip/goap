-- A change node is a change impact: what the change does to a node is its impact on it.
ALTER TABLE change_node RENAME TO change_impact;
ALTER TABLE node_version RENAME COLUMN change_node TO change_impact;
DROP INDEX change_node_node;
DROP INDEX change_node_live;
CREATE INDEX change_impact_node ON change_impact (node_id);
CREATE UNIQUE INDEX change_impact_live ON change_impact (change_id, node_id, flow) WHERE superseded = 0;

-- The change set is the change.
ALTER TABLE change_set RENAME TO change;
DROP INDEX change_set_parent;
CREATE INDEX change_parent ON change (parent_id);
