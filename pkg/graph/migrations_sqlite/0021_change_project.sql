-- A change belongs to a project (ADR 0039): project_id is the key of a ProjectUnit node of the
-- "organisation" namespace, required unless administrative (a change managing organisation/project/
-- policy/adapter data, the admin surface itself).
ALTER TABLE change ADD COLUMN project_id TEXT NOT NULL DEFAULT '';
ALTER TABLE change ADD COLUMN administrative INTEGER NOT NULL DEFAULT 0;
