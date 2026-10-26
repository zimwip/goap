-- ADR 0161 §3-4: who created a node (the subject and the unit it was created in, the same for every version), who
-- edited each version (attribution), and how far each version is seen, stamped at commit ('' reads as project)
ALTER TABLE node ADD COLUMN creator text NOT NULL DEFAULT '';
ALTER TABLE node ADD COLUMN creator_unit text NOT NULL DEFAULT '';
ALTER TABLE node_version ADD COLUMN editor text NOT NULL DEFAULT '';
ALTER TABLE node_version ADD COLUMN visibility text NOT NULL DEFAULT '';
