-- Every change belongs to an organisation (tenant). Existing changes are
-- backfilled with the default organisation.
ALTER TABLE change_set ADD COLUMN org_id text NOT NULL DEFAULT 'default';
CREATE INDEX change_set_org ON change_set (org_id);
