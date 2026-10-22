-- the changes of a node (sqlNodeChangePage) are read from the index alone: with change_id in it, a node with a long
-- history no longer costs a read of the table per impact
DROP INDEX change_impact_node;
CREATE INDEX change_impact_node ON change_impact (node_id, seq, change_id);
