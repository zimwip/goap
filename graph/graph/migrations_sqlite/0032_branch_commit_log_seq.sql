-- ADR 0174 addendum: a version is never rewritten when a branch takes it. The integration is the row of branch_commit:
-- the change whose merge made the branch take the commit (change_id) and the entry of that change's log recording it
-- (log_seq; none for a commit of the branch's own, or a move of its fork).
ALTER TABLE branch_commit ADD COLUMN log_seq INTEGER;
