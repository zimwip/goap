-- A change node is a change impact: what the change does to a node is its impact on it.
ALTER TABLE change_node RENAME TO change_impact;
ALTER TABLE node_version RENAME COLUMN change_node TO change_impact;
ALTER INDEX change_node_pkey RENAME TO change_impact_pkey;
ALTER INDEX change_node_node RENAME TO change_impact_node;
ALTER INDEX change_node_live RENAME TO change_impact_live;
ALTER SEQUENCE change_node_seq_seq RENAME TO change_impact_seq_seq;

-- The change set is the change.
ALTER TABLE change_set RENAME TO change;
ALTER INDEX change_set_pkey RENAME TO change_pkey;
ALTER INDEX change_set_parent RENAME TO change_parent;
