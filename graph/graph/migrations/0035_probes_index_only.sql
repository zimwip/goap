-- The probes of a branch (latestHeld) read their index only: the walk of the versions of a node by commit order takes
-- the number of each version from node_version_node_committed, and the order a branch took a commit at from
-- branch_commit_held, with no visit of the rows. A graph growing past what the cache holds otherwise reads a page of
-- the rows for each version walked and each commit asked.
DROP INDEX node_version_node_committed;
CREATE INDEX node_version_node_committed ON node_version (node_id, committed, version);
CREATE INDEX branch_commit_held ON branch_commit (branch, seq, at);
