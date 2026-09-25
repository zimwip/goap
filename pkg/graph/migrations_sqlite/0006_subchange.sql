-- Sub-changes: a change split along organisational boundaries. owner_org is the
-- key of an OrgUnit node of the "organisation" namespace.
ALTER TABLE change_set ADD COLUMN parent_id TEXT REFERENCES change_set(id);
ALTER TABLE change_set ADD COLUMN owner_org TEXT NOT NULL DEFAULT '';
CREATE INDEX change_set_parent ON change_set (parent_id);
