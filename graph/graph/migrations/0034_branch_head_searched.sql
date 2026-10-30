-- ADR 0174 addendum: a commit never updates its branch. The head of a branch is the search of its last commit (by
-- order, index commit_branch); head_commit keeps only the head a branch was created with. A branch is linear: two
-- commits of one branch never stand on the same parent.
CREATE UNIQUE INDEX commit_parent ON commits (namespace, branch, parent_id);
