-- ADR 0176: a branch reads commits, not versions. A merge that takes the commits of another branch as they are (a
-- fast-forward, the source of a three-way merge) adds them to the target: one row per commit, never per version. A
-- branch holds the versions of its own commits (node_version.branch) and of the commits it took (seq, the order of the
-- commit, commits.seq), each from the order of the commit that took it (at).
DROP TABLE node_branch;
CREATE TABLE branch_commit (
    branch    text   NOT NULL,
    seq       integer NOT NULL,
    at        integer NOT NULL,
    change_id text   NOT NULL,
    PRIMARY KEY (branch, seq)
);
CREATE INDEX branch_commit_seq ON branch_commit (seq);
CREATE INDEX branch_commit_at ON branch_commit (branch, at);
CREATE INDEX branch_commit_change ON branch_commit (change_id);
