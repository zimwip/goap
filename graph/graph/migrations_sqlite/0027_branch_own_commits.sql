-- ADR 0176: a branch holds its own commits as it holds the commits it took, one row each, from their order: one way
-- to read a branch.
INSERT INTO branch_commit (branch, seq, at, change_id)
SELECT branch, seq, seq, change_id FROM commits WHERE seq > 0 AND change_id IS NOT NULL
ON CONFLICT DO NOTHING;
