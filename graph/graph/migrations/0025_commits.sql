-- ADR 0175: everything is a commit. The table of the states of a branch is the table of its commits, every reference
-- to one names a commit.
ALTER TABLE baseline RENAME TO commits;
ALTER INDEX baseline_namespace RENAME TO commit_namespace;
ALTER INDEX baseline_order RENAME TO commit_order;
ALTER INDEX baseline_branch RENAME TO commit_branch;
ALTER INDEX baseline_change RENAME TO commit_change;
ALTER TABLE branch RENAME COLUMN fork_baseline TO fork_commit;
ALTER TABLE branch RENAME COLUMN head_baseline TO head_commit;
ALTER TABLE change RENAME COLUMN baseline_id TO fork_commit;
ALTER TABLE change RENAME COLUMN result_baseline_id TO landed_commit;
