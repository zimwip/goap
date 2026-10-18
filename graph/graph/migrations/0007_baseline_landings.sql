-- The state of a baseline follows from the versions that landed in it (ADR 0140): every baseline has an order (seq,
-- greater than every baseline before it on its branch), a version and a join record the order of the baseline they
-- landed in on their branch, and a baseline kept as a header names the baseline it is stored over (base_id): its state
-- is what landed on its branch since base_id put in the state of base_id. A baseline whose entries are stored names
-- the one its entries are put in (none: they are the whole state). A commit writes no entry.
ALTER TABLE baseline ADD COLUMN seq bigint NOT NULL DEFAULT 0;
ALTER TABLE baseline ADD COLUMN base_id uuid REFERENCES baseline(id);
ALTER TABLE node_version ADD COLUMN landed bigint NOT NULL DEFAULT 0;
ALTER TABLE node_branch ADD COLUMN landed bigint NOT NULL DEFAULT 0;
DROP INDEX node_version_on_branch;
DROP INDEX node_branch_branch;
CREATE INDEX node_version_landed ON node_version (branch, landed);
CREATE INDEX node_branch_landed ON node_branch (branch, landed);
CREATE INDEX baseline_order ON baseline (seq);
-- the last baseline of a branch storing its state, that a header is stored over (a checkpoint)
CREATE INDEX baseline_stored ON baseline (namespace, branch, seq) WHERE gap = 0;
