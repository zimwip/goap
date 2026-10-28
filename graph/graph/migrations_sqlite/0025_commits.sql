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
-- the commit the branch stood on when a commit was written: what the selection of its state reads past its branch (a
-- change branch moves its fork when it is rebased, the commits it wrote before keep theirs); empty on main
ALTER TABLE commits ADD COLUMN fork_commit text;
UPDATE commits SET fork_commit = (SELECT b.fork_commit FROM branch b WHERE b.namespace = commits.namespace AND b.name = commits.branch) WHERE branch <> 'main';
-- a sub-change forks from a commit of its parent's branch, no longer from an order of it
ALTER TABLE branch DROP COLUMN fork_seq;
