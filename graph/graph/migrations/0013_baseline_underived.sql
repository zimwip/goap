-- ADR 0140, ADR 0137 addendum: the version of a node in a baseline is its last landing on the branch, down to
-- the last baseline whose state does not follow from them (a snapshot, a rebase, a revert), found by this index.
CREATE INDEX baseline_underived ON baseline (namespace, branch, seq) WHERE kind NOT IN ('commit', 'merge', 'fast-forward');
-- The version of a node on a branch at an order of landing: one lookup, however long the history of the node.
CREATE INDEX node_version_fold ON node_version (node_id, branch, landed);
CREATE INDEX node_branch_fold ON node_branch (node_id, branch, landed);
