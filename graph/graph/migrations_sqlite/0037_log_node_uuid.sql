-- The node an impact entry of a change log is about is a node id, absent (null) on the entries about no node, in the
-- table and in the index change_log_node (PostgreSQL keeps it as a uuid).
DROP INDEX change_log_node;
ALTER TABLE change_log ADD COLUMN node_of text;
UPDATE change_log SET node_of = node WHERE node <> '';
ALTER TABLE change_log DROP COLUMN node;
ALTER TABLE change_log RENAME COLUMN node_of TO node;
CREATE INDEX change_log_node ON change_log (node, seq, change_id) WHERE node IS NOT NULL;
