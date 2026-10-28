-- ADR 0175: everything is a commit. The table of the states of a branch is the table of its commits, every reference
-- to one names a commit.
ALTER TABLE baseline RENAME TO commits;
DROP INDEX baseline_namespace;
DROP INDEX baseline_order;
DROP INDEX baseline_branch;
DROP INDEX baseline_change;
CREATE INDEX commit_namespace ON commits (namespace, created_at);
CREATE INDEX commit_order ON commits (seq);
CREATE INDEX commit_branch ON commits (namespace, branch, seq);
CREATE INDEX commit_change ON commits (change_id);
ALTER TABLE branch RENAME COLUMN fork_baseline TO fork_commit;
ALTER TABLE branch RENAME COLUMN head_baseline TO head_commit;
ALTER TABLE change RENAME COLUMN baseline_id TO fork_commit;
ALTER TABLE change RENAME COLUMN result_baseline_id TO landed_commit;
