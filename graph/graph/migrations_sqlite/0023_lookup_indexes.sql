-- the baselines a change left, by the change: read by the walk of a lineage (DiffBaselines, DiffStacks, the
-- change-stacks: ChangeBaselineHeaders) and by the purge of a change, which read the whole table without it
CREATE INDEX baseline_change ON baseline (change_id);
-- the nodes of a type, by id: a type read a batch at a time (NodeIDsOfType, the label anchor of a read query)
CREATE INDEX node_type ON node (namespace, type, id);
