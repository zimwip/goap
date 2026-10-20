-- ADR 0143: the state of a node at a baseline is a selection of the versions landed, no baseline stores entries. The
-- schema starts from zero without them; a database created before drops what is left (its baseline columns gap, depth
-- and base_id stay, unread).
DROP TABLE IF EXISTS baseline_entry;
DROP INDEX IF EXISTS baseline_stored;
DROP INDEX IF EXISTS baseline_underived;
-- The version of a node on a branch at an order of landing: one lookup, however long the history of the node.
CREATE INDEX IF NOT EXISTS node_version_fold ON node_version (node_id, branch, landed);
CREATE INDEX IF NOT EXISTS node_branch_fold ON node_branch (node_id, branch, landed);
