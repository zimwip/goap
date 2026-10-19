-- ADR 0140: a history is read a page at a time and from where it is asked, never whole.
-- The changes acting on a node, newest first, and the open ones at each landing: by node, then order.
DROP INDEX change_impact_node;
CREATE INDEX change_impact_node ON change_impact (node_id, seq);
-- The baselines of one branch (the change-stack of a difference walks the branch of a change it takes in).
CREATE INDEX baseline_branch ON baseline (namespace, branch, seq);
