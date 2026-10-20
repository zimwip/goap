-- A change commits what it lands in the graph: the order of the commits and the version an impact committed carry
-- that name (formerly landed / landing).
ALTER TABLE node_version RENAME COLUMN landed TO committed;
ALTER TABLE node_branch RENAME COLUMN landed TO committed;
ALTER TABLE change_impact RENAME COLUMN landed_version TO committed_version;
DROP INDEX node_version_landed;
CREATE INDEX node_version_committed ON node_version (branch, committed);
DROP INDEX node_branch_landed;
CREATE INDEX node_branch_committed ON node_branch (branch, committed);
ALTER SEQUENCE landing_seq RENAME TO commit_seq;
