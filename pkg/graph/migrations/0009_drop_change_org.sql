-- The organisation of a change is its owner unit (owner_org, an OrgUnit key).
DROP INDEX IF EXISTS change_set_org;
ALTER TABLE change_set DROP COLUMN org_id;
