-- the nodes a branch holds a version of, written on it or joined to it: what a change's own branch changed since its
-- fork, read without the state of its baselines (ADR 0136)
CREATE INDEX node_version_on_branch ON node_version (branch);
CREATE INDEX node_branch_branch ON node_branch (branch);
