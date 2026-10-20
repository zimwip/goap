-- A version is read by its key (node_id, version): node_version_pkey. The indexes that also lead with node_id tied with
-- it for the planner, which estimates one row for a node and so may scan every version of a node with a deep history,
-- version by version, to find one (each read of a hot node growing with its history). They lead with the column their
-- own lookups name first, so that only the primary key serves a lookup by node.
DROP INDEX node_version_fold;
CREATE INDEX node_version_fold ON node_version (branch, node_id, landed);
DROP INDEX node_branch_fold;
CREATE INDEX node_branch_fold ON node_branch (branch, node_id, landed);
DROP INDEX node_version_branch;
CREATE INDEX node_version_branch ON node_version (branch, node_id, version DESC);
DROP INDEX node_version_links;
CREATE INDEX node_version_links ON node_version (links, node_id);
DROP INDEX node_version_links_base;
CREATE INDEX node_version_links_base ON node_version (links_base, node_id);
