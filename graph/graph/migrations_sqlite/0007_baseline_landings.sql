-- The state of a baseline follows from the versions that landed in it (ADR 0140): every baseline has an order (seq,
-- greater than every baseline before it on its branch), a version and a join record the order of the baseline they
-- landed in on their branch: the state of a baseline is the last landing of each node on its branch up to its order
-- (ADR 0143), nothing is stored.
ALTER TABLE baseline ADD COLUMN seq integer NOT NULL DEFAULT 0;
ALTER TABLE node_version ADD COLUMN landed integer NOT NULL DEFAULT 0;
ALTER TABLE node_branch ADD COLUMN landed integer NOT NULL DEFAULT 0;
DROP INDEX node_version_on_branch;
DROP INDEX node_branch_branch;
CREATE INDEX node_version_landed ON node_version (branch, landed);
CREATE INDEX node_branch_landed ON node_branch (branch, landed);
CREATE INDEX baseline_order ON baseline (seq);
