-- A baseline snapshots exactly one namespace: its nodes are all of that namespace
-- (enforced in pkg/graph.CreateBaseline, not by a DB constraint, same as node_version.branch
-- is not FK-checked against branch.name today).
ALTER TABLE baseline ADD COLUMN namespace text NOT NULL DEFAULT 'default';
CREATE INDEX baseline_namespace ON baseline (namespace, created_at);
