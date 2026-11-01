-- ADR 0180: the change log is generic; an entry names its impact (subject), the impact its node. The changes acting on
-- a node are read from its versions and the views of the open changes.
DROP INDEX change_log_node;
ALTER TABLE change_log DROP COLUMN node;
