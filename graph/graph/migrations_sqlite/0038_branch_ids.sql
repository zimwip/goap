-- A branch holds its commits by an id of its name (branch_name), not by its name: a row of branch_commit, and each of its
-- indexes, keeps an integer for the branch instead of its name (change-<uuid>), and a graph holds one branch per
-- change. The name stays the branch's (branch, commits, node_version); the reads ask the id of the name.
CREATE TABLE branch_name (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name text    NOT NULL UNIQUE
);
INSERT INTO branch_name (name) SELECT branch FROM branch_commit GROUP BY branch ORDER BY min(seq);
CREATE TABLE branch_commit_of (
    branch    integer NOT NULL,
    seq       integer NOT NULL,
    at        integer NOT NULL,
    change_id text   NOT NULL,
    log_seq   integer
);
INSERT INTO branch_commit_of (branch, seq, at, change_id, log_seq)
SELECT b.id, j.seq, j.at, j.change_id, j.log_seq FROM branch_commit j JOIN branch_name b ON b.name = j.branch;
DROP TABLE branch_commit;
CREATE TABLE branch_commit (
    branch    integer NOT NULL,
    seq       integer NOT NULL,
    at        integer NOT NULL,
    change_id text   NOT NULL,
    log_seq   integer,
    PRIMARY KEY (branch, seq)
);
INSERT INTO branch_commit (branch, seq, at, change_id, log_seq) SELECT branch, seq, at, change_id, log_seq FROM branch_commit_of;
DROP TABLE branch_commit_of;
CREATE INDEX branch_commit_seq ON branch_commit (seq, branch, at);
CREATE INDEX branch_commit_at ON branch_commit (branch, at);
CREATE INDEX branch_commit_change ON branch_commit (change_id);
