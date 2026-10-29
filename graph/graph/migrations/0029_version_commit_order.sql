-- ADR 0176: the versions read by the commits that wrote them. By node (node_version_node_committed), a read of a branch
-- walks only the versions committed within the orders the branch holds, not the whole history of the node. By commit
-- (node_version_commit, an order being one commit's), the versions a branch holds are read from its commits with no
-- detour through the commit headers; it replaces the index by branch and order.
DROP INDEX node_version_committed;
CREATE INDEX node_version_node_committed ON node_version (node_id, committed);
CREATE INDEX node_version_commit ON node_version (committed, node_id);
